package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmessage"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Send struct {
	To            []string                 `json:"to"`
	CC            []string                 `json:"cc"`
	BCC           []string                 `json:"bcc"`
	Subject       string                   `json:"subject"`
	Body          string                   `json:"body"`
	ReplyFolder   string                   `json:"replyFolder"`
	ReplyID       string                   `json:"replyId"`
	ReplyAll      bool                     `json:"replyAll"`
	SendAt        time.Time                `json:"sendAt"`
	TransactionID string                   `json:"transactionId"`
	DraftID       string                   `json:"draftId"`
	DraftFolder   string                   `json:"draftFolder"`
	DraftValidity uint32                   `json:"draftValidity"`
	ReplyValidity uint32                   `json:"replyValidity"`
	From          string                   `json:"from"`
	HTML          bool                     `json:"html"`
	Automated     bool                     `json:"automated,omitempty"`
	Attachments   []mailmessage.Attachment `json:"attachments"`
}
type Outbox struct {
	ID             string     `bson:"_id" json:"id"`
	Owner          string     `bson:"owner" json:"-"`
	Account        string     `bson:"account" json:"-"`
	Payload        []byte     `bson:"payload" json:"-"`
	SendAt         time.Time  `bson:"send_at" json:"sendAt"`
	Created        time.Time  `bson:"created" json:"created"`
	Status         string     `bson:"status" json:"status"`
	RetryAt        time.Time  `bson:"retry_at,omitempty" json:"-"`
	Error          string     `bson:"error,omitempty" json:"error,omitempty"`
	Message        *Send      `bson:"-" json:"message,omitempty"`
	Actor          string     `bson:"actor,omitempty" json:"actor,omitempty"`
	PayloadExpires *time.Time `bson:"payload_expires,omitempty" json:"-"`
}

func validateSend(m Send) error {
	if err := mailmessage.ValidateAttachments(m.Attachments); err != nil {
		return err
	}
	for _, source := range []struct {
		folder, id string
		validity   uint32
	}{{m.ReplyFolder, m.ReplyID, m.ReplyValidity}, {m.DraftFolder, m.DraftID, m.DraftValidity}} {
		if source.id == "" {
			continue
		}
		uid, err := strconv.ParseUint(source.id, 10, 32)
		if err != nil || uid == 0 || source.validity == 0 || source.folder == "" || len(source.folder) > 1000 || strings.ContainsAny(source.folder, "\r\n\x00") {
			return errors.New("invalid source mailbox identity")
		}
	}
	if len(m.To)+len(m.CC)+len(m.BCC) == 0 && m.ReplyID == "" {
		return errors.New("at least one recipient is required")
	}
	if len(m.To)+len(m.CC)+len(m.BCC) > 100 || len(m.Subject) > 1000 || len(m.Body) > 1<<20 || strings.ContainsAny(m.Subject, "\r\n") {
		return errors.New("invalid message size or subject")
	}
	for _, list := range [][]string{m.To, m.CC, m.BCC} {
		for _, address := range list {
			if strings.ContainsAny(address, "\r\n") {
				return errors.New("invalid recipient")
			}
			if _, err := mail.ParseAddress(address); err != nil {
				return fmt.Errorf("invalid recipient: %w", err)
			}
		}
	}
	if m.SendAt.After(time.Now().AddDate(1, 0, 0)) {
		return errors.New("schedule must be within one year")
	}
	if len(m.TransactionID) > 100 {
		return errors.New("invalid transaction ID")
	}
	return nil
}
func (s *Service) Schedule(ctx context.Context, owner string, m Send) (Outbox, error) {
	return s.schedule(ctx, owner, m, "web")
}
func (s *Service) schedule(ctx context.Context, owner string, m Send, actor string) (Outbox, error) {
	if err := validateSend(m); err != nil {
		return Outbox{}, err
	}
	key := auth.Random()
	if m.TransactionID != "" {
		key = auth.Hash(owner + "\x00" + m.TransactionID)
	}
	address, err := s.Account(ctx, owner)
	if err != nil {
		return Outbox{}, err
	}
	state := s.owner(owner)
	state.write.Lock()
	defer state.write.Unlock()
	var receipt Outbox
	receiptErr := s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": key, "owner": owner}).Decode(&receipt)
	if receiptErr == nil {
		if receipt.Account != address {
			return Outbox{}, errors.New("transaction belongs to a previously linked account")
		}
		return receipt, nil
	}
	if !errors.Is(receiptErr, mongo.ErrNoDocuments) {
		return Outbox{}, receiptErr
	}
	if _, err := s.SenderIdentity(ctx, owner, m.From); err != nil {
		return Outbox{}, err
	}
	if m.SendAt.IsZero() {
		prefs, err := s.Preferences(ctx, owner)
		if err != nil {
			return Outbox{}, err
		}
		m.SendAt = time.Now().Add(time.Duration(prefs.UndoSeconds) * time.Second)
	}
	if m.SendAt.Before(time.Now()) {
		return Outbox{}, errors.New("send time must be in the future")
	}
	if err = s.checkRenamePending(ctx, owner, address); err != nil {
		return Outbox{}, err
	}
	if err = s.remapSendSources(ctx, owner, address, &m); err != nil {
		return Outbox{}, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return Outbox{}, err
	}
	if len(raw) > 15<<20 {
		return Outbox{}, errors.New("encoded message exceeds storage limit")
	}
	job := Outbox{ID: key, Owner: owner, Account: address, Payload: s.seal(raw), SendAt: m.SendAt, Created: time.Now(), Status: "pending", Actor: actor}
	_, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": key, "owner": owner}, bson.M{"$setOnInsert": job}, options.UpdateOne().SetUpsert(true))
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return Outbox{}, fmt.Errorf("schedule send: %w", err)
	}
	var stored Outbox
	err = s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": key, "owner": owner}).Decode(&stored)
	if err == nil && stored.Account != address {
		return Outbox{}, errors.New("transaction belongs to a previously linked account")
	}
	return stored, err
}
func (s *Service) Outbox(ctx context.Context, owner string) ([]Outbox, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Collection("mail_outbox").Find(ctx, bson.M{"owner": owner, "account": account, "status": bson.M{"$nin": []string{"sent", "cancelled", "restored"}}}, options.Find().SetSort(bson.D{{Key: "created", Value: -1}}).SetLimit(100))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	jobs := []Outbox{}
	err = rows.All(ctx, &jobs)
	if err == nil {
		for i := range jobs {
			raw, openErr := s.open(jobs[i].Payload)
			if openErr != nil {
				return nil, openErr
			}
			var message Send
			if err = json.Unmarshal(raw, &message); err != nil {
				return nil, err
			}
			jobs[i].Message = &message
		}
	}
	return jobs, err
}

func (s *Service) OutboxMessage(ctx context.Context, owner, id string) (Send, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return Send{}, err
	}
	var job Outbox
	if err := s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": id, "owner": owner, "account": account}).Decode(&job); err != nil {
		return Send{}, err
	}
	raw, err := s.open(job.Payload)
	if err != nil {
		return Send{}, err
	}
	var message Send
	err = json.Unmarshal(raw, &message)
	return message, err
}
func (s *Service) CancelSend(ctx context.Context, owner, id string) error {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return err
	}
	result, err := s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": id, "owner": owner, "account": account, "status": "pending"}, bson.M{"$set": bson.M{"status": "cancelled", "payload_expires": time.Now().Add(14 * 24 * time.Hour)}})
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return errors.New("send already started or is no longer pending")
	}
	return nil
}
func (s *Service) deliverNext(ctx context.Context) error {
	var job Outbox
	err := s.db.Collection("mail_outbox").FindOneAndUpdate(ctx, bson.M{"status": "pending", "send_at": bson.M{"$lte": time.Now()}, "$or": []bson.M{{"retry_at": bson.M{"$exists": false}}, {"retry_at": bson.M{"$lte": time.Now()}}}}, bson.M{"$set": bson.M{"status": "sending"}}, options.FindOneAndUpdate().SetSort(bson.D{{Key: "send_at", Value: 1}})).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	status, problem := "sent", ""
	if err = s.deliver(ctx, job); err != nil {
		var sentCopy *smtp.SentCopyError
		switch {
		case errors.Is(err, ErrRenamePending):
			status, problem = "pending", "Waiting for folder rename recovery."
		case errors.As(err, &sentCopy):
			problem = "Message sent; saving the Sent copy failed. Do not resend."
		default:
			status = "uncertain"
			problem = "Delivery could not be confirmed. Check Sent before retrying."
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	fields := bson.M{"status": status, "error": problem}
	if status == "pending" {
		fields["retry_at"] = time.Now().Add(10 * time.Second)
	}
	update := bson.M{"$set": fields}
	if status == "sent" {
		update["$unset"] = bson.M{"payload": ""}
		fields["payload_expires"] = time.Now().Add(14 * 24 * time.Hour)
	}
	_, updateErr := s.db.Collection("mail_outbox").UpdateOne(finishCtx, bson.M{"_id": job.ID, "owner": job.Owner}, update)
	if updateErr != nil {
		return updateErr
	}
	if err != nil && !errors.Is(err, ErrRenamePending) {
		return fmt.Errorf("deliver outbox %s: %w", job.ID, err)
	}
	return nil
}
func (s *Service) deliver(ctx context.Context, job Outbox) error {
	connection, release, err := s.Acquire(ctx, job.Owner)
	if err != nil {
		return err
	}
	defer release()
	if connection.Address != job.Account {
		return errors.New("the linked iCloud account changed before delivery")
	}
	if err := s.checkRenamePending(ctx, job.Owner, job.Account); err != nil {
		return errors.Join(ErrRenamePending, err)
	}
	// Reload after acquiring the command slot: rename recovery may have rewritten the payload.
	if err := s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": job.ID, "owner": job.Owner, "account": job.Account}).Decode(&job); err != nil {
		return err
	}
	raw, err := s.open(job.Payload)
	if err != nil {
		return err
	}
	var m Send
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	from, err := s.SenderIdentity(ctx, job.Owner, m.From)
	if err != nil {
		return err
	}
	parsed, _ := mail.ParseAddress(from)
	connection.Sender.SetAliases([]string{parsed.Address})
	opts := smtp.SendOptions{CC: m.CC, BCC: m.BCC, HTML: m.HTML, Attachments: m.Attachments, From: from}
	if m.Automated {
		opts.Headers = map[string]string{"Auto-Submitted": "auto-generated"}
	}
	if m.ReplyID != "" {
		validity, validityErr := connection.Mail.UIDValidity(ctx, m.ReplyFolder)
		if validityErr != nil {
			return validityErr
		}
		if validity != m.ReplyValidity {
			return errors.New("reply mailbox changed before delivery")
		}
		original, originalErr := connection.Mail.GetEmail(ctx, m.ReplyFolder, m.ReplyID)
		if originalErr != nil {
			return originalErr
		}
		if original.MessageID != "" {
			if opts.Headers == nil {
				opts.Headers = map[string]string{}
			}
			opts.Headers["In-Reply-To"] = original.MessageID
			opts.Headers["References"] = strings.Join(append(append([]string(nil), original.References...), original.MessageID), " ")
		}
	}
	// The composer owns the envelope; the original message supplies only threading headers.
	err = connection.Sender.SendEmail(ctx, from, m.To, m.Subject, m.Body, opts)
	var sentCopy *smtp.SentCopyError
	if err != nil && !errors.As(err, &sentCopy) {
		return err
	}
	if m.DraftID != "" {
		validity, cleanupErr := connection.Mail.UIDValidity(ctx, m.DraftFolder)
		if cleanupErr == nil && validity != m.DraftValidity {
			cleanupErr = errors.New("draft mailbox changed; saved copy retained")
		}
		if cleanupErr == nil {
			cleanupErr = connection.Mail.DeleteEmail(ctx, m.DraftFolder, m.DraftID, true)
		}
		if cleanupErr != nil {
			err = errors.Join(err, &smtp.SentCopyError{Err: fmt.Errorf("remove sent draft: %w", cleanupErr)})
		}
	}
	release()
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	actor := job.Actor
	if actor == "" {
		actor = "web"
	}
	if refreshErr := s.Refresh(refreshCtx, job.Owner, actor, m.TransactionID); refreshErr != nil {
		err = errors.Join(err, &smtp.SentCopyError{Err: fmt.Errorf("refresh sent mirror: %w", refreshErr)})
	}
	return err
}

func (s *Service) RestoreUncertain(ctx context.Context, owner, id string) (Send, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return Send{}, err
	}
	state := s.owner(owner)
	state.write.Lock()
	defer state.write.Unlock()
	filter := bson.M{"_id": id, "owner": owner, "account": account, "status": bson.M{"$in": []string{"uncertain", "restored"}}}
	var job Outbox
	if err = s.db.Collection("mail_outbox").FindOne(ctx, filter).Decode(&job); err != nil {
		return Send{}, err
	}
	raw, err := s.open(job.Payload)
	if err != nil {
		return Send{}, err
	}
	var message Send
	if err = json.Unmarshal(raw, &message); err != nil {
		return Send{}, err
	}
	if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, filter, bson.M{"$set": bson.M{"status": "restored", "payload_expires": time.Now().Add(14 * 24 * time.Hour)}}); err != nil {
		return Send{}, err
	}
	message.TransactionID = ""
	message.SendAt = time.Time{}
	return message, nil
}
func (s *Service) cleanOutbox(ctx context.Context) error {
	// Keep transaction receipts permanently; only old recovered payloads expire.
	_, err := s.db.Collection("mail_outbox").UpdateMany(ctx, bson.M{"status": bson.M{"$in": []string{"sent", "cancelled", "restored"}}, "payload_expires": bson.M{"$lte": time.Now()}}, bson.M{"$unset": bson.M{"payload": "", "payload_expires": ""}})
	return err
}
