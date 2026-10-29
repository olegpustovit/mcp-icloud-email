package mailsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPreferenceValidation(t *testing.T) {
	p := defaultPreferences()
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	p.UndoSeconds = 11
	if err := p.validate(); err == nil {
		t.Fatal("accepted unknown undo duration")
	}
	p = defaultPreferences()
	p.Identities = []Identity{{Address: "bad address"}}
	if err := p.validate(); err == nil {
		t.Fatal("accepted invalid identity")
	}
	p = defaultPreferences()
	p.Rules = []Rule{{ID: "1", Field: "from", Match: "regex", Value: ".*", Action: "move"}}
	if err := p.validate(); err == nil {
		t.Fatal("accepted unsupported rule")
	}
}
func TestSendValidation(t *testing.T) {
	for _, message := range []Send{{}, {To: []string{"bad"}}, {To: []string{"a@example.com"}, Subject: "hi\r\nBcc: evil@example.com"}, {To: []string{"a@example.com"}, SendAt: time.Now().AddDate(2, 0, 0)}} {
		if err := validateSend(message); err == nil {
			t.Fatalf("accepted invalid send %+v", message)
		}
	}
	for _, m := range []Send{{ReplyID: "42", ReplyFolder: "INBOX"}, {DraftID: "42", DraftFolder: "Drafts"}, {ReplyID: "42bad", ReplyFolder: "INBOX", ReplyValidity: 7}} {
		if validateSend(m) == nil {
			t.Fatal("accepted unbound or invalid source UID")
		}
	}
}
func TestOutboxRemainsBoundToLinkedAccount(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	owner := "owner"
	message := Send{To: []string{"to@example.com"}, Subject: "test", Body: "private draft", SendAt: time.Now().Add(time.Hour), TransactionID: "same-transaction"}
	job, err := s.Schedule(ctx, owner, message)
	if err != nil {
		t.Fatal(err)
	}
	s.credentials = func(context.Context, string) (string, string, error) {
		return "replacement@icloud.com", "password", nil
	}
	if jobs, err := s.Outbox(ctx, owner); err != nil || len(jobs) != 0 {
		t.Fatal("old account outbox appeared under replacement account")
	}
	if _, err = s.OutboxMessage(ctx, owner, job.ID); err == nil {
		t.Fatal("old account draft restored under replacement account")
	}
	if err = s.CancelSend(ctx, owner, job.ID); err == nil {
		t.Fatal("old account send changed through replacement account")
	}
	if _, err = s.Schedule(ctx, owner, message); err == nil {
		t.Fatal("old transaction reused for replacement account")
	}
}
func TestPersistentOutboxAndOwnerIsolation(t *testing.T) {
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	ctx := context.Background()
	s, err := New(ctx, uri, base64.StdEncoding.EncodeToString(make([]byte, 32)), func(context.Context, string) (string, string, error) {
		return "test@icloud.com", "password", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := "outbox-test-" + time.Now().Format("150405.000000000")
	defer func() {
		for _, collection := range []string{"mail_outbox", "mail_preferences"} {
			_, _ = s.db.Collection(collection).DeleteMany(ctx, bson.M{"$or": []bson.M{{"owner": owner}, {"_id": owner}}})
		}
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	message := Send{To: []string{"you@example.com"}, Subject: "Hello", Body: "private-body-never-plain", SendAt: time.Now().Add(time.Hour), TransactionID: "same-transaction"}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := s.Schedule(ctx, owner, message); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	jobs, err := s.Outbox(ctx, owner)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if bytes.Contains(jobs[0].Payload, []byte(message.Body)) {
		t.Fatal("outbox body stored as plaintext")
	}
	if err := s.CancelSend(ctx, "different-owner", jobs[0].ID); err == nil {
		t.Fatal("cross-owner cancellation succeeded")
	}
	if other, err := s.Outbox(ctx, "different-owner"); err != nil || len(other) != 0 {
		t.Fatal("cross-owner outbox visible")
	}
	if err := s.CancelSend(ctx, owner, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Outbox(ctx, owner)
	if err != nil || len(jobs) != 0 {
		t.Fatal("cancelled send still pending")
	}
	prefs := defaultPreferences()
	prefs.Theme = "dark"
	if err := s.SavePreferences(ctx, owner, prefs); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Preferences(ctx, owner)
	if err != nil || stored.Theme != "dark" {
		t.Fatal("preferences did not persist")
	}
	other, err := s.Preferences(ctx, "different-owner")
	if err != nil || other.Theme != "system" {
		t.Fatal("preferences crossed owners")
	}
}
