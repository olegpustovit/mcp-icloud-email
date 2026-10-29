package imap

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	imapv1 "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestRecoverableMoveWithoutMessageID(t *testing.T) {
	backend := imapmemserver.New()
	user := imapmemserver.NewUser("test", "password")
	for _, name := range []string{"INBOX", "Archive", "Snoozed"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	backend.AddUser(user)
	server := imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return backend.NewSession(), nil, nil
	}, InsecureAuth: true, Caps: imapv2.CapSet{imapv2.CapIMAP4rev1: {}, imapv2.CapMove: {}, imapv2.CapUIDPlus: {}}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	for _, folder := range []string{"INBOX", "Archive", "Archive"} {
		if _, err = user.Append(folder, strings.NewReader("From: sender@example.com\r\nSubject: Duplicate subject, no Message-ID\r\n\r\nBody"), &imapv2.AppendOptions{Flags: []imapv2.Flag{imapv2.FlagSeen}}); err != nil {
			t.Fatal(err)
		}
	}
	wire, err := client.Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	if err = wire.Login("test", "password"); err != nil {
		t.Fatal(err)
	}
	c := &Client{client: wire, username: "test"}
	ctx := context.Background()
	validity, err := c.UIDValidity(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	from := Location{Folder: "INBOX", ID: "1", Validity: validity}
	marker := "$TerminalMail_unique_test"
	location, err := c.Relocate(ctx, from, "Archive", marker)
	if err != nil || location.ID != "3" {
		t.Fatalf("move=%+v err=%v", location, err)
	}
	recovered, err := c.Relocate(ctx, from, "Archive", marker)
	if err != nil || recovered != location {
		t.Fatal("lost-response retry moved twice", err)
	}
	if count, err := c.CountEmails(ctx, "Archive", EmailFilters{}); err != nil || count != 3 {
		t.Fatal("retry duplicated mail", count, err)
	}
	stale := location
	stale.Validity++
	if _, err = c.Relocate(ctx, stale, "Snoozed", marker+"stale"); !errors.Is(err, ErrLocationChanged) {
		t.Fatal("stale mailbox generation moved mail", err)
	}
	back, err := c.Relocate(ctx, location, "INBOX", marker+"r")
	if err != nil {
		t.Fatal(err)
	}
	email, err := c.GetEmail(ctx, "INBOX", back.ID)
	if err != nil || email.Unread {
		t.Fatal("MOVE changed read state", err)
	}
	if err = c.ClearMoveMarker(ctx, back, marker); err != nil {
		t.Fatal(err)
	}
	if err = c.ClearMoveMarker(ctx, back, marker+"r"); err != nil {
		t.Fatal(err)
	}
	criteria := imapv1.NewSearchCriteria()
	criteria.WithFlags = []string{marker}
	uids, err := wire.UidSearch(criteria)
	if err != nil || len(uids) != 0 {
		t.Fatal("move markers were retained", err)
	}
}
func TestMoveMarkerRequiresPermanentKeywordSupport(t *testing.T) {
	for _, test := range []struct {
		flags   []string
		allowed bool
	}{{nil, true}, {[]string{`\Seen`, `\*`}, true}, {[]string{"$terminalmail_test"}, true}, {[]string{`\Seen`, `\Flagged`}, false}} {
		if got := supportsMoveMarker(test.flags, "$TerminalMail_test"); got != test.allowed {
			t.Fatalf("flags=%v allowed=%v", test.flags, got)
		}
	}
}
