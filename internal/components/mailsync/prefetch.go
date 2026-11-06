package mailsync

import (
	"context"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *Service) queuePrefetch(owner, account string) {
	select {
	case s.prefetch <- prefetchRequest{owner: owner, account: account}:
	default:
	}
}

func (s *Service) runPrefetch() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case request := <-s.prefetch:
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
			if err := s.prefetchBodies(ctx, request); err != nil && ctx.Err() == nil {
				slog.Warn("prefetch recent message bodies", "owner", request.owner, "error", err)
			}
			cancel()
		}
	}
}

func (s *Service) prefetchBodies(ctx context.Context, request prefetchRequest) error {
	rows, err := s.db.Collection("mail_messages").Find(ctx, bson.M{"owner": request.owner, "account": request.account, "email.folder": "INBOX", "body_cached": bson.M{"$ne": true}}, options.Find().SetSort(bson.D{{Key: "email.date", Value: -1}}).SetLimit(50))
	if err != nil {
		return err
	}
	var messages []Metadata
	err = rows.All(ctx, &messages)
	_ = rows.Close(ctx)
	if err != nil {
		return err
	}
	mailbox := &Mailbox{s: s, owner: request.owner, account: request.account, actor: "mcp:prefetch"}
	for _, message := range messages {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if message.Size > 8<<20 {
			continue
		}
		email, err := mailbox.GetEmail(ctx, "INBOX", message.ID)
		if err != nil {
			slog.Warn("prefetch message body", "owner", request.owner, "uid", message.ID, "error", err)
			continue
		}
		_, err = s.db.Collection("mail_messages").UpdateOne(ctx, bson.M{"_id": message.Key, "owner": request.owner, "account": request.account, "email.uidvalidity": message.UIDValidity}, bson.M{"$set": bson.M{"body_cached": true, "email.bodyplain": email.BodyPlain, "email.bodyhtml": email.BodyHTML, "email.inlineimages": email.InlineImages}})
		if err != nil {
			return err
		}
	}
	return nil
}
