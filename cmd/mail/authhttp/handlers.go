package authhttp

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	icloudimap "github.com/olegpustovit/mcp-icloud-email/imap"
	"html/template"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
)

type Handler struct {
	InvalidateAccount func(context.Context, string) error
	s                 *auth.Service
	emailClient       *http.Client
	mu                sync.Mutex
	requests          int
	emailRequests     int
	window            time.Time
}

func New(s *auth.Service) *Handler { return &Handler{s: s, emailClient: http.DefaultClient} }
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", h.limited(h.login))
	mux.HandleFunc("POST /auth/register", h.limited(h.signup))
	mux.HandleFunc("GET /auth/verify", h.limited(h.verify))
	mux.HandleFunc("POST /auth/forgot", h.limited(h.forgot))
	mux.HandleFunc("POST /auth/reset", h.limited(h.reset))
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("GET /api/me", h.me)
	mux.HandleFunc("PUT /api/account", h.account)
	mux.HandleFunc("DELETE /api/account", h.account)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", publicOAuth(h.metadata))
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", publicOAuth(h.metadata))
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/oauth/register", "/oauth/token", "/oauth/revoke"} {
		mux.HandleFunc("OPTIONS "+path, publicOAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", publicOAuth(h.resource))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", publicOAuth(h.resource))
	mux.HandleFunc("POST /oauth/register", publicOAuth(h.limited(h.register)))
	mux.HandleFunc("GET /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/token", publicOAuth(h.limited(h.token)))
	mux.HandleFunc("POST /oauth/revoke", publicOAuth(h.revoke))
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error { return decodeJSON(w, r, v, true) }
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, strict bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	if strict {
		d.DisallowUnknownFields()
	}
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("unexpected data")
	}
	return nil
}
func (h *Handler) origin(r *http.Request) bool { return r.Header.Get("Origin") == h.s.Config.BaseURL }
func (h *Handler) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (h *Handler) session(r *http.Request) (string, error) {
	c, e := r.Cookie("__Host-mail_session")
	if e != nil {
		return "", auth.ErrUnauthorized
	}
	v, e := h.s.Get(r.Context(), c.Value, "session", false)
	if e != nil {
		return "", e
	}
	if !auth.Allowed(v.Email) {
		return "", auth.ErrUnauthorized
	}
	return v.Email, nil
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	if c, e := r.Cookie("__Host-mail_session"); e == nil {
		if e = h.s.Delete(r.Context(), c.Value); e != nil {
			fail(w, 500, "logout_failed")
			return
		}
	}
	h.cookie(w, "__Host-mail_session", "", -1)
	w.WriteHeader(204)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	email, e := h.session(r)
	if e != nil {
		fail(w, 401, "login_required")
		return
	}
	a, e := h.s.Account(r.Context(), email)
	if e != nil {
		fail(w, 500, "account_unavailable")
		return
	}
	jsonResponse(w, 200, struct {
		Email       string `json:"email"`
		Connected   bool   `json:"connected"`
		ICloudEmail string `json:"icloud_email"`
	}{email, len(a.Secret) > 0, a.ICloudEmail})
}
func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	email, e := h.session(r)
	if e != nil {
		fail(w, 401, "login_required")
		return
	}
	if r.Method == "DELETE" {
		if h.InvalidateAccount != nil {
			if err := h.InvalidateAccount(r.Context(), email); err != nil {
				fail(w, 500, "account_update_failed")
				return
			}
		}
		e = h.s.DeleteAccount(r.Context(), email)
	} else {
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if decode(w, r, &input) != nil {
			fail(w, 400, "invalid_json")
			return
		}
		input.Email = strings.TrimSpace(input.Email)
		a, err := mail.ParseAddress(input.Email)
		if err != nil || a.Address != input.Email || len(input.Password) < 8 || len(input.Password) > 256 {
			fail(w, 400, "valid_email_and_app_specific_password_required")
			return
		}
		connection, err := icloudimap.NewClient(input.Email, input.Password)
		if err != nil {
			fail(w, 400, "icloud_login_failed_check_app_specific_password")
			return
		}
		_, err = connection.ListFolders(r.Context())
		closeErr := connection.Close()
		if err != nil || closeErr != nil {
			fail(w, 502, "icloud_connection_failed")
			return
		}
		if h.InvalidateAccount != nil {
			if err := h.InvalidateAccount(r.Context(), email); err != nil {
				fail(w, 500, "account_update_failed")
				return
			}
		}
		e = h.s.SaveAccount(r.Context(), email, input.Email, input.Password)
	}
	if e != nil {
		fail(w, 500, "account_update_failed")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) metadata(w http.ResponseWriter, r *http.Request) {
	b := h.s.Config.BaseURL
	jsonResponse(w, 200, map[string]any{"issuer": b, "authorization_endpoint": b + "/oauth/authorize", "token_endpoint": b + "/oauth/token", "registration_endpoint": b + "/oauth/register", "revocation_endpoint": b + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"mail"}})
}
func (h *Handler) resource(w http.ResponseWriter, r *http.Request) {
	b := h.s.Config.BaseURL
	jsonResponse(w, 200, map[string]any{"resource": b + "/mcp", "authorization_servers": []string{b}, "scopes_supported": []string{"mail"}, "bearer_methods_supported": []string{"header"}, "resource_name": "iCloud Mail"})
}

type registration struct {
	RedirectURIs  []string `json:"redirect_uris"`
	ClientName    string   `json:"client_name,omitempty"`
	TokenAuth     string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes    []string `json:"grant_types,omitempty"`
	ResponseTypes []string `json:"response_types,omitempty"`
}

func validRedirect(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.User == nil && u.Fragment == "" && u.Host != "" && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")))
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in registration
	if decodeJSON(w, r, &in, false) != nil || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 || len(in.ClientName) > 100 || (in.TokenAuth != "" && in.TokenAuth != "none") {
		fail(w, 400, "invalid_client_metadata")
		return
	}
	for _, uri := range in.RedirectURIs {
		if !validRedirect(uri) {
			fail(w, 400, "invalid_redirect_uri")
			return
		}
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			fail(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, v := range in.ResponseTypes {
		if v != "code" {
			fail(w, 400, "invalid_client_metadata")
			return
		}
	}
	id := auth.Random()
	data, _ := json.Marshal(in)
	if e := h.s.Put(r.Context(), auth.Record{ID: auth.Hash(id), Kind: "client", Data: string(data), Expires: time.Now().Add(365 * 24 * time.Hour)}); e != nil {
		fail(w, 500, "registration_failed")
		return
	}
	jsonResponse(w, 201, map[string]any{"client_id": id, "client_name": in.ClientName, "redirect_uris": in.RedirectURIs, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
}

//go:embed consent.html
var consentHTML string

var consent = template.Must(template.New("consent").Parse(consentHTML))

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	cr, e := h.s.Get(r.Context(), clientID, "client", false)
	var client registration
	if e != nil || json.Unmarshal([]byte(cr.Data), &client) != nil {
		fail(w, 400, "invalid_client")
		return
	}
	requestedRedirect := q.Get("redirect_uri")
	redirect := ""
	for _, v := range client.RedirectURIs {
		if v == requestedRedirect {
			redirect = v
		}
	}
	if redirect == "" {
		fail(w, 400, "invalid_redirect_uri")
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("scope") != "" && q.Get("scope") != "mail" || (q.Get("resource") != "" && q.Get("resource") != h.s.Config.BaseURL+"/mcp") {
		fail(w, 400, "invalid_request")
		return
	}
	email, e := h.session(r)
	if e != nil {
		http.Redirect(w, r, "/?login=1&next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	if r.Method == "GET" {
		t := auth.Random()
		if e = h.s.Put(r.Context(), auth.Record{ID: auth.Hash(t), Kind: "consent", Email: email, Data: r.URL.RawQuery, Expires: time.Now().Add(10 * time.Minute)}); e != nil {
			fail(w, 500, "authorization_unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Preserve the form's Origin and prevent the proxy from rewriting the account email.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		callback, _ := url.Parse(redirect)
		// Browsers also check form-action on redirects; escape CSP directive delimiters in client metadata.
		callbackOrigin := strings.ReplaceAll(callback.Scheme+"://"+callback.Host, ";", "%3B")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+callbackOrigin+"; frame-ancestors 'none'; base-uri 'none'")
		name := client.ClientName
		if name == "" {
			name = "Your AI assistant"
		}
		_ = consent.Execute(w, struct{ Name, Email, Token string }{name, email, t})
		return
	}
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		fail(w, 400, "invalid_request")
		return
	}
	c, e := h.s.Get(r.Context(), r.PostForm.Get("consent"), "consent", true)
	if e != nil || c.Email != email || c.Data != r.URL.RawQuery {
		fail(w, 400, "invalid_consent")
		return
	}
	u, _ := url.Parse(redirect)
	params := u.Query()
	params.Set("state", q.Get("state"))
	if r.PostForm.Get("decision") != "allow" {
		params.Set("error", "access_denied")
	} else {
		code := auth.Random()
		e = h.s.Put(r.Context(), auth.Record{ID: auth.Hash(code), Kind: "code", Email: email, ClientID: clientID, Redirect: redirect, Challenge: q.Get("code_challenge"), Resource: h.s.Config.BaseURL + "/mcp", Expires: time.Now().Add(5 * time.Minute)})
		if e != nil {
			fail(w, 500, "authorization_failed")
			return
		}
		params.Set("code", code)
	}
	u.RawQuery = params.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		fail(w, 400, "invalid_request")
		return
	}
	f := r.PostForm
	var rec auth.Record
	var e error
	switch f.Get("grant_type") {
	case "authorization_code":
		rec, e = h.s.Get(r.Context(), f.Get("code"), "code", true)
		if e != nil || rec.ClientID != f.Get("client_id") || rec.Redirect != f.Get("redirect_uri") || len(f.Get("code_verifier")) < 43 || len(f.Get("code_verifier")) > 128 || auth.Hash(f.Get("code_verifier")) != rec.Challenge {
			fail(w, 400, "invalid_grant")
			return
		}
		rec.Family = auth.Random()
	case "refresh_token":
		rec, e = h.s.Get(r.Context(), f.Get("refresh_token"), "refresh", true)
		if e != nil || rec.ClientID != f.Get("client_id") {
			fail(w, 400, "invalid_grant")
			return
		}
	default:
		fail(w, 400, "unsupported_grant_type")
		return
	}
	if !auth.Allowed(rec.Email) || (f.Get("resource") != "" && f.Get("resource") != rec.Resource) {
		fail(w, 400, "invalid_target")
		return
	}
	access, refresh := auth.Random(), auth.Random()
	rec.ID = auth.Hash(access)
	rec.Kind = "access"
	rec.Expires = time.Now().Add(time.Hour)
	if e = h.s.Put(r.Context(), rec); e != nil {
		fail(w, 500, "token_failed")
		return
	}
	rec.ID = auth.Hash(refresh)
	rec.Kind = "refresh"
	rec.Expires = time.Now().Add(30 * 24 * time.Hour)
	if e = h.s.Put(r.Context(), rec); e != nil {
		_ = h.s.Delete(r.Context(), access)
		fail(w, 500, "token_failed")
		return
	}
	jsonResponse(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": "mail"})
}
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		fail(w, 400, "invalid_request")
		return
	}
	if e := h.s.Revoke(r.Context(), r.PostForm.Get("token")); e != nil {
		fail(w, 500, "revocation_failed")
		return
	}
	w.WriteHeader(200)
}

// One bounded global bucket also limits requests when a reverse proxy hides client IPs.
func (h *Handler) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		now := time.Now()
		if now.Sub(h.window) >= time.Minute {
			h.window = now
			h.requests = 0
			h.emailRequests = 0
		}
		h.requests++
		allowed := h.requests <= 120
		if r.URL.Path == "/auth/register" || r.URL.Path == "/auth/forgot" {
			h.emailRequests++
			allowed = allowed && h.emailRequests <= 10
		}
		h.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "rate_limited")
			return
		}
		next(w, r)
	}
}

func publicOAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		next(w, r)
	}
}
