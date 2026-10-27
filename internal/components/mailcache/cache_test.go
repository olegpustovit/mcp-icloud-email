package mailcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/tools"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type emailStub struct {
	tools.EmailService
	gets        int
	mutations   int
	mutationErr error
}

func (s *emailStub) GetEmail(context.Context, string, string) (*imap.Email, error) {
	s.gets++
	return &imap.Email{Subject: fmt.Sprintf("message %d", s.gets)}, nil
}
func (s *emailStub) MarkRead(context.Context, string, string, bool) error    { return s.mutate() }
func (s *emailStub) MoveEmail(context.Context, string, string, string) error { return s.mutate() }
func (s *emailStub) DeleteEmail(context.Context, string, string, bool) error { return s.mutate() }
func (s *emailStub) FlagEmail(context.Context, string, string, string, string) error {
	return s.mutate()
}
func (s *emailStub) SaveDraft(context.Context, string, []string, string, string, imap.DraftOptions) (string, error) {
	return "draft", s.mutate()
}
func (s *emailStub) CreateFolder(context.Context, string, string) error { return s.mutate() }
func (s *emailStub) DeleteFolder(context.Context, string, bool) (bool, int, error) {
	return false, 0, s.mutate()
}
func (s *emailStub) mutate() error {
	s.mutations++
	return s.mutationErr
}

func TestMongoScopedCacheAndMutationInvalidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	database := "mailcache_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	store, err := New(ctx, uri, database)
	if err != nil {
		t.Skipf("MongoDB test service unavailable: %v", err)
	}
	defer func() {
		_ = store.Close(context.Background())
		cleanup, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err == nil {
			_ = cleanup.Database(database).Drop(context.Background())
			_ = cleanup.Disconnect(context.Background())
		}
	}()

	inner := &emailStub{}
	alice := store.Wrap(inner, "alice@example.com", "icloud@example.com")
	first, err := alice.GetEmail(ctx, "INBOX", "7")
	if err != nil || first.Subject != "message 1" {
		t.Fatalf("first fetch = %+v, %v", first, err)
	}
	second, err := alice.GetEmail(ctx, "INBOX", "7")
	if err != nil || second.Subject != "message 1" || inner.gets != 1 {
		t.Fatalf("cache hit = %+v, %v; backend gets=%d", second, err, inner.gets)
	}
	for _, scope := range []struct{ owner, account, folder, uid string }{
		{"bob@example.com", "icloud@example.com", "INBOX", "7"},
		{"alice@example.com", "other@example.com", "INBOX", "7"},
		{"alice@example.com", "icloud@example.com", "Sent", "7"},
		{"alice@example.com", "icloud@example.com", "INBOX", "8"},
	} {
		client := store.Wrap(inner, scope.owner, scope.account)
		if _, err := client.GetEmail(ctx, scope.folder, scope.uid); err != nil {
			t.Fatal(err)
		}
	}
	if inner.gets != 5 {
		t.Fatalf("scope collision: backend gets=%d, want 5", inner.gets)
	}

	inner.mutationErr = errors.New("uncertain write")
	mutators := []struct {
		name string
		call func(tools.EmailService) error
	}{
		{"mark_read", func(c tools.EmailService) error { return c.MarkRead(ctx, "INBOX", "x", true) }},
		{"move_email", func(c tools.EmailService) error { return c.MoveEmail(ctx, "INBOX", "Sent", "x") }},
		{"delete_email", func(c tools.EmailService) error { return c.DeleteEmail(ctx, "INBOX", "x", false) }},
		{"flag_email", func(c tools.EmailService) error { return c.FlagEmail(ctx, "INBOX", "x", "important", "") }},
		{"save_draft", func(c tools.EmailService) error {
			_, err := c.SaveDraft(ctx, "a@example.com", nil, "s", "b", imap.DraftOptions{})
			return err
		}},
		{"create_folder", func(c tools.EmailService) error { return c.CreateFolder(ctx, "Work", "") }},
		{"delete_folder", func(c tools.EmailService) error { _, _, err := c.DeleteFolder(ctx, "Work", false); return err }},
	}
	for i, mutator := range mutators {
		t.Run(mutator.name, func(t *testing.T) {
			uid := fmt.Sprintf("mutation-%d", i)
			if _, err := alice.GetEmail(ctx, "INBOX", uid); err != nil {
				t.Fatal(err)
			}
			if err := mutator.call(alice); err == nil {
				t.Fatal("expected underlying mutation error")
			}
			before := inner.gets
			if _, err := alice.GetEmail(ctx, "INBOX", uid); err != nil {
				t.Fatal(err)
			}
			if inner.gets != before+1 {
				t.Fatalf("cache was not invalidated after failed mutation; gets=%d want %d", inner.gets, before+1)
			}
		})
	}
}

func TestCacheMissesExpiredDocuments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	database := "mailcache_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	store, err := New(ctx, uri, database)
	if err != nil {
		t.Skipf("MongoDB test service unavailable: %v", err)
	}
	defer func() {
		_ = store.Close(context.Background())
		cleanup, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err == nil {
			_ = cleanup.Database(database).Drop(context.Background())
			_ = cleanup.Disconnect(context.Background())
		}
	}()

	inner := &emailStub{}
	client := store.Wrap(inner, "alice@example.com", "icloud@example.com")
	if _, err := client.GetEmail(ctx, "INBOX", "1"); err != nil {
		t.Fatal(err)
	}
	key, _ := cacheKey("alice@example.com", "icloud@example.com", "INBOX", "1")
	if _, err := store.cache.UpdateOne(ctx, bson.M{"_id": key}, bson.M{"$set": bson.M{"expires_at": time.Now().Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetEmail(ctx, "INBOX", "1"); err != nil {
		t.Fatal(err)
	}
	if inner.gets != 2 {
		t.Fatalf("backend gets=%d, want cache miss after expiry", inner.gets)
	}
	indexes, err := store.cache.Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundTTL bool
	for indexes.Next(ctx) {
		var index bson.M
		if err := indexes.Decode(&index); err != nil {
			t.Fatal(err)
		}
		if index["expireAfterSeconds"] == int32(0) {
			foundTTL = true
		}
	}
	if err := indexes.Err(); err != nil {
		t.Fatal(err)
	}
	if !foundTTL {
		t.Fatal("TTL index not found")
	}
}
