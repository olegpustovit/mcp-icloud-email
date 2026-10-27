package smtp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"net/mail"
	"slices"
	"strings"
	"testing"
)

func TestSendEmailRejectsImpersonationAndUnsafeHeaders(t *testing.T) {
	c := NewClient("owner@icloud.com", "secret", nil)
	for _, tc := range []struct {
		name    string
		from    string
		headers map[string]string
	}{
		{name: "impersonation", from: "victim@example.com"},
		{name: "header injection", from: "owner@icloud.com", headers: map[string]string{"References": "<a>\r\nBcc: victim@example.com"}},
		{name: "reserved header", from: "owner@icloud.com", headers: map[string]string{"From": "victim@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := c.SendEmail(context.Background(), tc.from, []string{"to@example.com"}, "subject", "body", SendOptions{Headers: tc.headers})
			if err == nil {
				t.Fatal("expected validation error before network send")
			}
		})
	}
}

func TestReplyAllExcludesOnlyAuthenticatedAddress(t *testing.T) {
	if !isAccountAddress("Owner <owner@icloud.com>", "owner@icloud.com") {
		t.Fatal("expected account address to match")
	}
	if isAccountAddress("owner@icloud.com.attacker.test", "owner@icloud.com") {
		t.Fatal("substring match must not treat another address as the account")
	}
}

func TestSendAndReplyArchiveExactMessage(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(fmt.Sprintf("reply=%t", reply), func(t *testing.T) {
			var submitted, archived []byte
			calls := []string{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := NewClient("owner@icloud.com", "secret", func(ctx context.Context, raw []byte) error {
				calls = append(calls, "archive")
				if ctx.Err() != nil {
					t.Fatal("accepted mail must be filed after caller cancellation")
				}
				archived = bytes.Clone(raw)
				return nil
			})
			c.submit = func(_ context.Context, _ string, recipients []string, raw []byte) error {
				calls = append(calls, "send")
				submitted = bytes.Clone(raw)
				if !slices.Contains(recipients, "hidden@example.com") {
					t.Fatal("missing BCC envelope recipient")
				}
				cancel()
				return nil
			}
			opts := SendOptions{HTML: true, BCC: []string{"hidden@example.com"}}
			var err error
			if reply {
				err = c.ReplyToEmail(ctx, &imap.Email{From: "to@example.com", Subject: "Original", MessageID: "<parent@example.com>", References: []string{"<root@example.com>"}}, "<p>Test body</p>", false, opts)
			} else {
				err = c.SendEmail(ctx, "owner@icloud.com", []string{"to@example.com"}, "Test", "<p>Test body</p>", opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, []string{"send", "archive"}) || !bytes.Equal(submitted, archived) {
				t.Fatal("must send once and archive exactly those bytes")
			}
			msg, err := mail.ReadMessage(bytes.NewReader(archived))
			if err != nil {
				t.Fatal(err)
			}
			if id := msg.Header.Get("Message-ID"); id == "" || strings.Count(id, "@") != 1 {
				t.Fatalf("invalid Message-ID: %q", id)
			}
			if msg.Header.Get("Bcc") != "" {
				t.Fatal("BCC leaked into transmitted message")
			}
			if reply && (msg.Header.Get("In-Reply-To") != "<parent@example.com>" || msg.Header.Get("References") != "<root@example.com> <parent@example.com>") {
				t.Fatal("reply threading lost")
			}
		})
	}
}

func TestSendArchiveFailures(t *testing.T) {
	for _, tc := range []struct {
		name                string
		sendErr, archiveErr error
		wantArchive         int
	}{
		{"SMTP rejected", errors.New("rejected"), nil, 0},
		{"copy failed", nil, errors.New("IMAP unavailable"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sends, archives := 0, 0
			c := NewClient("owner@icloud.com", "secret", func(context.Context, []byte) error { archives++; return tc.archiveErr })
			c.submit = func(context.Context, string, []string, []byte) error { sends++; return tc.sendErr }
			err := c.SendEmail(context.Background(), "owner@icloud.com", []string{"to@example.com"}, "Test", "Body", SendOptions{})
			if sends != 1 || archives != tc.wantArchive {
				t.Fatalf("sends=%d archives=%d", sends, archives)
			}
			var copyErr *SentCopyError
			if tc.archiveErr != nil && (!errors.As(err, &copyErr) || !errors.Is(err, tc.archiveErr)) {
				t.Fatalf("expected sent-copy warning: %v", err)
			}
			if tc.sendErr != nil && (!errors.Is(err, tc.sendErr) || errors.As(err, &copyErr)) {
				t.Fatalf("expected SMTP error: %v", err)
			}
		})
	}
}
