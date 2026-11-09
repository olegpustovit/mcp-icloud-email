package mailhttp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
)

func TestPushAcceptsBrowserSubscriptionJSON(t *testing.T) {
	h := &handler{state: &mailsync.Service{}, owner: func(context.Context) string { return "owner" }}
	for _, tc := range []struct {
		name, extra string
		status      int
	}{
		{"no expiration", "", 401},
		{"null expiration", `,"expirationTime":null`, 401},
		{"numeric expiration", `,"expirationTime":1795000000000`, 401},
		{"unknown subscription field", `,"owner":"other"`, 400},
		{"invalid expiration", `,"expirationTime":"tomorrow"`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"subscription":{"endpoint":"https://push.example.test/device","keys":{"auth":"test","p256dh":"test"}` + tc.extra + `},"name":"Safari","zone":"Europe/Zagreb"}`
			r := httptest.NewRequest("POST", "/api/mail/push", strings.NewReader(body))
			w := httptest.NewRecorder()
			h.push(w, r)
			// No session cookie: valid browser JSON must reach the authentication check.
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
