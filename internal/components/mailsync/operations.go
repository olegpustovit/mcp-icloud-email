package mailsync

import (
	"context"
	"errors"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type RenameOperation struct {
	ID string `json:"id"`
	FolderRename
	Error             string    `json:"error,omitempty"`
	ReferencesUpdated bool      `json:"referencesUpdated"`
	Created           time.Time `json:"created"`
}

func (s *Service) RenameOperations(ctx context.Context, owner string) ([]RenameOperation, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Collection("mail_folder_ops").Find(ctx, bson.M{"owner": owner, "account": account, "state": "pending"}, options.Find().SetSort(bson.D{{Key: "created", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	result := []RenameOperation{}
	for rows.Next(ctx) {
		var op folderOperation
		if err = rows.Decode(&op); err != nil {
			return nil, err
		}
		result = append(result, RenameOperation{ID: op.ID, FolderRename: op.FolderRename, Error: op.Error, ReferencesUpdated: op.ReferencesUpdated, Created: op.Created})
	}
	return result, rows.Err()
}
func (s *Service) RetryRename(ctx context.Context, owner, id string) error {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return err
	}
	_, err = s.db.Collection("mail_folder_ops").UpdateOne(ctx, bson.M{"_id": id, "owner": owner, "account": account, "state": "pending"}, bson.M{"$set": bson.M{"next_attempt": time.Now(), "error": ""}})
	return err
}
func (s *Service) CancelRename(ctx context.Context, owner, id string) error {
	box, err := s.Mailbox(ctx, owner, "web")
	if err != nil {
		return err
	}
	return box.changeCommand(ctx, nil, true, func(c *Connection) error {
		var op folderOperation
		filter := bson.M{"_id": id, "owner": owner, "account": box.account, "state": "pending", "references_updated": bson.M{"$ne": true}}
		if err := s.db.Collection("mail_folder_ops").FindOne(ctx, filter).Decode(&op); err != nil {
			return errors.New("rename already applied; retry recovery instead")
		}
		validity, err := c.Mail.UIDValidity(ctx, op.From)
		if err != nil || validity != op.Validity {
			return errors.Join(imap.ErrLocationChanged, err)
		}
		result, err := s.db.Collection("mail_folder_ops").UpdateOne(ctx, filter, bson.M{"$set": bson.M{"state": "cancelled", "expires": time.Now().Add(14 * 24 * time.Hour), "error": ""}})
		if err != nil {
			return err
		}
		if result.MatchedCount != 1 {
			return errors.New("rename already changed")
		}
		return nil
	})
}
