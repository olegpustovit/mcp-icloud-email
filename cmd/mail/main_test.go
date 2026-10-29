package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/tools"
)

type mailboxStub struct {
	tools.EmailService
	email string
}

func (m mailboxStub) ListFolders(context.Context) ([]string, error) { return []string{m.email}, nil }
func TestAuthenticatedMCPIdentityIsolation(t *testing.T) {
	server := mailmcp.New(func(ctx context.Context) (mailmcp.Dependencies, error) {
		email, _ := ctx.Value(principalKey{}).(string)
		if email == "" {
			return mailmcp.Dependencies{}, errors.New("missing request identity")
		}
		return mailmcp.Dependencies{IMAP: mailboxStub{email: email}}, nil
	}, "test")
	authenticate := func(r *http.Request) (string, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer alice":
			return "alice@olegpustovit.com", nil
		case "Bearer ursus":
			return "ursus95@gmail.com", nil
		}
		return "", errors.New("unauthorized")
	}
	ts := httptest.NewServer(protect(authenticate, "https://mail.microapps.space", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})))
	defer ts.Close()
	for _, tc := range []struct{ token, email string }{{"alice", "alice@olegpustovit.com"}, {"ursus", "ursus95@gmail.com"}} {
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		httpClient := &http.Client{Transport: bearerTransport{token: tc.token}}
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: httpClient}, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_folders"})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("tool failure: %+v", result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, tc.email) {
			t.Fatalf("wrong identity result: %+v", result.Content)
		}
		session.Close()
	}
	for _, origin := range []string{"", "https://evil.example"} {
		req, _ := http.NewRequest("POST", ts.URL, nil)
		req.Host = "mail.microapps.space"
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 401
		if origin != "" {
			want = 403
		}
		if resp.StatusCode != want {
			t.Fatalf("status=%d want %d", resp.StatusCode, want)
		}
		if want == 401 && resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="https://mail.microapps.space/.well-known/oauth-protected-resource", scope="mail"` {
			t.Fatal("missing OAuth discovery challenge")
		}
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = "mail.microapps.space"
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestProductionRoutes(t *testing.T) {
	handler, err := newHandler(&auth.Service{Config: auth.Config{BaseURL: "https://mail.microapps.space"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/", 200}, {"/healthz", 200}, {"/api/me", 401}, {"/api/mail/folders", 401}, {"/api/mail/outbox", 401}, {"/api/mail/preferences", 401}, {"/mcp", 401}, {"/.well-known/oauth-authorization-server", 200}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Host = "mail.microapps.space"
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s status %d", tc.path, w.Code)
		}
	}
}
