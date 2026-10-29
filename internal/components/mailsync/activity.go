package mailsync

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *Service) Activity(ctx context.Context, owner, account string) ([]Change, error) {
	rows, err := s.db.Collection("mail_changelog").Find(ctx, bson.M{"owner": owner, "account": account, "committed": true, "actor": bson.M{"$regex": "^mcp:"}, "entity": bson.M{"$in": []string{"msg", "folder"}}}, options.Find().SetSort(bson.D{{Key: "seq", Value: -1}}).SetLimit(100))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	changes := []Change{}
	err = rows.All(ctx, &changes)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := make([]Change, 0, len(changes))
	for _, change := range changes {
		if change.MoveTransaction != "" {
			if seen[change.MoveTransaction] {
				continue
			}
			seen[change.MoveTransaction] = true
		}
		result = append(result, change)
	}
	return result, nil
}
func (s *Service) UndoActivity(ctx context.Context, owner, account string, sequence int64) error {
	var change Change
	if err := s.db.Collection("mail_changelog").FindOne(ctx, bson.M{"owner": owner, "account": account, "seq": sequence, "committed": true, "actor": bson.M{"$regex": "^mcp:"}}).Decode(&change); err != nil {
		return err
	}
	mailbox, err := s.Mailbox(ctx, owner, "web")
	if err != nil {
		return err
	}
	if mailbox.account != account {
		return errors.New("iCloud account changed")
	}
	if change.MoveTransaction != "" {
		return mailbox.UndoMove(ctx, change.MoveTransaction)
	}
	before, after := change.Before, change.Message
	if before == nil || after == nil || change.Op != "put" || before.Key != after.Key {
		return errors.New("this activity cannot be reversed")
	}
	return mailbox.change(ctx, []string{after.Folder}, func(c *Connection) error {
		var current Metadata
		if err := s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": after.Key, "owner": owner, "account": account}).Decode(&current); err != nil {
			return err
		}
		if current.Version != sequence {
			return errors.New("message changed after this activity")
		}
		live, err := c.Mail.GetEmail(ctx, after.Folder, after.ID)
		if err != nil {
			return err
		}
		if live.UIDValidity != after.UIDValidity || live.Unread != after.Unread || live.FlagColor != after.FlagColor {
			return errors.New("message changed on iCloud after this activity")
		}
		if before.Unread != after.Unread {
			return c.Mail.MarkRead(ctx, after.Folder, after.ID, !before.Unread)
		}
		if before.FlagColor != after.FlagColor {
			kind := "follow-up"
			if before.FlagColor == "" {
				kind = "none"
			}
			return c.Mail.FlagEmail(ctx, after.Folder, after.ID, kind, before.FlagColor)
		}
		return errors.New("this activity has no reversible flag change")
	})
}
func (s *Service) Watch(owner string) func() {
	state := s.owner(owner)
	s.mu.Lock()
	state.viewers++
	state.active = time.Now()
	s.mu.Unlock()
	return sync.OnceFunc(func() { s.mu.Lock(); state.viewers--; state.active = time.Now(); s.mu.Unlock() })
}
func (s *Service) touch(owner string) {
	state := s.owner(owner)
	s.mu.Lock()
	state.active = time.Now()
	s.mu.Unlock()
}
func (s *Service) pollAll(owner string, last, now time.Time) bool {
	state := s.owner(owner)
	s.mu.Lock()
	defer s.mu.Unlock()
	return state.viewers > 0 || now.Sub(state.active) < 2*time.Minute || now.Sub(last) >= 10*time.Minute
}
