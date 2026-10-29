package imap

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"
	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type heldSearch struct {
	imapserver.Session
	started, release chan struct{}
	once             *sync.Once
}

func (s *heldSearch) Search(kind imapserver.NumKind, criteria *imapv2.SearchCriteria, options *imapv2.SearchOptions) (*imapv2.SearchData, error) {
	if len(criteria.Text) > 0 && criteria.Text[0] == "wait" {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return s.Session.Search(kind, criteria, options)
}
func TestAdvancedSearchUsesServerBodiesWithoutReadingThemAndCancels(t *testing.T) {
	backend := imapmemserver.New()
	user := imapmemserver.NewUser("test", "password")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	backend.AddUser(user)
	messages := []string{
		"From: Mira <mira@example.com>\r\nTo: me@example.com\r\nDate: Wed, 28 Oct 2026 11:00:00 +0000\r\nSubject: Review\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=mail\r\n\r\n--mail\r\nContent-Type: text/plain\r\n\r\nneedle hidden only here\r\n--mail\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=notes.pdf\r\n\r\nattachment\r\n--mail--\r\n",
		"From: Mira <mira@example.com>\r\nTo: me@example.com\r\nDate: Wed, 28 Oct 2026 11:00:00 +0000\r\nSubject: Other\r\n\r\nneedle elsewhere",
	}
	for _, raw := range messages {
		if _, err := user.Append("INBOX", strings.NewReader(raw), &imapv2.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &heldSearch{Session: backend.NewSession(), started: started, release: release, once: &once}, nil, nil
	}, InsecureAuth: true, Caps: imapv2.CapSet{imapv2.CapIMAP4rev1: {}}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	wire, err := client.Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Terminate()
	if err = wire.Login("test", "password"); err != nil {
		t.Fatal(err)
	}
	c := &Client{client: wire}
	rows, total, err := c.SearchEmails(context.Background(), "INBOX", `from:mira has:attachment "needle hidden" after:2026-10-27 before:2026-10-29 is:unread`, EmailFilters{Advanced: true, Limit: 200})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Subject != "Review" || rows[0].BodyPlain != "" || rows[0].BodyHTML != "" || !rows[0].Unread || rows[0].UIDValidity == 0 || len(rows[0].Attachments) != 1 {
		t.Fatalf("search=%+v total=%d err=%v", rows, total, err)
	}
	if count, err := c.CountEmails(context.Background(), "INBOX", EmailFilters{UnreadOnly: true}); err != nil || count != 2 {
		t.Fatal("search marked mail read", count, err)
	}
	first, err := c.SearchPage(context.Background(), "INBOX", "needle", EmailFilters{Advanced: true, Limit: 1})
	if err != nil || len(first.Messages) != 1 || first.Messages[0].ID != "2" || first.NextUID != 2 {
		t.Fatal("invalid first UID page", first, err)
	}
	second, err := c.SearchPage(context.Background(), "INBOX", "needle", EmailFilters{Advanced: true, Limit: 1, BeforeUID: first.NextUID, ExpectedValidity: first.UIDValidity})
	if err != nil || len(second.Messages) != 1 || second.Messages[0].ID != "1" || second.NextUID != 0 {
		t.Fatal("invalid second UID page", second, err)
	}
	if _, err = c.SearchPage(context.Background(), "INBOX", "needle", EmailFilters{Advanced: true, Limit: 1, BeforeUID: first.NextUID, ExpectedValidity: first.UIDValidity + 1}); !errors.Is(err, ErrLocationChanged) {
		t.Fatal("stale UID search cursor accepted", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, _, err := c.SearchEmails(ctx, "INBOX", "wait", EmailFilters{Advanced: true}); result <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled search succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled search retained the command connection")
	}
	releaseOnce.Do(func() { close(release) })
}
func TestSearchQueryOperatorsAndLocalizedRoles(t *testing.T) {
	q, err := ParseQuery(`in:sent from:mira is:UNREAD has:attachment after:2026-10-27`)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IncludesFolder("Envoyés", "sent") || q.IncludesFolder("INBOX", "inbox") {
		t.Fatal("localized role did not scope search")
	}
	mail := Email{From: "Mira <mira@example.com>", Unread: true, Date: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Attachments: []Attachment{{Filename: "file.pdf"}}}
	if !q.MatchesMetadata(mail) {
		t.Fatal("matching metadata rejected")
	}
	mail.Unread = false
	if q.MatchesMetadata(mail) {
		t.Fatal("read filter ignored")
	}
	for _, raw := range []string{"after:yesterday", "is:unknown", "has:banana", "bad\x00query"} {
		if _, err = ParseQuery(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{}
	if _, _, err = c.SearchEmails(ctx, "INBOX", "needle", EmailFilters{Advanced: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled query opened a connection", err)
	}
}
