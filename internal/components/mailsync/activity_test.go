package mailsync

import (
	"context"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"testing"
	"time"
)

func TestConcurrentArrivalIsNotAttributedToAgentAndSentAttributionPersists(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	mail := Metadata{Key: "arrival", Owner: "owner", Account: "mirror@icloud.com", Email: imap.Email{ID: "1", Folder: "INBOX", UIDValidity: 7}}
	sent := mail
	sent.Key = "sent"
	sent.Folder = "Sent Messages"
	if err := s.commit(ctx, "owner", mail.Account, "mcp:agent", "agent-tx", []Change{{Entity: "msg", Op: "put", Key: mail.Key, Message: &mail, Arrival: true}, {Entity: "msg", Op: "put", Key: sent.Key, Message: &sent}}); err != nil {
		t.Fatal(err)
	}
	changes, err := s.Changes(ctx, "owner", mail.Account, 0)
	if err != nil || len(changes) != 2 || changes[0].Actor != "imap" || changes[0].Message.Actor != "imap" || changes[0].TransactionID != "" || changes[1].Message.SentBy != "mcp:agent" {
		t.Fatalf("attribution: %+v %v", changes, err)
	}
	sent = *changes[1].Message
	sent.Unread = true
	if err := s.commit(ctx, "owner", mail.Account, "web", "", []Change{{Entity: "msg", Op: "put", Key: sent.Key, Message: &sent}}); err != nil {
		t.Fatal(err)
	}
	changes, err = s.Changes(ctx, "owner", mail.Account, changes[1].Sequence)
	if err != nil || len(changes) != 1 || changes[0].Message.SentBy != "mcp:agent" {
		t.Fatal("subsequent read erased sender attribution", changes, err)
	}
}

func TestPollingRetainsIdleInboxAndSlowsInactiveFolders(t *testing.T) {
	s := &Service{owners: map[string]*ownerState{}}
	now := time.Now()
	last := now.Add(-time.Minute)
	if s.pollAll("owner", last, now) {
		t.Fatal("inactive mailbox uses active cadence")
	}
	if !s.pollAll("owner", now.Add(-10*time.Minute), now) {
		t.Fatal("inactive mailbox never polled")
	}
	done := s.Watch("owner")
	if !s.pollAll("owner", last, now) {
		t.Fatal("SSE viewer did not activate polling")
	}
	done()
	done()
	if !s.pollAll("owner", last, now) {
		t.Fatal("recent disconnect lost active grace period")
	}
	s.mu.Lock()
	s.owners["owner"].active = now.Add(-3 * time.Minute)
	s.mu.Unlock()
	if s.pollAll("owner", last, now) {
		t.Fatal("disconnected viewer retained fast polling")
	}
	s.touch("owner")
	if !s.pollAll("owner", last, now) {
		t.Fatal("MCP call did not activate polling")
	}
}
