package mailsync

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type replyFailureMail struct{ *moveWire }

func (f *replyFailureMail) GetEmail(context.Context, string, string) (*imap.Email, error) {
	return &imap.Email{ID: "1", UIDValidity: 7, From: "original@example.com", Subject: "Original", MessageID: "<original@example.com>", References: []string{"<ancestor@example.com>"}}, nil
}

type replyCaptureSender struct {
	sender
	to      []string
	subject string
	options smtp.SendOptions
}

func (s *replyCaptureSender) SetAliases([]string) {}
func (s *replyCaptureSender) SendEmail(_ context.Context, _ string, to []string, subject, _ string, options smtp.SendOptions) error {
	s.to = to
	s.subject = subject
	s.options = options
	return errors.New("submission failed")
}
func TestOutboxReplyFailureIsReturnedBeforeDraftCleanup(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	wire := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {"1": {ID: "1", From: "sender@example.com", Date: time.Now()}}}, next: map[string]uint32{"INBOX": 2}}
	connectMoveFake(s, wire)
	sender := &replyCaptureSender{}
	s.dialCommand = func(_ context.Context, address, _ string) (*Connection, error) {
		return &Connection{Mail: &replyFailureMail{wire}, Sender: sender, Address: address}, nil
	}
	job, err := s.Schedule(ctx, "owner", Send{To: []string{"edited@example.com"}, CC: []string{"copy@example.com"}, Subject: "Edited subject", Body: "reply body", ReplyID: "1", ReplyFolder: "INBOX", ReplyValidity: 7, ReplyAll: true, SendAt: time.Now().Add(time.Hour), TransactionID: "failed-reply"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.deliver(ctx, job); err == nil {
		t.Fatal("reply delivery failure was reported as success")
	}
	if !slices.Equal(sender.to, []string{"edited@example.com"}) || !slices.Equal(sender.options.CC, []string{"copy@example.com"}) || sender.subject != "Edited subject" || sender.options.Headers["In-Reply-To"] != "<original@example.com>" || sender.options.Headers["References"] != "<ancestor@example.com> <original@example.com>" {
		t.Fatal("reply changed edited envelope or lost threading", sender)
	}
	stored, err := s.OutboxMessage(ctx, "owner", job.ID)
	if err != nil || stored.Body != "reply body" {
		t.Fatal("failed reply payload was lost", err)
	}
}

func TestUncertainRestoreRetainsReceiptAndRejectsActiveSends(t *testing.T) {
	s := testMirror(t)
	s.cancel()
	s.wg.Wait()
	ctx := context.Background()
	message := Send{To: []string{"to@example.com"}, Subject: "uncertain", Body: "private body", SendAt: time.Now().Add(time.Hour), TransactionID: "original-send"}
	job, err := s.Schedule(ctx, "owner", message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreUncertain(ctx, "owner", job.ID); err == nil {
		t.Fatal("restored an active send")
	}
	if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"status": "uncertain"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreUncertain(ctx, "foreign", job.ID); err == nil {
		t.Fatal("foreign draft restored")
	}
	restored, err := s.RestoreUncertain(ctx, "owner", job.ID)
	if err != nil || restored.Body != message.Body || restored.TransactionID != "" || !restored.SendAt.IsZero() {
		t.Fatal("restore lost content or send identity", restored, err)
	}
	if _, err = s.RestoreUncertain(ctx, "owner", job.ID); err != nil {
		t.Fatal("lost-response retry failed", err)
	}
	if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"payload_expires": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.cleanOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	var stored Outbox
	if err = s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": job.ID}).Decode(&stored); err != nil || len(stored.Payload) != 0 || stored.Status != "restored" {
		t.Fatal("cleanup deleted receipt or retained payload", stored, err)
	}
	receipt, err := s.Schedule(ctx, "owner", message)
	if err != nil || receipt.ID != job.ID || receipt.Status != "restored" {
		t.Fatal("old transaction became a duplicate send", receipt, err)
	}
}
func TestRenameInspectorCancellationAndOwnerScope(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {}, "Clients": {}, "Conflict": {}}, next: map[string]uint32{"INBOX": 1, "Clients": 1, "Conflict": 1}}
	connectMoveFake(s, f)
	if _, err := s.Start(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	op := folderOperation{ID: "rename-conflict", Owner: "owner", Account: "mirror@icloud.com", Actor: "web", Transaction: "conflict", FolderRename: FolderRename{From: "Clients", To: "Conflict", Delimiter: "/"}, Validity: 7, State: "pending", NextAttempt: time.Now().Add(time.Hour), Created: time.Now()}
	if _, err := s.db.Collection("mail_folder_ops").InsertOne(ctx, op); err != nil {
		t.Fatal(err)
	}
	operations, err := s.RenameOperations(ctx, "owner")
	if err != nil || len(operations) != 1 || operations[0].ReferencesUpdated {
		t.Fatal(operations, err)
	}
	foreign, err := s.RenameOperations(ctx, "foreign")
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign operation visible", err)
	}
	if err = s.CancelRename(ctx, "foreign", op.ID); err == nil {
		t.Fatal("foreign operation cancelled")
	}
	if err = s.CancelRename(ctx, "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.checkRenamePending(ctx, "owner", op.Account); err != nil {
		t.Fatal("cancelled operation still pauses work", err)
	}
	op.ID = "already-applied"
	op.ReferencesUpdated = true
	if _, err = s.db.Collection("mail_folder_ops").InsertOne(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelRename(ctx, "owner", op.ID); err == nil {
		t.Fatal("cancelled partially rewritten references")
	}
	if _, err = s.db.Collection("mail_folder_ops").UpdateOne(ctx, bson.M{"_id": op.ID}, bson.M{"$set": bson.M{"references_updated": false}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	delete(f.mail, "Clients")
	f.mu.Unlock()
	if err = s.CancelRename(ctx, "owner", op.ID); err == nil {
		t.Fatal("cancelled an IMAP rename after source vanished")
	}
}
