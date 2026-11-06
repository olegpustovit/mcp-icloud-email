package mailsync

import (
	"context"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"testing"
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
