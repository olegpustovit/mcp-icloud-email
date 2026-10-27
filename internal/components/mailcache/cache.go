package mailcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/tools"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const ttl = time.Minute
const maxCacheDocumentSize = 8 << 20

type Store struct {
	client *mongo.Client
	cache  *mongo.Collection
}

type cachedEmail struct {
	ID        string     `bson:"_id"`
	Owner     string     `bson:"owner"`
	Account   string     `bson:"account"`
	Folder    string     `bson:"folder"`
	UID       string     `bson:"uid"`
	Email     imap.Email `bson:"email"`
	ExpiresAt time.Time  `bson:"expires_at"`
}

// New opens the mail cache collection in the supplied database and ensures its TTL index.
func New(ctx context.Context, mongoURI, database string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI).SetTimeout(10 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect mail cache: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mail cache: %w", err)
	}
	store := &Store{client: client, cache: client.Database(database).Collection("email_cache")}
	_, err = store.cache.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("create mail cache TTL index: %w", err)
	}
	return store, nil
}

func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

// Wrap adds an owner and account scoped GetEmail cache to an existing service.
func (s *Store) Wrap(inner tools.EmailService, owner, account string) tools.EmailService {
	return &service{EmailService: inner, store: s, owner: strings.ToLower(strings.TrimSpace(owner)), account: strings.ToLower(strings.TrimSpace(account))}
}

type service struct {
	tools.EmailService
	store   *Store
	owner   string
	account string
}

func (s *service) GetEmail(ctx context.Context, folder, uid string) (*imap.Email, error) {
	if s.store != nil && s.owner != "" && s.account != "" {
		key, err := cacheKey(s.owner, s.account, folder, uid)
		if err == nil {
			var hit cachedEmail
			err = s.store.cache.FindOne(ctx, bson.M{"_id": key, "expires_at": bson.M{"$gt": time.Now()}}).Decode(&hit)
			if err == nil {
				return &hit.Email, nil
			}
			if !errors.Is(err, mongo.ErrNoDocuments) {
				if err != nil {
					slog.Warn("email cache lookup failed", "error", err)
				}
			}
		}
	}

	email, err := s.EmailService.GetEmail(ctx, folder, uid)
	if err != nil || email == nil || s.store == nil || s.owner == "" || s.account == "" {
		return email, err
	}
	key, keyErr := cacheKey(s.owner, s.account, folder, uid)
	if keyErr != nil {
		slog.Warn("email cache key failed", "error", keyErr)
		return email, nil
	}
	doc := cachedEmail{ID: key, Owner: s.owner, Account: s.account, Folder: folder, UID: uid, Email: *email, ExpiresAt: time.Now().Add(ttl)}
	// shortcut: a concurrent read can repopulate after mutation invalidation; add generation tokens if stale reads persist beyond this TTL.
	encoded, err := bson.Marshal(doc)
	if err != nil {
		slog.Warn("email cache encoding failed", "error", err)
		return email, nil
	}
	if len(encoded) > maxCacheDocumentSize {
		slog.Warn("email cache skipped oversized message", "size_bytes", len(encoded))
		return email, nil
	}
	if _, err := s.store.cache.ReplaceOne(ctx, bson.M{"_id": key}, doc, options.Replace().SetUpsert(true)); err != nil {
		slog.Warn("email cache write failed", "error", err)
	}
	return email, nil
}

func cacheKey(owner, account, folder, uid string) (string, error) {
	identity, err := json.Marshal([]string{owner, account, folder, uid})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(identity)
	return hex.EncodeToString(hash[:]), nil
}

func (s *service) invalidate(ctx context.Context, mutationErr error) error {
	if s.store == nil || s.owner == "" || s.account == "" {
		return mutationErr
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, invalidateErr := s.store.cache.DeleteMany(cleanupCtx, bson.M{"owner": s.owner, "account": s.account})
	return errors.Join(mutationErr, invalidateErr)
}

// InvalidateOwner drops cached mail after an account is disconnected or replaced.
func (s *Store) InvalidateOwner(ctx context.Context, owner string) error {
	_, err := s.cache.DeleteMany(ctx, bson.M{"owner": strings.ToLower(strings.TrimSpace(owner))})
	return err
}

func (s *service) MarkRead(ctx context.Context, folder, uid string, read bool) error {
	return s.invalidate(ctx, s.EmailService.MarkRead(ctx, folder, uid, read))
}

func (s *service) MoveEmail(ctx context.Context, from, to, uid string) error {
	return s.invalidate(ctx, s.EmailService.MoveEmail(ctx, from, to, uid))
}

func (s *service) DeleteEmail(ctx context.Context, folder, uid string, permanent bool) error {
	return s.invalidate(ctx, s.EmailService.DeleteEmail(ctx, folder, uid, permanent))
}

func (s *service) FlagEmail(ctx context.Context, folder, uid, flag, color string) error {
	return s.invalidate(ctx, s.EmailService.FlagEmail(ctx, folder, uid, flag, color))
}

func (s *service) SaveDraft(ctx context.Context, from string, to []string, subject, body string, opts imap.DraftOptions) (string, error) {
	draft, err := s.EmailService.SaveDraft(ctx, from, to, subject, body, opts)
	return draft, s.invalidate(ctx, err)
}

func (s *service) CreateFolder(ctx context.Context, name, parent string) error {
	return s.invalidate(ctx, s.EmailService.CreateFolder(ctx, name, parent))
}

func (s *service) DeleteFolder(ctx context.Context, name string, force bool) (bool, int, error) {
	empty, count, err := s.EmailService.DeleteFolder(ctx, name, force)
	return empty, count, s.invalidate(ctx, err)
}
