package authhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRedirectValidation(t *testing.T) {
	for _, v := range []struct {
		uri  string
		want bool
	}{{"https://chatgpt.com/connector_platform/oauth/callback", true}, {"http://localhost:123/cb", true}, {"https://evil.com/#fragment", false}, {"https://user:pass@example.com/cb", false}, {"http://example.com/cb", false}, {"javascript:alert(1)", false}, {"//evil.com", false}} {
		if validRedirect(v.uri) != v.want {
			t.Errorf("redirect %s", v.uri)
		}
	}
}
func TestUnauthorizedAndOrigin(t *testing.T) {
	h := New(&auth.Service{Config: auth.Config{BaseURL: "https://mail.example.com"}})
	mux := http.NewServeMux()
	h.Register(mux)
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/api/me", 401}, {"PUT", "/api/account", 403}, {"POST", "/auth/logout", 403}, {"GET", "/.well-known/oauth-protected-resource/mcp", 200}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s: %d", tc.path, w.Code)
		}
	}
}
func TestOAuthPKCEIntegration(t *testing.T) {
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	ctx := context.Background()
	s, e := auth.New(ctx, auth.Config{BaseURL: "https://mail.example.com", MongoURI: uri, ResendAPIKey: "test", EmailFrom: "test@example.com", EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx)
	h := New(s)
	mux := http.NewServeMux()
	h.Register(mux)
	request := func(path string, values url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(w, r)
		return w
	}
	// Exercise registration and explicit browser consent before token exchange.
	registrationBody := `{"redirect_uris":["https://client.example.com/cb"],"client_name":"Test client","token_endpoint_auth_method":"none","client_uri":"https://client.example.com","contacts":["support@client.example.com"],"custom_metadata":"ignored"}`
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest("POST", "/oauth/register", strings.NewReader(registrationBody)))
	if rw.Code != 201 {
		t.Fatal("registration failed", rw.Body.String())
	}
	var registered struct {
		ID string `json:"client_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &registered)
	defer s.Delete(ctx, registered.ID)
	browserSession := auth.Random()
	if e = s.Put(ctx, auth.Record{ID: auth.Hash(browserSession), Kind: "session", Email: "ursus95@gmail.com", Expires: time.Now().Add(time.Minute)}); e != nil {
		t.Fatal(e)
	}
	defer s.Delete(ctx, browserSession)
	browserVerifier := auth.Random()
	query := url.Values{"client_id": {registered.ID}, "redirect_uri": {"https://client.example.com/cb"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {auth.Hash(browserVerifier)}, "state": {"client-state"}}
	path := "/oauth/authorize?" + query.Encode()
	getConsent := httptest.NewRequest("GET", path, nil)
	getConsent.AddCookie(&http.Cookie{Name: "__Host-mail_session", Value: browserSession})
	rw = httptest.NewRecorder()
	mux.ServeHTTP(rw, getConsent)
	if rw.Code != 200 {
		t.Fatal("consent page failed", rw.Code, rw.Body.String())
	}
	match := regexp.MustCompile(`name="consent" value="([^"]+)"`).FindStringSubmatch(rw.Body.String())
	if len(match) != 2 {
		t.Fatal("missing consent nonce")
	}
	form := url.Values{"consent": {match[1]}, "decision": {"allow"}}
	postConsent := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	postConsent.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postConsent.Header.Set("Origin", "https://mail.example.com")
	postConsent.AddCookie(&http.Cookie{Name: "__Host-mail_session", Value: browserSession})
	rw = httptest.NewRecorder()
	mux.ServeHTTP(rw, postConsent)
	if rw.Code != 303 {
		t.Fatal("consent failed", rw.Code, rw.Body.String())
	}
	location, _ := url.Parse(rw.Header().Get("Location"))
	if location.Query().Get("state") != "client-state" || location.Query().Get("code") == "" {
		t.Fatal("bad authorization response")
	}
	defer s.Delete(ctx, location.Query().Get("code"))
	verifier := auth.Random()
	clientID := auth.Random()
	newCode := func() string {
		code := auth.Random()
		e = s.Put(ctx, auth.Record{ID: auth.Hash(code), Kind: "code", Email: "ursus95@gmail.com", ClientID: clientID, Redirect: "https://client.example.com/cb", Challenge: auth.Hash(verifier), Resource: "https://mail.example.com/mcp", Expires: time.Now().Add(time.Minute)})
		if e != nil {
			t.Fatal(e)
		}
		return code
	}
	values := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {"https://client.example.com/cb"}, "code_verifier": {verifier}, "code": {newCode()}}
	bad := url.Values{}
	for k, v := range values {
		bad[k] = append([]string(nil), v...)
	}
	bad.Set("code_verifier", auth.Random())
	if w := request("/oauth/token", bad); w.Code != 400 {
		t.Fatal("bad PKCE accepted")
	}
	values.Set("code", newCode())
	w := request("/oauth/token", values)
	if w.Code != 200 {
		t.Fatalf("exchange %d %s", w.Code, w.Body.String())
	}
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(w.Body.Bytes(), &tokens) != nil || tokens.Access == "" || tokens.Refresh == "" {
		t.Fatal("missing tokens")
	}
	defer s.Revoke(ctx, tokens.Access)
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tokens.Access)
	if email, e := s.Authenticate(r); e != nil || email != "ursus95@gmail.com" {
		t.Fatal("bearer rejected", e)
	}
	if w = request("/oauth/token", values); w.Code != 400 {
		t.Fatal("code replay accepted")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tokens.Refresh}}
	if w = request("/oauth/token", refresh); w.Code != 200 {
		t.Fatal("refresh failed")
	}
	if w = request("/oauth/token", refresh); w.Code != 400 {
		t.Fatal("refresh replay accepted")
	}
	if e = s.Revoke(ctx, tokens.Access); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(r); e == nil {
		t.Fatal("revocation failed")
	}
}
func TestNativeLoginAndResetIntegration(t *testing.T) {
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	ctx := context.Background()
	s, e := auth.New(ctx, auth.Config{BaseURL: "https://mail.example.com", MongoURI: uri, ResendAPIKey: "test", EmailFrom: "test@example.com", EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx)
	h := New(s)
	mux := http.NewServeMux()
	h.Register(mux)
	email := "native-" + strings.ToLower(auth.Random()) + "@olegpustovit.com"
	password := "correct horse battery staple"
	hash, e := auth.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	verify := auth.Random()
	if e = s.Put(ctx, auth.Record{ID: auth.Hash(verify), Kind: "verify", Email: email, Data: hash, Expires: time.Now().Add(time.Minute)}); e != nil {
		t.Fatal(e)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Origin", "https://mail.example.com")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	loginBody, _ := json.Marshal(map[string]string{"email": email, "password": password, "next": "//evil.com"})
	if w := post("/auth/login", string(loginBody)); w.Code != 401 {
		t.Fatal("unverified user logged in")
	}
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest("GET", "/auth/verify?token="+verify, nil))
	if rw.Code != 303 || rw.Header().Get("Location") != "/?verified=1" || len(rw.Result().Cookies()) != 1 {
		t.Fatal("verification failed", rw.Code)
	}
	cookie := rw.Result().Cookies()[0]
	defer s.Delete(ctx, cookie.Value)
	if w := post("/auth/login", string(loginBody)); w.Code != 200 || !strings.Contains(w.Body.String(), `"next":"/"`) {
		t.Fatal("verified login failed", w.Code, w.Body.String())
	} else {
		for _, c := range w.Result().Cookies() {
			defer s.Delete(ctx, c.Value)
		}
	}
	reset := auth.Random()
	if e = s.Put(ctx, auth.Record{ID: auth.Hash(reset), Kind: "reset", Email: email, Expires: time.Now().Add(time.Minute)}); e != nil {
		t.Fatal(e)
	}
	resetBody, _ := json.Marshal(map[string]string{"token": reset, "password": "a brand new password value"})
	if w := post("/auth/reset", string(resetBody)); w.Code != 204 {
		t.Fatal("password reset failed", w.Body.String())
	}
	if w := post("/auth/reset", string(resetBody)); w.Code != 400 {
		t.Fatal("reset replay accepted")
	}
	if w := post("/auth/login", string(loginBody)); w.Code != 401 {
		t.Fatal("old password accepted")
	}
	me := httptest.NewRequest("GET", "/api/me", nil)
	me.AddCookie(cookie)
	rw = httptest.NewRecorder()
	mux.ServeHTTP(rw, me)
	if rw.Code != 401 {
		t.Fatal("password reset retained session")
	}
}

type emailRoundTripper func(*http.Request) (*http.Response, error)

func (f emailRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestEmailPayload(t *testing.T) {
	h := New(&auth.Service{Config: auth.Config{ResendAPIKey: "test-secret", EmailFrom: "Mail <mail@example.com>"}})
	h.emailClient = &http.Client{Transport: emailRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.resend.com/emails" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatal("wrong email delivery target or credentials")
		}
		var body struct {
			From          string
			To            []string
			Subject, HTML string
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.From != "Mail <mail@example.com>" || len(body.To) != 1 || body.To[0] != "ursus95@gmail.com" || !strings.Contains(body.HTML, "&amp;") {
			t.Fatal("incorrect or unescaped email payload")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"email-id"}`)), Header: http.Header{}}, nil
	})}
	if e := h.sendEmail(context.Background(), "ursus95@gmail.com", "Verify", "Continue", "https://mail.example.com/verify?token=x&next=y"); e != nil {
		t.Fatal(e)
	}
}

func TestMetadataAliasAndCORS(t *testing.T) {
	h := New(&auth.Service{Config: auth.Config{BaseURL: "https://mail.example.com"}})
	mux := http.NewServeMux()
	h.Register(mux)
	var root string
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("metadata %s: %d", path, w.Code)
		}
		if root == "" {
			root = w.Body.String()
		} else if w.Body.String() != root {
			t.Fatal("metadata alias differs")
		}
	}
	for _, path := range []string{"/.well-known/oauth-authorization-server/mcp", "/.well-known/oauth-protected-resource/mcp", "/oauth/register", "/oauth/token", "/oauth/revoke"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("OPTIONS", path, nil)
		r.Header.Set("Origin", "https://grok.com")
		r.Header.Set("Access-Control-Request-Method", "POST")
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Headers") == "" {
			t.Fatalf("preflight %s: %d", path, w.Code)
		}
	}
}
