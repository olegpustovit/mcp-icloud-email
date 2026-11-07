package imap

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	imapv1 "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestRecoverableMoveWithoutMessageID(t *testing.T) {
	for _, mode := range []string{"MOVE", "UIDPLUS", "copy-response", "copy-disconnect", "expunge-before", "expunge-response", "unsupported"} {
		t.Run(mode, func(t *testing.T) { testRecoverableMove(t, mode) })
	}
}

type interruptedMoveSession struct {
	imapserver.Session
	failure string
	conn    net.Conn
}

func (s *interruptedMoveSession) Search(kind imapserver.NumKind, criteria *imapv2.SearchCriteria, options *imapv2.SearchOptions) (*imapv2.SearchData, error) {
	// Match iCloud: custom keywords are retained but SEARCH KEYWORD misses them.
	for _, flag := range criteria.Flag {
		if strings.HasPrefix(strings.ToLower(string(flag)), "$terminalmail_") {
			return &imapv2.SearchData{All: imapv2.UIDSet{}}, nil
		}
	}
	return s.Session.Search(kind, criteria, options)
}

func (s *interruptedMoveSession) Copy(set imapv2.NumSet, to string) (*imapv2.CopyData, error) {
	data, err := s.Session.Copy(set, to)
	if err == nil && s.failure == "copy-disconnect" {
		s.failure = ""
		_ = s.conn.Close()
	}
	if err == nil && s.failure == "copy-response" {
		s.failure = ""
		return nil, &imapv2.Error{Type: imapv2.StatusResponseTypeNo, Text: "COPY response lost"}
	}
	return data, err
}

func (s *interruptedMoveSession) Expunge(w *imapserver.ExpungeWriter, uids *imapv2.UIDSet) error {
	if s.failure == "expunge-before" {
		s.failure = ""
		return &imapv2.Error{Type: imapv2.StatusResponseTypeNo, Text: "EXPUNGE interrupted"}
	}
	err := s.Session.Expunge(w, uids)
	if err == nil && s.failure == "expunge-response" {
		s.failure = ""
		return &imapv2.Error{Type: imapv2.StatusResponseTypeNo, Text: "EXPUNGE response lost"}
	}
	return err
}

func testRecoverableMove(t *testing.T, mode string) {
	backend := imapmemserver.New()
	user := imapmemserver.NewUser("test", "password")
	for _, name := range []string{"INBOX", "Archive", "Snoozed", "Deleted Messages"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	backend.AddUser(user)
	caps := imapv2.CapSet{imapv2.CapIMAP4rev1: {}}
	if mode != "unsupported" {
		caps[imapv2.CapUIDPlus] = struct{}{}
	}
	if mode == "MOVE" {
		caps[imapv2.CapMove] = struct{}{}
	}
	var interrupted atomic.Bool
	server := imapserver.New(&imapserver.Options{NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		session := backend.NewSession()
		if mode != "MOVE" {
			failure := mode
			if interrupted.Swap(true) {
				failure = ""
			}
			session = &interruptedMoveSession{Session: session, failure: failure, conn: conn.NetConn()}
		}
		return session, nil, nil
	}, InsecureAuth: true, Caps: caps})
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
	if _, err = user.Append("INBOX", strings.NewReader("Subject: Unrelated deleted mail\r\n\r\nKeep me"), &imapv2.AppendOptions{Flags: []imapv2.Flag{imapv2.FlagDeleted}}); err != nil {
		t.Fatal(err)
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
	if mode == "unsupported" {
		if !errors.Is(err, ErrMarkerUnsupported) {
			t.Fatalf("unsafe fallback accepted: %v", err)
		}
		if count, err := c.CountEmails(ctx, "INBOX", EmailFilters{}); err != nil || count != 2 {
			t.Fatal("unsupported move modified source", count, err)
		}
		if count, err := c.CountEmails(ctx, "Archive", EmailFilters{}); err != nil || count != 2 {
			t.Fatal("unsupported move copied mail", count, err)
		}
		return
	}
	if strings.Contains(mode, "-") {
		if err == nil {
			t.Fatal("interruption was not simulated")
		}
		if mode == "copy-disconnect" {
			wire, err = client.Dial(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer wire.Close()
			if err = wire.Login("test", "password"); err != nil {
				t.Fatal(err)
			}
			c.client = wire
		}
		location, err = c.Relocate(ctx, from, "Archive", marker)
	}
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
	if count, err := c.CountEmails(ctx, "INBOX", EmailFilters{}); err != nil || count != 1 {
		t.Fatal("source retained a copy or unrelated deleted mail was expunged", count, err)
	}
	if same, err := c.Relocate(ctx, location, "Archive", marker+"same"); err != nil || same != location {
		t.Fatal("same-folder snooze failed", same, err)
	}
	if same, err := c.Relocate(ctx, location, "Archive", marker+"same"); err != nil || same != location {
		t.Fatal("same-folder retry removed mail", same, err)
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
	for _, mark := range []string{marker, marker + "r", marker + "same"} {
		if err = c.ClearMoveMarker(ctx, back, mark); err != nil {
			t.Fatal(err)
		}
	}
	set := new(imapv1.SeqSet)
	set.AddRange(1, 0)
	uids, err := c.markerUIDs(set, marker)
	if err != nil || len(uids) != 0 {
		t.Fatal("move markers were retained", err)
	}
	if err = c.MoveEmail(ctx, "INBOX", "Archive", back.ID); err != nil {
		t.Fatal("direct move failed", err)
	}
	if err = c.DeleteEmail(ctx, "Archive", "4", false); err != nil {
		t.Fatal("move to trash failed", err)
	}
	trashed, err := c.GetEmail(ctx, "Deleted Messages", "1")
	if err != nil || trashed.Unread {
		t.Fatal("trash lost the message or read state", err)
	}
	for _, flag := range trashed.Flags {
		if strings.HasPrefix(strings.ToLower(flag), "$terminalmail_") {
			t.Fatal("direct move retained its marker", flag)
		}
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
