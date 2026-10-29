package mailsync

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestRenameLostResponseRestartAndReferences(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {}, "Clients": {}, "Clients/Child": {}, "ClientsOther": {}}, next: map[string]uint32{"INBOX": 1, "Clients": 1, "Clients/Child": 1, "ClientsOther": 1}}
	connectMoveFake(s, f)
	box, err := s.Mailbox(ctx, "owner", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WaitReady(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	prefs := defaultPreferences()
	prefs.Rules = []Rule{{ID: "rule", Field: "from", Match: "contains", Value: "example.com", Action: "move", Target: "Clients/Child", Enabled: true}}
	if err = s.SavePreferences(ctx, "owner", prefs); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePreferences(ctx, "other-owner", prefs); err != nil {
		t.Fatal(err)
	}
	send := Send{To: []string{"recipient@example.com"}, Subject: "Scheduled", Body: "Hello", ReplyFolder: "Clients/Child", ReplyID: "1", ReplyValidity: 7, DraftFolder: "Clients", DraftID: "2", DraftValidity: 7, SendAt: time.Now().Add(time.Hour), TransactionID: "scheduled"}
	job, err := s.Schedule(ctx, "owner", send)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Schedule(ctx, "other-owner", send)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.lostRenameResponse = true
	f.mu.Unlock()
	renameCtx := Transaction(ctx, "rename-test")
	if err = box.RenameFolder(renameCtx, "Clients", "Work"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected lost response: %v", err)
	}
	if err = s.checkRenamePending(ctx, "owner", box.account); !errors.Is(err, ErrRenamePending) {
		t.Fatalf("missing durable plan: %v", err)
	}
	retryReceipt, retryErr := s.Schedule(ctx, "owner", send)
	if retryErr != nil || retryReceipt.ID != job.ID {
		t.Fatal("rename recovery lost an existing send receipt", retryErr)
	}
	lateRetry := send
	lateRetry.SendAt = time.Now().Add(-time.Hour)
	retryReceipt, retryErr = s.Schedule(ctx, "owner", lateRetry)
	if retryErr != nil || retryReceipt.ID != job.ID {
		t.Fatal("an overdue retry lost the original send receipt", retryErr)
	}
	if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"send_at": time.Now().Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.deliverNext(ctx); err != nil {
		t.Fatal(err)
	}
	var paused Outbox
	if err = s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": job.ID}).Decode(&paused); err != nil || paused.Status != "pending" || !paused.RetryAt.After(time.Now()) || len(paused.Payload) == 0 {
		t.Fatal("paused delivery was not retained", err, paused.Status)
	}
	if err = s.deliverNext(ctx); err != nil {
		t.Fatal("paused owner blocked the delivery worker", err)
	}
	if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"send_at": time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	// A job claimed before rename recovery must stop before trying SMTP, retaining its payload.
	if err = s.deliver(ctx, job); !errors.Is(err, ErrRenamePending) {
		t.Fatalf("delivery did not wait for recovery: %v", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := newService(ctx, os.Getenv("MAIL_TEST_MONGODB_URI"), base64.StdEncoding.EncodeToString(make([]byte, 32)), s.credentials, s.db.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resumed.Close(context.Background()) })
	connectMoveFake(resumed, f)
	if err = resumed.runRename(ctx); err != nil {
		t.Fatal(err)
	}
	if err = resumed.checkRenamePending(ctx, "owner", box.account); err != nil {
		t.Fatal(err)
	}
	if f.renames != 1 {
		t.Fatalf("rename repeated %d times", f.renames)
	}
	got, err := resumed.OutboxMessage(ctx, "owner", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyFolder != "Work/Child" || got.DraftFolder != "Work" || got.ReplyValidity != 7 {
		t.Fatalf("references orphaned: %+v", got)
	}
	send.TransactionID = "scheduled-after-rename"
	late, lateErr := resumed.Schedule(ctx, "owner", send)
	if lateErr != nil {
		t.Fatal(lateErr)
	}
	lateMessage, lateErr := resumed.OutboxMessage(ctx, "owner", late.ID)
	if lateErr != nil || lateMessage.ReplyFolder != "Work/Child" || lateMessage.DraftFolder != "Work" {
		t.Fatal("late queued send retained stale folder paths", lateErr, lateMessage)
	}
	got, err = resumed.OutboxMessage(ctx, "other-owner", other.ID)
	if err != nil || got.ReplyFolder != "Clients/Child" {
		t.Fatalf("foreign outbox changed: %+v %v", got, err)
	}
	prefs, err = resumed.Preferences(ctx, "owner")
	if err != nil || prefs.Rules[0].Target != "Work/Child" {
		t.Fatal("rule orphaned", err, prefs)
	}
	if err = resumed.PatchPreferences(ctx, "owner", []byte(`{"theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	prefs, _ = resumed.Preferences(ctx, "owner")
	if prefs.Theme != "dark" || prefs.Rules[0].Target != "Work/Child" {
		t.Fatal("partial preference update lost renamed rule")
	}
	prefs, _ = resumed.Preferences(ctx, "other-owner")
	if prefs.Rules[0].Target != "Clients/Child" {
		t.Fatal("foreign rules changed")
	}
	var op folderOperation
	if err = resumed.db.Collection("mail_folder_ops").FindOne(ctx, bson.M{"owner": "owner"}).Decode(&op); err != nil || op.State != "done" {
		t.Fatal("rename not finished", err)
	}
	c, release, err := resumed.Acquire(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	op.State = "pending"
	op.Validity = 99
	if err = resumed.applyRename(ctx, c, op); !errors.Is(err, imap.ErrLocationChanged) {
		t.Fatalf("accepted a different mailbox identity: %v", err)
	}
	op.Validity = 7
	op.From = "ClientsOther" // both names exist: never infer which external rename happened.
	if err = resumed.applyRename(ctx, c, op); !errors.Is(err, imap.ErrLocationChanged) {
		t.Fatalf("accepted conflicting mailbox names: %v", err)
	}
	changes, err := resumed.Changes(ctx, "owner", box.account, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		if change.Entity == "rename" && change.Rename != nil && change.Rename.From == "Clients" && change.Rename.To == "Work" {
			found = true
		}
	}
	if !found {
		t.Fatal("replicas were not told about the rename")
	}
}
