package mailsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Move struct {
	ID           string        `bson:"_id" json:"id"`
	Owner        string        `bson:"owner" json:"-"`
	Account      string        `bson:"account" json:"-"`
	Transaction  string        `bson:"transaction" json:"transaction"`
	Actor        string        `bson:"actor" json:"actor"`
	Kind         string        `bson:"kind" json:"kind"`
	From         imap.Location `bson:"from" json:"from"`
	Target       string        `bson:"target" json:"target"`
	Current      imap.Location `bson:"current" json:"current"`
	Marker       string        `bson:"marker" json:"-"`
	Until        *time.Time    `bson:"until,omitempty" json:"until"`
	State        string        `bson:"state" json:"state"`
	ReturnTarget string        `bson:"return_target,omitempty" json:"-"`
	Undo         bool          `bson:"undo" json:"-"`
	Cleanup      bool          `bson:"cleanup" json:"-"`
	NextAttempt  *time.Time    `bson:"next_attempt,omitempty" json:"-"`
	Error        string        `bson:"error,omitempty" json:"error,omitempty"`
	Created      time.Time     `bson:"created" json:"created"`
	Expires      *time.Time    `bson:"expires,omitempty" json:"-"`
}

func validLocation(location imap.Location) bool {
	uid, err := strconv.ParseUint(location.ID, 10, 32)
	return err == nil && uid > 0 && location.Validity > 0 && validMailbox(location.Folder)
}
func validMailbox(name string) bool {
	return name != "" && len(name) <= 1000 && !strings.ContainsAny(name, "\r\n\x00")
}
func validSnooze(until *time.Time) bool {
	return until == nil || until.After(time.Now()) && until.Before(time.Now().AddDate(1, 0, 0))
}
func (s *Service) move(ctx context.Context, owner, account, actor, transaction string, from imap.Location, target, kind string, until *time.Time) error {
	if !validLocation(from) || !validMailbox(target) || from.Folder == target && kind != "snooze" || kind == "snooze" && !validSnooze(until) {
		return errors.New("invalid relocation or snooze time")
	}
	if transaction == "" {
		transaction = auth.Random()
	}
	if len(transaction) > 100 {
		return errors.New("invalid transaction")
	}
	box := &Mailbox{s: s, owner: owner, account: account, actor: actor}
	return s.change(ctx, owner, actor, transaction, nil, func() error {
		connection, release, err := s.Acquire(ctx, owner)
		if err != nil {
			return err
		}
		defer release()
		if connection.Address != account {
			return errors.New("iCloud account changed")
		}
		if kind == "snooze" {
			folders, err := connection.Mail.ListFolders(ctx)
			if err != nil {
				return err
			}
			found := false
			for _, folder := range folders {
				found = found || folder == target
			}
			if !found {
				if err = connection.Mail.CreateFolder(ctx, target, ""); err != nil {
					return err
				}
			}
		}
		now := time.Now().UTC()
		job := Move{ID: identity(owner, account, transaction), Owner: owner, Account: account, Actor: actor, Transaction: transaction, Kind: kind, From: from, Target: target, Marker: "$TerminalMail_" + auth.Random(), Until: until, State: "moving", NextAttempt: &now, Created: now}
		_, err = s.db.Collection("mail_moves").UpdateOne(ctx, bson.M{"_id": job.ID, "owner": owner, "account": account}, bson.M{"$setOnInsert": job}, options.UpdateOne().SetUpsert(true))
		if err != nil && !mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("record relocation: %w", err)
		}
		return box.advanceMove(ctx, connection, job.ID)
	})
}
func (m *Mailbox) advanceMove(ctx context.Context, c *Connection, id string) error {
	if err := m.s.checkRenamePending(ctx, m.owner, m.account); err != nil {
		return err
	}
	var job Move
	if err := m.s.db.Collection("mail_moves").FindOne(ctx, bson.M{"_id": id, "owner": m.owner, "account": m.account}).Decode(&job); err != nil {
		return err
	}
	if c.Address != job.Account {
		return errors.New("iCloud account changed")
	}
	var err error
	switch job.State {
	case "moving":
		job.Current, err = c.Mail.Relocate(ctx, job.From, job.Target, job.Marker)
		if err == nil && job.Kind == "unsnooze" {
			err = c.Mail.MarkRead(ctx, job.Current.Folder, job.Current.ID, false)
		}
		if err == nil {
			job.State = "moved"
			next := time.Now().Add(14 * 24 * time.Hour)
			job.NextAttempt = &next
			if job.Kind == "snooze" {
				job.State = "snoozed"
				job.NextAttempt = job.Until
			}
		}
	case "waking":
		job.Current, err = c.Mail.Relocate(ctx, job.Current, job.ReturnTarget, job.Marker+"r")
		if err == nil && job.Kind == "snooze" && !job.Undo {
			err = c.Mail.MarkRead(ctx, job.Current.Folder, job.Current.ID, false)
		}
		if err == nil {
			job.State = "awake"
			if job.Undo {
				job.State = "undone"
			}
			job.Cleanup = true
			if _, err = m.s.db.Collection("mail_moves").ReplaceOne(context.WithoutCancel(ctx), bson.M{"_id": job.ID, "owner": m.owner, "account": m.account}, job); err != nil {
				return err
			}
			err = c.Mail.ClearMoveMarker(ctx, job.Current, job.Marker)
			if err == nil {
				err = c.Mail.ClearMoveMarker(ctx, job.Current, job.Marker+"r")
			}
			if err == nil {
				job.Cleanup = false
				job.NextAttempt = nil
				expires := time.Now().Add(14 * 24 * time.Hour)
				job.Expires = &expires
			}
		}
	case "moved":
		if job.NextAttempt == nil || job.NextAttempt.After(time.Now()) {
			return nil
		}
		err = c.Mail.ClearMoveMarker(ctx, job.Current, job.Marker)
		if err == nil {
			job.State = "expired"
			job.NextAttempt = nil
			expires := time.Now().Add(14 * 24 * time.Hour)
			job.Expires = &expires
		}
	case "awake", "undone":
		if !job.Cleanup {
			return nil
		}
		err = c.Mail.ClearMoveMarker(ctx, job.Current, job.Marker)
		if err == nil {
			err = c.Mail.ClearMoveMarker(ctx, job.Current, job.Marker+"r")
		}
		if err == nil {
			job.Cleanup = false
			job.NextAttempt = nil
			expires := time.Now().Add(14 * 24 * time.Hour)
			job.Expires = &expires
		}
	case "snoozed", "expired", "cancelled":
		return nil
	case "failed":
		return imap.ErrLocationChanged
	default:
		return errors.New("unknown relocation state")
	}
	if err != nil {
		next := time.Now().Add(time.Minute)
		fields := bson.M{"error": "Message could not be relocated. It will be checked before retrying.", "next_attempt": next}
		if errors.Is(err, imap.ErrLocationChanged) || errors.Is(err, imap.ErrMarkerUnsupported) {
			fields["state"] = "failed"
			fields["next_attempt"] = nil
			fields["error"] = "The message location changed or the mailbox cannot retain move markers. Refresh before trying again."
		}
		_, updateErr := m.s.db.Collection("mail_moves").UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": job.ID, "owner": m.owner, "account": m.account}, bson.M{"$set": fields})
		return errors.Join(err, updateErr)
	}
	job.Error = ""
	_, err = m.s.db.Collection("mail_moves").ReplaceOne(context.WithoutCancel(ctx), bson.M{"_id": job.ID, "owner": m.owner, "account": m.account}, job)
	return err
}
func (m *Mailbox) MoveWithValidity(ctx context.Context, from imap.Location, target string) error {
	return m.s.move(ctx, m.owner, m.account, m.actor, transactionID(ctx), from, target, "move", nil)
}
func (m *Mailbox) Snooze(ctx context.Context, from imap.Location, until *time.Time) error {
	if !validSnooze(until) {
		return errors.New("snooze must be in the future and within one year")
	}
	if strings.EqualFold(from.Folder, "Snoozed") {
		count, err := m.s.db.Collection("mail_moves").CountDocuments(ctx, bson.M{"owner": m.owner, "account": m.account, "state": "snoozed", "current.folder": from.Folder, "current.uid": from.ID, "current.validity": from.Validity})
		if err != nil {
			return err
		}
		if count == 0 {
			return m.s.move(ctx, m.owner, m.account, m.actor, transactionID(ctx), from, "Snoozed", "snooze", until)
		}
		return m.change(ctx, nil, func(c *Connection) error {
			var job Move
			if err := m.s.db.Collection("mail_moves").FindOne(ctx, bson.M{"owner": m.owner, "account": m.account, "state": "snoozed", "current.folder": from.Folder, "current.uid": from.ID, "current.validity": from.Validity}).Decode(&job); err != nil {
				return err
			}
			result, err := m.s.db.Collection("mail_moves").UpdateOne(ctx, bson.M{"_id": job.ID, "owner": m.owner, "account": m.account, "state": "snoozed"}, bson.M{"$set": bson.M{"until": until, "next_attempt": until}})
			if err == nil && result.MatchedCount == 0 {
				return errors.New("snooze changed before rescheduling")
			}
			if err != nil {
				return err
			}
			return m.s.updateSnoozeMetadata(ctx, m.owner, m.account, from, until, m.actor, transactionID(ctx))
		})
	}
	return m.s.move(ctx, m.owner, m.account, m.actor, transactionID(ctx), from, "Snoozed", "snooze", until)
}
func (m *Mailbox) returnMove(ctx context.Context, originalTransaction string, undo bool) error {
	id := identity(m.owner, m.account, originalTransaction)
	return m.change(ctx, nil, func(c *Connection) error {
		if err := m.advanceMove(ctx, c, id); err != nil {
			return err
		}
		var job Move
		if err := m.s.db.Collection("mail_moves").FindOne(ctx, bson.M{"_id": id, "owner": m.owner, "account": m.account}).Decode(&job); err != nil {
			return err
		}
		if job.State == "awake" || job.State == "undone" {
			return nil
		}
		if job.State != "waking" {
			if job.State != "moved" && job.State != "snoozed" {
				return errors.New("relocation can no longer be undone")
			}
			now := time.Now()
			target := job.From.Folder
			if job.Kind == "snooze" {
				if strings.EqualFold(target, "Snoozed") {
					target = "INBOX"
				}
				if _, err := c.Mail.UIDValidity(ctx, target); err != nil {
					target = "INBOX"
				}
			}
			if _, err := m.s.db.Collection("mail_moves").UpdateOne(ctx, bson.M{"_id": id, "owner": m.owner, "account": m.account}, bson.M{"$set": bson.M{"state": "waking", "return_target": target, "undo": undo, "next_attempt": now}}); err != nil {
				return err
			}
		}
		return m.advanceMove(ctx, c, id)
	})
}
func (m *Mailbox) UndoMove(ctx context.Context, originalTransaction string) error {
	if originalTransaction == "" || len(originalTransaction) > 100 {
		return errors.New("invalid original transaction")
	}
	return m.returnMove(ctx, originalTransaction, true)
}
func (m *Mailbox) Unsnooze(ctx context.Context, from imap.Location) error {
	if !validLocation(from) {
		return errors.New("invalid snoozed message")
	}
	var job Move
	if err := m.s.db.Collection("mail_moves").FindOne(ctx, bson.M{"owner": m.owner, "account": m.account, "kind": "snooze", "state": "snoozed", "current.folder": from.Folder, "current.uid": from.ID, "current.validity": from.Validity}).Decode(&job); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) && strings.EqualFold(from.Folder, "Snoozed") {
			return m.s.move(ctx, m.owner, m.account, m.actor, transactionID(ctx), from, "INBOX", "unsnooze", nil)
		}
		return err
	}
	return m.returnMove(ctx, job.Transaction, false)
}
func (s *Service) runMove(ctx context.Context) error {
	var job Move
	err := s.db.Collection("mail_moves").FindOne(ctx, bson.M{"state": bson.M{"$in": []string{"moving", "waking", "moved", "snoozed", "awake", "undone"}}, "next_attempt": bson.M{"$type": "date", "$lte": time.Now()}}, options.FindOne().SetSort(bson.D{{Key: "next_attempt", Value: 1}})).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	box := &Mailbox{s: s, owner: job.Owner, account: job.Account, actor: "system"}
	if job.State == "snoozed" {
		err = box.returnMove(ctx, job.Transaction, false)
	} else {
		err = box.change(ctx, nil, func(c *Connection) error { return box.advanceMove(ctx, c, job.ID) })
	}
	if err != nil {
		next := time.Now().Add(time.Minute)
		_, updateErr := s.db.Collection("mail_moves").UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": job.ID, "owner": job.Owner, "account": job.Account, "state": bson.M{"$nin": []string{"failed", "cancelled"}}}, bson.M{"$set": bson.M{"next_attempt": next, "error": "Relocation is paused; check the connected account and message location."}})
		return errors.Join(err, updateErr, s.reportSnoozeFailure(context.WithoutCancel(ctx), job))
	}
	return nil
}
func (s *Service) decorateMoves(ctx context.Context, owner, account string, mail []Metadata) error {
	type mailbox struct {
		folder   string
		validity uint32
	}
	uids := map[mailbox][]string{}
	markers := map[string]bool{}
	for _, message := range mail {
		box := mailbox{message.Folder, message.UIDValidity}
		uids[box] = append(uids[box], message.ID)
		for _, flag := range message.Flags {
			if strings.HasPrefix(flag, "$TerminalMail_") {
				markers[flag] = true
			}
		}
	}
	conditions := make([]bson.M, 0, len(uids)+1)
	for box, ids := range uids {
		conditions = append(conditions, bson.M{"current.folder": box.folder, "current.validity": box.validity, "current.uid": bson.M{"$in": ids}})
	}
	if len(markers) > 0 {
		values := make([]string, 0, len(markers))
		for marker := range markers {
			values = append(values, marker)
		}
		conditions = append(conditions, bson.M{"marker": bson.M{"$in": values}})
	}
	if len(conditions) == 0 {
		return nil
	}
	rows, err := s.db.Collection("mail_moves").Find(ctx, bson.M{"owner": owner, "account": account, "state": bson.M{"$in": []string{"snoozed", "moved", "moving", "failed"}}, "$or": conditions}, options.Find().SetSort(bson.D{{Key: "created", Value: 1}}))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close(ctx) }()
	var jobs []Move
	if err = rows.All(ctx, &jobs); err != nil {
		return err
	}
	byKey := make(map[string]Move, len(jobs))
	byMarker := make(map[string]Move, len(jobs))
	for _, job := range jobs {
		if job.Current.ID != "" {
			byKey[messageKey(owner, account, job.Current.Folder, job.Current.Validity, job.Current.ID)] = job
		}
		byMarker[job.Marker] = job
	}
	for i := range mail {
		job, ok := byKey[mail[i].Key]
		if !ok {
			for _, flag := range mail[i].Flags {
				if matched, exists := byMarker[flag]; exists && matched.Target == mail[i].Folder {
					job = matched
					ok = true
					break
				}
			}
		}
		if ok {
			mail[i].Snoozed = job.Kind == "snooze"
			if mail[i].Snoozed {
				mail[i].SnoozeUntil = job.Until
				mail[i].SnoozeError = job.Error
			}
			mail[i].MoveTransaction = job.Transaction
			mail[i].MoveActor = job.Actor
		}
	}
	return nil
}
func (s *Service) updateSnoozeMetadata(ctx context.Context, owner, account string, location imap.Location, until *time.Time, actor, tx string) error {
	var metadata Metadata
	if err := s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": messageKey(owner, account, location.Folder, location.Validity, location.ID), "owner": owner, "account": account}).Decode(&metadata); err != nil {
		return err
	}
	metadata.Snoozed = true
	metadata.SnoozeUntil = until
	metadata.SnoozeError = ""
	if err := s.commit(ctx, owner, account, actor, tx, []Change{{Entity: "msg", Op: "put", Key: metadata.Key, Message: &metadata}}); err != nil {
		return err
	}
	return nil
}
func (s *Service) reportSnoozeFailure(ctx context.Context, job Move) error {
	if job.Kind != "snooze" {
		return nil
	}
	mail, err := s.metadata(ctx, job.Owner, job.Account, bson.M{"email.flags": job.Marker}, 0)
	if err != nil {
		return err
	}
	var changes []Change
	for _, message := range mail {
		if message.SnoozeError != "" {
			continue
		}
		message.SnoozeError = "RETURN: PAUSED // The message or mailbox changed. Use RETURN to choose the current message, or reschedule it."
		changes = append(changes, Change{Entity: "msg", Op: "put", Key: message.Key, Message: &message})
	}
	return s.commit(ctx, job.Owner, job.Account, "system", "", changes)
}
