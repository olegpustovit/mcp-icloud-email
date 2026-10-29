package mailhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"github.com/olegpustovit/mcp-icloud-email/tools"
)

type mailboxFake struct {
	tools.EmailService
	marked     bool
	attachment []byte
}

func (f *mailboxFake) UIDValidity(context.Context, string) (uint32, error) { return 7, nil }
func (f *mailboxFake) MarkRead(_ context.Context, folder, id string, read bool) error {
	if folder != "INBOX" || id != "42" {
		return errors.New("wrong message")
	}
	f.marked = read
	return nil
}
func (f *mailboxFake) GetEmail(context.Context, string, string) (*imap.Email, error) {
	return &imap.Email{ID: "42", BodyHTML: `<script>bad()</script><p onclick="bad()">Safe</p><a href="javascript:bad()">Bad URL</a><img src="https://tracker.example/1">`}, nil
}

func (f *mailboxFake) GetAttachment(context.Context, string, string, string) (*imap.AttachmentData, error) {
	return &imap.AttachmentData{Filename: "file", Content: f.attachment}, nil
}

type senderFake struct {
	tools.EmailSender
	calls int
}

func (f *senderFake) SendEmail(context.Context, string, []string, string, string, smtp.SendOptions) error {
	f.calls++
	return &smtp.SentCopyError{Err: errors.New("append failed")}
}
func TestMailAPIValidationAndSafeHTML(t *testing.T) {
	fake := &mailboxFake{}
	sender := &senderFake{}
	resolved := 0
	closed := 0
	h := New(func(context.Context) (mailmcp.Dependencies, error) {
		resolved++
		return mailmcp.Dependencies{IMAP: fake, SMTP: sender, FromEmail: "me@icloud.com", Close: func() { closed++ }}, nil
	}, "https://mail.microapps.space", nil, func(context.Context) string { return "alice" }, nil)
	call := func(method, path, body, origin, content string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Mail-Owner", "alice")
		r.Header.Set("X-Mail-Account", "me@icloud.com")
		r.Header.Set("Content-Type", content)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, body, origin, content string
		status                              int
	}{
		{"POST", "/api/mail/mutate", `{"op":"read","folder":"INBOX","id":"42","validity":7,"read":true}`, "", "application/json", 403},
		{"POST", "/api/mail/mutate", `{}`, "https://evil.example", "application/json", 403},
		{"POST", "/api/mail/mutate", `{}`, "https://mail.microapps.space", "text/plain", 403},
		{"POST", "/api/mail/mutate", `{"op":"move","folder":"INBOX","id":"42","target":"bad\r\nfolder"}`, "https://mail.microapps.space", "application/json", 400},
		{"POST", "/api/mail/mutate", `{"op":"read","folder":"INBOX","id":"0"}`, "https://mail.microapps.space", "application/json", 400},
		{"GET", "/api/mail/message?folder=INBOX&id=42junk", "", "", "", 400},
		{"GET", "/api/mail/messages?folder=INBOX&offset=-1", "", "", "", 400},
	} {
		w := call(tc.method, tc.path, tc.body, tc.origin, tc.content)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.status)
		}
	}
	if resolved != 0 {
		t.Fatal("invalid input opened a mailbox")
	}
	w := call("POST", "/api/mail/mutate", `{"op":"read","folder":"INBOX","id":"42","validity":7,"read":true}`, "https://mail.microapps.space", "application/json")
	if w.Code != 204 || !fake.marked || closed != 1 {
		t.Fatalf("read mutation: %d, marked %v closed %d", w.Code, fake.marked, closed)
	}
	fake.marked = false
	w = call("POST", "/api/mail/mutate", `{"op":"read","folder":"INBOX","id":"42","validity":8,"read":true}`, "https://mail.microapps.space", "application/json")
	if w.Code != 409 || fake.marked {
		t.Fatal("stale mailbox identity changed a message")
	}
	w = call("GET", "/api/mail/message?folder=INBOX&id=42&validity=7", "", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "script") || strings.Contains(w.Body.String(), "onclick") || strings.Contains(w.Body.String(), "javascript:") {
		t.Fatalf("unsafe message response: %s", w.Body.String())
	}
	for _, tc := range []struct{ data, kind string }{{"plain text", "text/plain"}, {"<html><script>bad()</script>", "application/octet-stream"}, {"<svg onload=bad()></svg>", "text/plain"}} {
		fake.attachment = []byte(tc.data)
		preview := call("GET", "/api/mail/attachment?folder=INBOX&id=42&validity=7&filename=file&preview=1", "", "", "")
		if preview.Code != 200 || preview.Header().Get("Content-Type") != tc.kind || preview.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("unsafe preview: %d %v", preview.Code, preview.Header())
		}
	}
	w = call("POST", "/api/mail/send", `{"to":["you@example.com"],"subject":"Hello","body":"World"}`, "https://mail.microapps.space", "application/json")
	if w.Code != http.StatusOK || sender.calls != 1 || !strings.Contains(w.Body.String(), "do_not_resend") {
		t.Fatalf("ambiguous send result: %d %s, %d calls", w.Code, w.Body.String(), sender.calls)
	}
}
