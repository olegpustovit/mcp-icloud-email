package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type FolderRename struct {
	From      string `bson:"from" json:"from"`
	To        string `bson:"to" json:"to"`
	Delimiter string `bson:"delimiter" json:"delimiter"`
}
type folderOperation struct {
	ID                string `bson:"_id"`
	Owner             string `bson:"owner"`
	Account           string `bson:"account"`
	Actor             string `bson:"actor"`
	Transaction       string `bson:"transaction"`
	FolderRename      `bson:",inline"`
	Generations       map[string]uint32 `bson:"generations"`
	Validity          uint32            `bson:"validity"`
	State             string            `bson:"state"`
	NextAttempt       time.Time         `bson:"next_attempt"`
	Created           time.Time         `bson:"created"`
	Expires           *time.Time        `bson:"expires,omitempty"`
	ReferencesUpdated bool              `bson:"references_updated,omitempty"`
	Error             string            `bson:"error,omitempty"`
}

var ErrRenamePending = errors.New("folder rename recovery is pending")

func (r FolderRename) path(name string) string {
	if name == r.From || r.Delimiter != "" && strings.HasPrefix(name, r.From+r.Delimiter) {
		return r.To + strings.TrimPrefix(name, r.From)
	}
	return name
}
func (m *Mailbox) RenameFolder(ctx context.Context, from, to string) error {
	if !validMailbox(from) || !validMailbox(to) || from == to || strings.EqualFold(from, "INBOX") {
		return errors.New("invalid mailbox rename")
	}
	transaction := transactionID(ctx)
	if transaction == "" {
		transaction = auth.Random()
	}
	return m.changeCommand(ctx, nil, true, func(c *Connection) error {
		id := identity(m.owner, m.account, transaction)
		var op folderOperation
		err := m.s.db.Collection("mail_folder_ops").FindOne(ctx, bson.M{"_id": id, "owner": m.owner, "account": m.account}).Decode(&op)
		if errors.Is(err, mongo.ErrNoDocuments) {
			folders, err := m.s.Folders(ctx, m.owner, m.account)
			if err != nil {
				return err
			}
			var source *Folder
			for i := range folders {
				f := &folders[i]
				if f.Name == from {
					source = f
				}
				if (f.Name == from || f.Delimiter != "" && strings.HasPrefix(f.Name, from+f.Delimiter)) && f.Role != "" {
					return errors.New("system mailboxes cannot be renamed")
				}
			}
			if source == nil {
				return errors.New("source mailbox missing")
			}
			if source.Delimiter != "" && strings.HasPrefix(to, from+source.Delimiter) {
				return errors.New("mailbox cannot become its own child")
			}
			if err = m.s.checkRenamePending(ctx, m.owner, m.account); err != nil {
				return err
			}
			validity, err := c.Mail.UIDValidity(ctx, from)
			if err != nil {
				return err
			}
			op = folderOperation{ID: id, Owner: m.owner, Account: m.account, Actor: m.actor, Transaction: transaction, FolderRename: FolderRename{From: from, To: to, Delimiter: source.Delimiter}, Validity: validity, State: "pending", NextAttempt: time.Now(), Created: time.Now()}
			op.Generations = map[string]uint32{from: validity}
			for _, folder := range folders {
				if folder.Name != from && op.path(folder.Name) != folder.Name {
					generation, generationErr := c.Mail.UIDValidity(ctx, folder.Name)
					if generationErr != nil {
						return generationErr
					}
					op.Generations[folder.Name] = generation
				}
			}
			if _, err = m.s.db.Collection("mail_folder_ops").InsertOne(ctx, op); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if op.From != from || op.To != to {
			return errors.New("transaction belongs to another rename")
		}
		return m.s.applyRename(ctx, c, op)
	})
}
func (s *Service) checkRenamePending(ctx context.Context, owner, account string) error {
	n, err := s.db.Collection("mail_folder_ops").CountDocuments(ctx, bson.M{"owner": owner, "account": account, "state": "pending"})
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrRenamePending
	}
	return nil
}
func (s *Service) applyRename(ctx context.Context, c *Connection, op folderOperation) error {
	if op.State == "done" {
		return nil
	}
	if op.State != "pending" || c.Address != op.Account {
		return errors.New("rename account or state changed")
	}
	names, err := c.Mail.ListFolders(ctx)
	if err != nil {
		return err
	}
	source, target := slices.Contains(names, op.From), slices.Contains(names, op.To)
	if source && !target {
		validity, err := c.Mail.UIDValidity(ctx, op.From)
		if err != nil {
			return err
		}
		if validity != op.Validity {
			return imap.ErrLocationChanged
		}
		if err = c.Mail.RenameFolder(ctx, op.From, op.To); err != nil {
			return err
		}
	} else if source || !target {
		return imap.ErrLocationChanged
	}
	validity, err := c.Mail.UIDValidity(ctx, op.To)
	if err != nil {
		return err
	}
	if validity != op.Validity {
		return imap.ErrLocationChanged
	}
	state := s.owner(op.Owner)
	state.write.Lock()
	defer state.write.Unlock()
	if _, err = s.db.Collection("mail_folder_ops").UpdateOne(ctx, bson.M{"_id": op.ID, "owner": op.Owner, "account": op.Account, "state": "pending"}, bson.M{"$set": bson.M{"references_updated": true}}); err != nil {
		return err
	}
	if err = s.rewriteRenameRefs(ctx, op); err != nil {
		return err
	}
	// The path event and preferences share the journal; replay is safe after any interruption.
	if err = s.recoverOwner(ctx, op.Owner); err != nil {
		return err
	}
	prefs, err := s.Preferences(ctx, op.Owner)
	if err != nil {
		return err
	}
	for i := range prefs.Rules {
		for j := range prefs.Rules[i].Actions {
			if prefs.Rules[i].Actions[j].Action == "move" {
				prefs.Rules[i].Actions[j].Target = op.path(prefs.Rules[i].Actions[j].Target)
			}
		}
		if prefs.Rules[i].Action == "move" {
			prefs.Rules[i].Target = op.path(prefs.Rules[i].Target)
		}
	}
	rename := op.FolderRename
	if err = s.commitLocked(ctx, op.Owner, op.Account, op.Actor, op.Transaction, []Change{{Entity: "rename", Op: "put", Key: op.ID, Rename: &rename}, {Entity: "pref", Op: "put", Key: op.Owner, Preferences: &prefs}}); err != nil {
		return err
	}
	expires := time.Now().Add(14 * 24 * time.Hour)
	_, err = s.db.Collection("mail_folder_ops").UpdateOne(ctx, bson.M{"_id": op.ID, "owner": op.Owner, "account": op.Account, "state": "pending"}, bson.M{"$set": bson.M{"state": "done", "expires": expires}})
	return err
}
func (s *Service) rewriteRenameRefs(ctx context.Context, op folderOperation) error {
	scope := bson.M{"owner": op.Owner, "account": op.Account}
	rows, err := s.db.Collection("mail_moves").Find(ctx, scope)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close(ctx) }()
	for rows.Next(ctx) {
		var job Move
		if err = rows.Decode(&job); err != nil {
			return err
		}
		changed := false
		for _, path := range []*string{&job.From.Folder, &job.Target, &job.Current.Folder, &job.ReturnTarget} {
			next := op.path(*path)
			if next != *path {
				*path = next
				changed = true
			}
		}
		if changed {
			if _, err = s.db.Collection("mail_moves").ReplaceOne(ctx, bson.M{"_id": job.ID, "owner": op.Owner, "account": op.Account}, job); err != nil {
				return err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	ruleRows, err := s.db.Collection("mail_rule_jobs").Find(ctx, bson.M{"owner": op.Owner, "account": op.Account, "state": bson.M{"$in": []string{"pending", "failed"}}})
	if err != nil {
		return err
	}
	defer func() { _ = ruleRows.Close(ctx) }()
	for ruleRows.Next(ctx) {
		var job RuleJob
		if err = ruleRows.Decode(&job); err != nil {
			return err
		}
		folder := op.path(job.Source.Folder)
		actions := job.Actions
		for i := range actions {
			if actions[i].Action == "move" {
				actions[i].Target = op.path(actions[i].Target)
			}
		}
		if _, err = s.db.Collection("mail_rule_jobs").UpdateOne(ctx, bson.M{"_id": job.ID, "owner": op.Owner, "account": op.Account}, bson.M{"$set": bson.M{"source.folder": folder, "actions": actions}}); err != nil {
			return err
		}
	}
	if err = ruleRows.Err(); err != nil {
		return err
	}
	applications, err := s.db.Collection("mail_rule_applications").Find(ctx, bson.M{"owner": op.Owner, "account": op.Account, "state": "pending"})
	if err != nil {
		return err
	}
	defer func() { _ = applications.Close(ctx) }()
	for applications.Next(ctx) {
		var job RuleApplication
		if err = applications.Decode(&job); err != nil {
			return err
		}
		for i := range job.Rules {
			if job.Rules[i].Action == "move" {
				job.Rules[i].Target = op.path(job.Rules[i].Target)
			}
			for j := range job.Rules[i].Actions {
				if job.Rules[i].Actions[j].Action == "move" {
					job.Rules[i].Actions[j].Target = op.path(job.Rules[i].Actions[j].Target)
				}
			}
		}
		if _, err = s.db.Collection("mail_rule_applications").UpdateOne(ctx, bson.M{"_id": job.ID, "state": "pending"}, bson.M{"$set": bson.M{"folder": op.path(job.Folder), "rules": job.Rules}}); err != nil {
			return err
		}
	}
	if err = applications.Err(); err != nil {
		return err
	}
	outbox, err := s.db.Collection("mail_outbox").Find(ctx, bson.M{"owner": op.Owner, "account": op.Account, "payload": bson.M{"$exists": true}, "status": bson.M{"$in": []string{"pending", "sending", "uncertain", "cancelled", "restored"}}})
	if err != nil {
		return err
	}
	defer func() { _ = outbox.Close(ctx) }()
	for outbox.Next(ctx) {
		var job Outbox
		if err = outbox.Decode(&job); err != nil {
			return err
		}
		raw, err := s.open(job.Payload)
		if err != nil {
			return err
		}
		var message Send
		if err = json.Unmarshal(raw, &message); err != nil {
			return err
		}
		reply, draft := op.path(message.ReplyFolder), op.path(message.DraftFolder)
		if reply == message.ReplyFolder && draft == message.DraftFolder {
			continue
		}
		message.ReplyFolder, message.DraftFolder = reply, draft
		raw, err = json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err = s.db.Collection("mail_outbox").UpdateOne(ctx, bson.M{"_id": job.ID, "owner": op.Owner, "account": op.Account, "payload": job.Payload}, bson.M{"$set": bson.M{"payload": s.seal(raw)}}); err != nil {
			return err
		}
	}
	return outbox.Err()
}
func (s *Service) runRename(ctx context.Context) error {
	var op folderOperation
	err := s.db.Collection("mail_folder_ops").FindOne(ctx, bson.M{"state": "pending", "next_attempt": bson.M{"$lte": time.Now()}}, options.FindOne().SetSort(bson.D{{Key: "created", Value: 1}})).Decode(&op)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	m, err := s.Mailbox(ctx, op.Owner, op.Actor)
	if err == nil && m.account != op.Account {
		_, err = s.db.Collection("mail_folder_ops").UpdateOne(ctx, bson.M{"_id": op.ID, "owner": op.Owner, "account": op.Account, "state": "pending"}, bson.M{"$set": bson.M{"state": "cancelled", "expires": time.Now().Add(14 * 24 * time.Hour)}})
		return err
	}
	if err == nil {
		err = m.changeCommand(ctx, nil, true, func(c *Connection) error { return s.applyRename(ctx, c, op) })
	}
	if err != nil {
		_, updateErr := s.db.Collection("mail_folder_ops").UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": op.ID, "state": "pending"}, bson.M{"$set": bson.M{"next_attempt": time.Now().Add(time.Minute), "error": cutText(err.Error(), 500)}})
		return errors.Join(err, updateErr)
	}
	return nil
}

func (s *Service) remapSendSources(ctx context.Context, owner, account string, message *Send) error {
	rows, err := s.db.Collection("mail_folder_ops").Find(ctx, bson.M{"owner": owner, "account": account, "state": "done"}, options.Find().SetSort(bson.D{{Key: "created", Value: 1}}))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close(ctx) }()
	for rows.Next(ctx) {
		var op folderOperation
		if err = rows.Decode(&op); err != nil {
			return err
		}
		if message.ReplyValidity != 0 && op.Generations[message.ReplyFolder] == message.ReplyValidity {
			message.ReplyFolder = op.path(message.ReplyFolder)
		}
		if message.DraftValidity != 0 && op.Generations[message.DraftFolder] == message.DraftValidity {
			message.DraftFolder = op.path(message.DraftFolder)
		}
	}
	return rows.Err()
}
