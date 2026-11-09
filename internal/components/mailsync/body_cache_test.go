package mailsync

import (
	"context"
	"testing"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCachedEmailIsScopedToOwnerAccountFolderAndGeneration(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	row := Metadata{Key: messageKey("alice", "mail@icloud.com", "INBOX", 7, "42"), Owner: "alice", Account: "mail@icloud.com", BodyCached: true, Email: imap.Email{ID: "42", Folder: "INBOX", UIDValidity: 7, BodyPlain: "private"}}
	if _, err := s.db.Collection("mail_messages").InsertOne(ctx, row); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		owner, account, folder string
		validity               uint32
		hit                    bool
	}{
		{"alice", "mail@icloud.com", "INBOX", 7, true},
		{"bob", "mail@icloud.com", "INBOX", 7, false},
		{"alice", "other@icloud.com", "INBOX", 7, false},
		{"alice", "mail@icloud.com", "Archive", 7, false},
		{"alice", "mail@icloud.com", "INBOX", 8, false},
	} {
		mailbox := &Mailbox{s: s, owner: tc.owner, account: tc.account}
		email, err := mailbox.CachedEmail(ctx, tc.folder, "42", tc.validity)
		if err != nil || (email != nil) != tc.hit {
			t.Fatalf("%+v: email=%v err=%v", tc, email, err)
		}
	}
	changed := row
	changed.BodyCached = false
	changed.BodyPlain = ""
	changed.Unread = true
	changed.Subject = "$literal subject"
	if err := s.commit(ctx, row.Owner, row.Account, "imap", "", []Change{{Entity: "msg", Op: "put", Key: row.Key, Message: &changed}}); err != nil {
		t.Fatal(err)
	}
	mailbox := &Mailbox{s: s, owner: row.Owner, account: row.Account}
	if email, err := mailbox.CachedEmail(ctx, "INBOX", "42", 7); err != nil || email == nil || email.BodyPlain != "private" || !email.Unread || email.Subject != changed.Subject {
		t.Fatalf("metadata sync discarded cached content or flags: %v %v", email, err)
	}
	journal, err := s.Changes(ctx, row.Owner, row.Account, 0)
	if err != nil || len(journal) != 1 || journal[0].Message.BodyPlain != "" {
		t.Fatalf("cached body leaked into the metadata journal: %v %v", journal, err)
	}
	if _, err := s.db.Collection("mail_messages").UpdateOne(ctx, bson.M{"_id": row.Key}, bson.M{"$set": bson.M{"body_cached": false}}); err != nil {
		t.Fatal(err)
	}
	if email, err := mailbox.CachedEmail(ctx, "INBOX", "42", 7); err != nil || email != nil {
		t.Fatalf("uncached body was returned: %v %v", email, err)
	}
}
