package imap

import (
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

func TestSentFolder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		folders []*imap.MailboxInfo
		want    string
	}{
		{"special-use first", []*imap.MailboxInfo{{Name: "Sent Messages"}, {Name: "Gesendet", Attributes: []string{`\Sent`}}}, "Gesendet"},
		{"iCloud fallback", []*imap.MailboxInfo{{Name: "Sent"}, {Name: "Sent Messages"}}, "Sent Messages"},
		{"no selectable sent", []*imap.MailboxInfo{{Name: "Sent", Attributes: []string{`\Sent`, `\Noselect`}}}, ""},
		{"missing", []*imap.MailboxInfo{{Name: "INBOX"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sentFolder(tc.folders)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestSaveSentAppendsSeenAndPreservesRawMessage(t *testing.T) {
	be := memory.New()
	srv := server.New(be)
	srv.AllowInsecureAuth = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	defer func() { _ = srv.Close(); <-done }()
	wire, err := imapclient.Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	wire.Timeout = 5 * time.Second
	c := &Client{client: wire}
	defer c.Close()
	if err := wire.Login("username", "password"); err != nil {
		t.Fatal(err)
	}
	if err := wire.Create("Sent Messages"); err != nil {
		t.Fatal(err)
	}
	raw := "From: owner@icloud.com\r\nTo: to@example.com\r\nMessage-ID: <sent@example.com>\r\nIn-Reply-To: <parent@example.com>\r\nReferences: <root@example.com> <parent@example.com>\r\nSubject: Sent test\r\n\r\nExact body\r\n"
	if err := c.SaveSent(context.Background(), []byte(raw)); err != nil {
		t.Fatal(err)
	}
	mailbox, err := wire.Select("Sent Messages", true)
	if err != nil || mailbox.Messages != 1 {
		t.Fatalf("mailbox=%+v err=%v", mailbox, err)
	}
	ids := new(imap.SeqSet)
	ids.AddNum(1)
	section := &imap.BodySectionName{Peek: true}
	messages := make(chan *imap.Message, 1)
	fetched := make(chan error, 1)
	go func() { fetched <- wire.Fetch(ids, []imap.FetchItem{imap.FetchFlags, section.FetchItem()}, messages) }()
	for msg := range messages {
		body, err := io.ReadAll(msg.GetBody(section))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != raw {
			t.Fatal("Sent message bytes changed")
		}
		if !slices.Contains(msg.Flags, imap.SeenFlag) || slices.Contains(msg.Flags, imap.DraftFlag) {
			t.Fatalf("flags=%v", msg.Flags)
		}
	}
	if err := <-fetched; err != nil {
		t.Fatal(err)
	}
	if err := wire.Delete("Sent Messages"); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveSent(context.Background(), []byte(raw)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing Sent mailbox: %v", err)
	}
}
