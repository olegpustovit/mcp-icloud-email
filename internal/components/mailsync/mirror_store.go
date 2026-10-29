package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Metadata struct {
	Key             string `bson:"_id" json:"key"`
	Owner           string `bson:"owner" json:"-"`
	Account         string `bson:"account" json:"-"`
	imap.Email      `bson:"email"`
	SubjectKey      string     `bson:"subject_key" json:"-"`
	Fingerprint     string     `bson:"fingerprint" json:"-"`
	Version         int64      `bson:"sync_id" json:"syncId"`
	Actor           string     `bson:"actor" json:"actor"`
	SentBy          string     `bson:"sent_by,omitempty" json:"sentBy,omitempty"`
	Snoozed         bool       `bson:"snoozed,omitempty" json:"snoozed,omitempty"`
	SnoozeUntil     *time.Time `bson:"snooze_until,omitempty" json:"snoozeUntil,omitempty"`
	SnoozeError     string     `bson:"snooze_error,omitempty" json:"snoozeError,omitempty"`
	MoveTransaction string     `bson:"move_transaction,omitempty" json:"moveTransaction,omitempty"`
	MoveActor       string     `bson:"move_actor,omitempty" json:"moveActor,omitempty"`
}
type Folder struct {
	Key             string `bson:"_id" json:"key"`
	Owner           string `bson:"owner" json:"-"`
	Account         string `bson:"account" json:"-"`
	imap.FolderInfo `bson:"info"`
	Imported        int64     `bson:"imported" json:"imported"`
	BeforeUID       uint32    `bson:"before_uid" json:"-"`
	Ready           bool      `bson:"ready" json:"ready"`
	Complete        bool      `bson:"complete" json:"complete"`
	FullFlagSync    time.Time `bson:"full_flag_sync" json:"-"`
	Version         int64     `bson:"sync_id" json:"syncId"`
}
type SyncStatus struct {
	Owner    string             `bson:"_id" json:"-"`
	Account  string             `bson:"account" json:"-"`
	Phase    string             `bson:"phase" json:"phase"`
	Mode     string             `bson:"mode" json:"mode"`
	Ready    bool               `bson:"ready" json:"ready"`
	Imported int64              `bson:"imported" json:"imported"`
	Total    int64              `bson:"total" json:"total"`
	Error    string             `bson:"error" json:"error,omitempty"`
	Quota    *imap.StorageQuota `bson:"quota,omitempty" json:"quota,omitempty"`
	At       time.Time          `bson:"at" json:"at"`
}
type Change struct {
	ID              string        `bson:"_id" json:"-"`
	Owner           string        `bson:"owner" json:"-"`
	Account         string        `bson:"account" json:"-"`
	Sequence        int64         `bson:"seq" json:"syncId"`
	Entity          string        `bson:"entity" json:"entity"`
	Op              string        `bson:"op" json:"op"`
	Key             string        `bson:"key" json:"key"`
	Message         *Metadata     `bson:"message,omitempty" json:"message,omitempty"`
	Before          *Metadata     `bson:"before,omitempty" json:"before,omitempty"`
	MoveTransaction string        `bson:"move_transaction,omitempty" json:"moveTransaction,omitempty"`
	Folder          *Folder       `bson:"folder,omitempty" json:"folder,omitempty"`
	Status          *SyncStatus   `bson:"status,omitempty" json:"status,omitempty"`
	Preferences     *Preferences  `bson:"preferences,omitempty" json:"preferences,omitempty"`
	Rename          *FolderRename `bson:"rename,omitempty" json:"rename,omitempty"`
	Arrival         bool          `bson:"arrival,omitempty" json:"arrival,omitempty"`
	RuleJob         *RuleJob      `bson:"rule_job,omitempty" json:"-"`
	Actor           string        `bson:"actor" json:"actor"`
	TransactionID   string        `bson:"tx_id,omitempty" json:"txId,omitempty"`
	At              time.Time     `bson:"at" json:"at"`
	Committed       bool          `bson:"committed" json:"-"`
}
type ownerState struct {
	generation uint64
	viewers    int
	active     time.Time
	write      sync.Mutex
	changed    chan struct{}
}

func identity(parts ...string) string { raw, _ := json.Marshal(parts); return auth.Hash(string(raw)) }
func messageKey(owner, account, folder string, validity uint32, uid string) string {
	return identity(owner, account, folder, fmt.Sprint(validity), uid)
}
func (s *Service) owner(owner string) *ownerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.owners[owner]
	if state == nil {
		state = &ownerState{changed: make(chan struct{})}
		s.owners[owner] = state
	}
	return state
}
func (s *Service) Changed(owner string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.owners[owner]
	if state == nil {
		state = &ownerState{changed: make(chan struct{})}
		s.owners[owner] = state
	}
	return state.changed
}
func (s *Service) notify(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.owners[owner]; state != nil {
		close(state.changed)
		state.changed = make(chan struct{})
	}
}
func (s *Service) Done() <-chan struct{} { return s.ctx.Done() }

// The journal is written before the mirror, then marked visible after applying it.
// Replaying unfinished entries repairs a crash without requiring a Mongo replica set.
func (s *Service) commit(ctx context.Context, owner, account, actor, txID string, changes []Change) error {
	if len(changes) == 0 {
		return nil
	}
	state := s.owner(owner)
	state.write.Lock()
	defer state.write.Unlock()
	return s.commitLocked(ctx, owner, account, actor, txID, changes)
}
func (s *Service) commitLocked(ctx context.Context, owner, account, actor, txID string, changes []Change) error {
	if err := s.recoverOwner(ctx, owner); err != nil {
		return err
	}
	var counter struct {
		Value int64 `bson:"value"`
	}
	err := s.db.Collection("mail_sync_counters").FindOneAndUpdate(ctx, bson.M{"_id": owner}, bson.M{"$inc": bson.M{"value": int64(len(changes))}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&counter)
	if err != nil {
		return fmt.Errorf("allocate mail sync IDs: %w", err)
	}
	docs := make([]any, len(changes))
	var relocation Move
	if txID != "" {
		err = s.db.Collection("mail_moves").FindOne(ctx, bson.M{"_id": identity(owner, account, txID), "owner": owner, "account": account, "state": bson.M{"$in": []string{"moved", "snoozed"}}}).Decode(&relocation)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
	}
	first := counter.Value - int64(len(changes)) + 1
	for i := range changes {
		change := &changes[i]
		change.Owner = owner
		change.Account = account
		change.Sequence = first + int64(i)
		change.ID = identity(owner, fmt.Sprint(change.Sequence))
		change.Actor = actor
		change.TransactionID = txID
		change.At = time.Now().UTC()
		change.Committed = false
		if change.Op == "del" && change.Before != nil && relocation.ID != "" && change.Before.Folder == relocation.From.Folder && change.Before.ID == relocation.From.ID && change.Before.UIDValidity == relocation.From.Validity {
			change.MoveTransaction = relocation.Transaction
		}
		if change.Message != nil {
			copy := *change.Message
			fresh := copy.Version == 0
			copy.Version = change.Sequence
			copy.Actor = actor
			if change.Arrival && copy.MoveActor == "" {
				copy.Actor, change.Actor, change.TransactionID = "imap", "imap", ""
			}
			if fresh && strings.HasPrefix(actor, "mcp:") && (strings.EqualFold(copy.Folder, "Sent Messages") || strings.EqualFold(copy.Folder, "Sent")) {
				copy.SentBy = actor
			}
			if fresh && copy.MoveActor != "" {
				copy.Actor = copy.MoveActor
				change.Actor = copy.MoveActor
				change.MoveTransaction = copy.MoveTransaction
			}
			change.Message = &copy
		}
		if change.Folder != nil {
			copy := *change.Folder
			copy.Version = change.Sequence
			change.Folder = &copy
		}
		docs[i] = *change
	}
	if _, err = s.db.Collection("mail_changelog").InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("write mail changelog: %w", err)
	}
	if err = s.applyChanges(ctx, changes); err != nil {
		return err
	}
	_, err = s.db.Collection("mail_changelog").UpdateMany(ctx, bson.M{"owner": owner, "seq": bson.M{"$gte": first, "$lte": counter.Value}}, bson.M{"$set": bson.M{"committed": true}})
	if err != nil {
		return fmt.Errorf("commit mail changelog: %w", err)
	}
	s.notify(owner)
	return nil
}
func (s *Service) recoverOwner(ctx context.Context, owner string) error {
	filter := bson.M{"committed": false}
	if owner != "" {
		filter["owner"] = owner
	}
	rows, err := s.db.Collection("mail_changelog").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "owner", Value: 1}, {Key: "seq", Value: 1}}))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close(ctx) }()
	var pending []Change
	if err = rows.All(ctx, &pending); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if err = s.applyChanges(ctx, pending); err != nil {
		return fmt.Errorf("recover mail journal: %w", err)
	}
	ids := make([]string, len(pending))
	for i, c := range pending {
		ids[i] = c.ID
	}
	_, err = s.db.Collection("mail_changelog").UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"$set": bson.M{"committed": true}})
	return err
}
func (s *Service) applyChanges(ctx context.Context, changes []Change) error {
	var models []mongo.WriteModel
	collection := ""
	flush := func() error {
		if len(models) == 0 {
			return nil
		}
		_, err := s.db.Collection(collection).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(true))
		models = nil
		return err
	}
	for _, change := range changes {
		if change.Entity == "rule-job" {
			if err := flush(); err != nil {
				return err
			}
			if change.RuleJob == nil || change.RuleJob.Owner != change.Owner || change.RuleJob.Account != change.Account {
				return errors.New("invalid rule job journal scope")
			}
			if err := s.insertRuleJobs(ctx, []RuleJob{*change.RuleJob}); err != nil {
				return err
			}
			continue
		}
		if change.Entity == "tx" || change.Entity == "rename" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		name := map[string]string{"msg": "mail_messages", "folder": "mail_folders", "status": "mail_sync_status", "pref": "mail_preferences"}[change.Entity]
		if name == "" {
			return fmt.Errorf("unknown mail journal entity %q", change.Entity)
		}
		if name != collection || change.Op == "reset" {
			if err := flush(); err != nil {
				return err
			}
			collection = name
		}
		filter := bson.M{"_id": change.Key, "owner": change.Owner, "account": change.Account}
		if change.Entity == "status" || change.Entity == "pref" {
			filter = bson.M{"_id": change.Owner}
		}
		if change.Op == "reset" {
			if change.Folder == nil {
				return errors.New("mail folder reset has no folder")
			}
			_, err := s.db.Collection("mail_messages").DeleteMany(ctx, bson.M{"owner": change.Owner, "account": change.Account, "email.folder": change.Folder.Name})
			if err != nil {
				return err
			}
		}
		if change.Op == "del" {
			models = append(models, mongo.NewDeleteOneModel().SetFilter(filter))
			continue
		}
		var value any
		switch change.Entity {
		case "msg":
			if change.Message == nil || change.Message.Owner != change.Owner || change.Message.Account != change.Account {
				return errors.New("invalid mail message journal scope")
			}
			value = change.Message
		case "folder":
			if change.Folder == nil || change.Folder.Owner != change.Owner || change.Folder.Account != change.Account {
				return errors.New("invalid folder journal scope")
			}
			value = change.Folder
		case "status":
			if change.Status == nil || change.Status.Owner != change.Owner || change.Status.Account != change.Account {
				return errors.New("invalid status journal scope")
			}
			value = change.Status
		case "pref":
			if change.Preferences == nil {
				return errors.New("invalid preferences journal entry")
			}
			value = struct {
				ID          string `bson:"_id"`
				Preferences `bson:",inline"`
			}{change.Owner, *change.Preferences}
		}
		models = append(models, mongo.NewReplaceOneModel().SetFilter(filter).SetReplacement(value).SetUpsert(true))
	}
	return flush()
}

func (s *Service) acknowledge(ctx context.Context, owner, account, actor, txID string) error {
	if txID == "" {
		return nil
	}
	return s.commit(ctx, owner, account, actor, txID, []Change{{Entity: "tx", Op: "ack", Key: txID}})
}
func (s *Service) TransactionCommitted(ctx context.Context, owner, account, txID string) (int64, error) {
	var change Change
	err := s.db.Collection("mail_changelog").FindOne(ctx, bson.M{"owner": owner, "account": account, "tx_id": txID, "entity": "tx", "committed": true}).Decode(&change)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	return change.Sequence, err
}
func (s *Service) Cursor(ctx context.Context, owner, account string) (int64, error) {
	var last Change
	err := s.db.Collection("mail_changelog").FindOne(ctx, bson.M{"owner": owner, "account": account, "committed": true}, options.FindOne().SetSort(bson.D{{Key: "seq", Value: -1}})).Decode(&last)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	return last.Sequence, err
}
func (s *Service) Changes(ctx context.Context, owner, account string, cursor int64) ([]Change, error) {
	rows, err := s.db.Collection("mail_changelog").Find(ctx, bson.M{"owner": owner, "account": account, "committed": true, "seq": bson.M{"$gt": cursor}}, options.Find().SetSort(bson.D{{Key: "seq", Value: 1}}).SetLimit(1000))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	changes := []Change{}
	err = rows.All(ctx, &changes)
	return changes, err
}

func (s *Service) HistoryMissing(ctx context.Context, owner, account string, cursor int64) (bool, error) {
	if cursor == 0 {
		return false, nil
	}
	var first Change
	err := s.db.Collection("mail_changelog").FindOne(ctx, bson.M{"owner": owner, "account": account, "committed": true}, options.FindOne().SetSort(bson.D{{Key: "seq", Value: 1}})).Decode(&first)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	last, err := s.Cursor(ctx, owner, account)
	return cursor < first.Sequence-1 || cursor > last, err
}
func (s *Service) Folders(ctx context.Context, owner, account string) ([]Folder, error) {
	rows, err := s.db.Collection("mail_folders").Find(ctx, bson.M{"owner": owner, "account": account}, options.Find().SetSort(bson.D{{Key: "info.name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	folders := []Folder{}
	err = rows.All(ctx, &folders)
	return folders, err
}
func (s *Service) Status(ctx context.Context, owner, account string) (SyncStatus, error) {
	var status SyncStatus
	err := s.db.Collection("mail_sync_status").FindOne(ctx, bson.M{"_id": owner, "account": account}).Decode(&status)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return SyncStatus{Owner: owner, Account: account, Phase: "connecting"}, nil
	}
	return status, err
}
func (s *Service) metadata(ctx context.Context, owner, account string, filter bson.M, limit int64) ([]Metadata, error) {
	filter["owner"] = owner
	filter["account"] = account
	find := options.Find().SetSort(bson.D{{Key: "email.date", Value: -1}, {Key: "_id", Value: 1}})
	if limit > 0 {
		find.SetLimit(limit)
	}
	rows, err := s.db.Collection("mail_messages").Find(ctx, filter, find)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	mail := []Metadata{}
	err = rows.All(ctx, &mail)
	return mail, err
}
func (s *Service) Bootstrap(ctx context.Context, owner, account string) (int64, []Folder, []Metadata, SyncStatus, error) {
	cursor, err := s.Cursor(ctx, owner, account)
	if err != nil {
		return 0, nil, nil, SyncStatus{}, err
	}
	folders, err := s.Folders(ctx, owner, account)
	if err != nil {
		return 0, nil, nil, SyncStatus{}, err
	}
	messages, err := s.metadata(ctx, owner, account, bson.M{}, 2000)
	if err != nil {
		return 0, nil, nil, SyncStatus{}, err
	}
	status, err := s.Status(ctx, owner, account)
	return cursor, folders, messages, status, err
}

func (s *Service) History(ctx context.Context, owner, account string, before time.Time, key string) ([]Metadata, error) {
	filter := bson.M{"owner": owner, "account": account}
	if !before.IsZero() {
		filter["$or"] = []bson.M{{"email.date": bson.M{"$lt": before}}, {"email.date": before, "_id": bson.M{"$gt": key}}}
	}
	rows, err := s.db.Collection("mail_messages").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "email.date", Value: -1}, {Key: "_id", Value: 1}}).SetLimit(2000))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	mail := []Metadata{}
	err = rows.All(ctx, &mail)
	return mail, err
}

func normalizeSubject(subject string) string {
	text := strings.TrimSpace(strings.ToLower(subject))
	for {
		old := text
		for _, prefix := range []string{"re:", "fw:", "fwd:"} {
			text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
		if old == text {
			return strings.Join(strings.Fields(text), " ")
		}
	}
}
