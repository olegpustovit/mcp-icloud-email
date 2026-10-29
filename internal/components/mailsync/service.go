package mailsync

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"github.com/olegpustovit/mcp-icloud-email/tools"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	client      *mongo.Client
	db          *mongo.Database
	credentials func(context.Context, string) (string, string, error)
	cipher      cipher.AEAD
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	sessions    map[string]*session
	owners      map[string]*ownerState
	monitors    map[string]*monitor
	dialSync    func(context.Context, string, string, func()) (syncTransport, error)
	dialCommand func(context.Context, string, string) (*Connection, error)
	pushSession func(context.Context, string, string) (bool, error)
	pushSubject string
	pushHTTP    *http.Client
}

type Connection struct {
	Mail    commandClient
	Sender  sender
	Address string
}
type sender interface {
	SetAliases([]string)
	SendEmail(context.Context, string, []string, string, string, smtp.SendOptions) error
	ReplyToEmail(context.Context, *imap.Email, string, bool, smtp.SendOptions) error
}
type commandClient interface {
	tools.EmailService
	UIDValidity(context.Context, string) (uint32, error)
	Closed() bool
	Close() error
	Relocate(context.Context, imap.Location, string, string) (imap.Location, error)
	ClearMoveMarker(context.Context, imap.Location, string) error
	RenameFolder(context.Context, string, string) error
	SearchPage(context.Context, string, string, imap.EmailFilters) (imap.SearchPage, error)
}

func (s *Service) Account(ctx context.Context, owner string) (string, error) {
	address, _, err := s.credentials(ctx, owner)
	return address, err
}

type session struct {
	gate        chan struct{}
	connection  *Connection
	fingerprint [32]byte
	used        time.Time
}

func New(ctx context.Context, uri, key string, credentials func(context.Context, string) (string, string, error)) (*Service, error) {
	return newService(ctx, uri, key, credentials, "mail")
}
func newService(ctx context.Context, uri, key string, credentials func(context.Context, string) (string, string, error), database string) (*Service, error) {
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid mail encryption key")
	}
	block, err := aes.NewCipher(decoded)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetTimeout(10 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect mail sync: %w", err)
	}
	if err = client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mail sync: %w", err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s := &Service{client: client, db: client.Database(database), cipher: aead, credentials: credentials, ctx: workerCtx, cancel: cancel, sessions: map[string]*session{}, owners: map[string]*ownerState{}, monitors: map[string]*monitor{}, dialSync: func(ctx context.Context, address, password string, notify func()) (syncTransport, error) {
		return imap.NewSyncClient(ctx, address, password, notify)
	}}
	s.dialCommand = func(ctx context.Context, address, password string) (*Connection, error) {
		client, err := imap.NewClient(address, password)
		if err != nil {
			return nil, err
		}
		return &Connection{Mail: client, Sender: smtp.NewClient(address, password, client.SaveSent), Address: address}, nil
	}
	for collection, models := range map[string][]mongo.IndexModel{
		"mail_rule_jobs":         {{Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_attempt", Value: 1}, {Key: "created", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "state", Value: 1}}}, {Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"mail_rule_applications": {{Keys: bson.D{{Key: "state", Value: 1}, {Key: "created", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}}}, {Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"mail_push_devices":      {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}}}, {Keys: bson.D{{Key: "next_attempt", Value: 1}}}},
		"mail_folder_ops":        {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "state", Value: 1}}}, {Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_attempt", Value: 1}}}, {Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"mail_outbox":            {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "created", Value: -1}}}, {Keys: bson.D{{Key: "status", Value: 1}, {Key: "send_at", Value: 1}}}, {Keys: bson.D{{Key: "payload_expires", Value: 1}}}},
		"mail_activity":          {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "at", Value: -1}}}},
		"mail_moves":             {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "transaction", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_attempt", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "current.folder", Value: 1}, {Key: "current.validity", Value: 1}, {Key: "current.uid", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "marker", Value: 1}}}, {Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"mail_messages":          {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "email.folder", Value: 1}, {Key: "email.date", Value: -1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "email.threadid", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "email.messageid", Value: 1}}}, {Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "subject_key", Value: 1}, {Key: "email.date", Value: -1}}}},
		"mail_folders":           {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "info.name", Value: 1}}}},
		"mail_changelog":         {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "seq", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(14 * 86400).SetPartialFilterExpression(bson.M{"committed": true})}},
		"mail_thread_nodes":      {{Keys: bson.D{{Key: "owner", Value: 1}, {Key: "account", Value: 1}, {Key: "thread", Value: 1}}}},
	} {
		if _, err = s.db.Collection(collection).Indexes().CreateMany(ctx, models); err != nil {
			cancel()
			_ = client.Disconnect(context.Background())
			return nil, fmt.Errorf("index %s: %w", collection, err)
		}
	}
	if err = s.recoverOwner(ctx, ""); err != nil {
		cancel()
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	// A process crash during SMTP is ambiguous; never automatically send it twice.
	if _, err = s.db.Collection("mail_outbox").UpdateMany(ctx, bson.M{"status": "sending"}, bson.M{"$set": bson.M{"status": "uncertain", "error": "Server restarted during delivery; check Sent before retrying."}}); err != nil {
		cancel()
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	s.wg.Add(3)
	go s.runJobs()
	go s.runPush()
	go s.runRules()
	return s, nil
}

// Acquire shares one serialized command connection across browser and MCP requests.
func (s *Service) Acquire(ctx context.Context, owner string) (*Connection, func(), error) {
	s.mu.Lock()
	entry := s.sessions[owner]
	if entry == nil {
		entry = &session{gate: make(chan struct{}, 1)}
		s.sessions[owner] = entry
	}
	s.mu.Unlock()
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, nil, s.ctx.Err()
	}
	release := sync.OnceFunc(func() { <-entry.gate })
	address, password, err := s.credentials(ctx, owner)
	if err != nil {
		release()
		return nil, nil, err
	}
	fingerprint := sha256.Sum256([]byte(address + "\x00" + password))
	if entry.connection != nil && (entry.fingerprint != fingerprint || time.Since(entry.used) > 4*time.Minute || entry.connection.Mail.Closed()) {
		_ = entry.connection.Mail.Close()
		entry.connection = nil
	}
	if entry.connection == nil {
		connection, err := s.dialCommand(ctx, address, password)
		if err != nil {
			release()
			return nil, nil, err
		}
		entry.connection = connection
		entry.fingerprint = fingerprint
	}
	entry.used = time.Now()
	return entry.connection, release, nil
}

// Credential checks borrow the command slot instead of opening a third IMAP session.
func (s *Service) ValidateAccount(ctx context.Context, owner, address, password string) error {
	s.mu.Lock()
	entry := s.sessions[owner]
	if entry == nil {
		entry = &session{gate: make(chan struct{}, 1)}
		s.sessions[owner] = entry
	}
	s.mu.Unlock()
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-entry.gate }()
	if entry.connection != nil {
		_ = entry.connection.Mail.Close()
		entry.connection = nil
	}
	client, err := imap.NewClient(address, password)
	if err != nil {
		return err
	}
	_, err = client.ListFolders(ctx)
	return errors.Join(err, client.Close())
}

func (s *Service) Invalidate(ctx context.Context, owner string, disconnect bool) error {
	state := s.owner(owner)
	s.mu.Lock()
	state.generation++
	entry := s.sessions[owner]
	m := s.monitors[owner]
	delete(s.monitors, owner)
	s.mu.Unlock()
	if m != nil {
		m.cancel()
		select {
		case <-m.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if entry != nil {
		select {
		case entry.gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if entry.connection != nil {
			_ = entry.connection.Mail.Close()
			entry.connection = nil
		}
		<-entry.gate
	}
	if !disconnect {
		resume, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, err := s.Start(resume, owner)
		return err
	}
	state.write.Lock()
	defer state.write.Unlock()
	for _, collection := range []string{"mail_messages", "mail_folders", "mail_thread_nodes", "mail_changelog", "mail_push_devices", "mail_rule_jobs", "mail_rule_applications"} {
		if _, err := s.db.Collection(collection).DeleteMany(ctx, bson.M{"owner": owner}); err != nil {
			return err
		}
	}
	if _, err := s.db.Collection("mail_sync_status").DeleteOne(ctx, bson.M{"_id": owner}); err != nil {
		return err
	}
	_, err := s.db.Collection("mail_outbox").UpdateMany(ctx, bson.M{"owner": owner, "status": "pending"}, bson.M{"$set": bson.M{"status": "cancelled"}})
	if err != nil {
		return err
	}
	expires := time.Now().Add(14 * 24 * time.Hour)
	_, err = s.db.Collection("mail_moves").UpdateMany(ctx, bson.M{"owner": owner}, bson.M{"$set": bson.M{"state": "cancelled", "expires": expires}, "$unset": bson.M{"next_attempt": ""}})
	if err != nil {
		return err
	}
	_, err = s.db.Collection("mail_folder_ops").UpdateMany(ctx, bson.M{"owner": owner}, bson.M{"$set": bson.M{"state": "cancelled", "expires": expires}})
	return err
}
func (s *Service) Close(ctx context.Context) error {
	s.cancel()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.sessions {
		select {
		case entry.gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if entry.connection != nil {
			_ = entry.connection.Mail.Close()
		}
		<-entry.gate
	}
	err := s.client.Disconnect(ctx)
	if errors.Is(err, mongo.ErrClientDisconnected) {
		return nil
	}
	return err
}

func (s *Service) seal(data []byte) []byte {
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return s.cipher.Seal(nonce, nonce, data, nil)
}
func (s *Service) open(data []byte) ([]byte, error) {
	n := s.cipher.NonceSize()
	if len(data) < n {
		return nil, errors.New("invalid encrypted mail")
	}
	return s.cipher.Open(nil, data[:n], data[n:], nil)
}

func (s *Service) runJobs() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ticks++
			if ticks%60 == 0 {
				if err := s.cleanOutbox(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("clean recovered outbox payloads", "error", err)
				}
			}
			if err := s.runRename(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("mail folder recovery", "error", err)
			}
			if err := s.deliverNext(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("mail outbox worker", "error", err)
			}
			if err := s.runMove(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("mail relocation worker", "error", err)
			}
		}
	}
}
