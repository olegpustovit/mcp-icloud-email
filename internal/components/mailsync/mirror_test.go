package mailsync

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func testMirror(t *testing.T) *Service {
	t.Helper()
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	s, err := newService(context.Background(), uri, base64.StdEncoding.EncodeToString(make([]byte, 32)), func(context.Context, string) (string, string, error) {
		return "mirror@icloud.com", "test-password", nil
	}, "mail_sync_test_"+auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.cancel()
		s.wg.Wait()
		_ = s.db.Drop(context.Background())
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

type syncFake struct {
	mu              sync.Mutex
	mail            map[uint32]imap.Email
	validity        uint32
	modseq          uint64
	next            uint32
	notify          func()
	closed          chan struct{}
	once            sync.Once
	names           []string
	resetAfterFetch bool
}

func (f *syncFake) Folders() ([]imap.FolderInfo, error) {
	if f.names != nil {
		rows := make([]imap.FolderInfo, len(f.names))
		for i, name := range f.names {
			rows[i] = imap.FolderInfo{Name: name, Role: map[string]string{"Drafts": "drafts"}[name]}
		}
		return rows, nil
	}
	return []imap.FolderInfo{{Name: "INBOX", Role: "inbox"}}, nil
}
func (f *syncFake) Status(folder string) (imap.FolderInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	unread := uint32(0)
	for _, m := range f.mail {
		if m.Unread {
			unread++
		}
	}
	return imap.FolderInfo{Name: folder, Role: "inbox", UIDValidity: f.validity, UIDNext: f.next, HighestModSeq: f.modseq, Total: uint32(len(f.mail)), Unread: unread}, nil
}
func (f *syncFake) UIDs(string) ([]uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	uids := make([]uint32, 0, len(f.mail))
	for uid := range f.mail {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	return uids, nil
}
func (f *syncFake) Fetch(folder string, uids []uint32, since uint64, flagsOnly bool) ([]imap.Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mail := []imap.Email{}
	for _, uid := range uids {
		if m, ok := f.mail[uid]; ok && (since == 0 || m.ModSeq > since) {
			m.UIDValidity = f.validity
			m.Folder = folder
			if flagsOnly {
				m = imap.Email{ID: m.ID, UIDValidity: m.UIDValidity, Unread: m.Unread, FlagColor: m.FlagColor, Flags: m.Flags, ModSeq: m.ModSeq}
			}
			mail = append(mail, m)
		}
	}
	if f.resetAfterFetch {
		f.validity++
		f.resetAfterFetch = false
	}
	return mail, nil
}

func TestReconcileRejectsGenerationChangeAndRemovesDeletedFolders(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	m := &monitor{owner: "owner", account: "account", ctx: ctx, folders: map[string]Folder{}, ready: make(chan struct{})}
	f := &syncFake{mail: map[uint32]imap.Email{1: {ID: "1", Subject: "old", Date: time.Now()}}, validity: 7, next: 2, modseq: 1, resetAfterFetch: true}
	if err := s.reconcileFolder(m, f, "INBOX", false, "imap", ""); err == nil {
		t.Fatal("committed FETCH across a UIDVALIDITY reset")
	}
	count, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"owner": m.owner})
	if count != 0 {
		t.Fatal("mixed mailbox generations were persisted")
	}
	if err := s.reconcileFolder(m, f, "INBOX", false, "imap", ""); err != nil {
		t.Fatal(err)
	}
	f.names = []string{}
	if err := s.refreshFolders(m, f, refreshRequest{actor: "web"}); err != nil {
		t.Fatal(err)
	}
	count, _ = s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"owner": m.owner})
	boxes, err := s.Folders(ctx, m.owner, m.account)
	if err != nil || count != 0 || len(boxes) != 0 || len(m.order) != 0 || len(m.folders) != 0 {
		t.Fatalf("deleted folder survived: messages=%d folders=%d err=%v", count, len(boxes), err)
	}
}
func (f *syncFake) Quota() (*imap.StorageQuota, error) {
	return &imap.StorageQuota{Used: 1024, Limit: 2048}, nil
}
func (f *syncFake) CanIdle() bool                    { return false }
func (f *syncFake) CondStore() bool                  { return true }
func (f *syncFake) StartIdle() (func() error, error) { panic("fake does not advertise IDLE") }
func (f *syncFake) Closed() <-chan struct{}          { return f.closed }
func (f *syncFake) Close() error                     { f.once.Do(func() { close(f.closed) }); return nil }
func (f *syncFake) change(fn func()) {
	f.mu.Lock()
	fn()
	notify := f.notify
	f.mu.Unlock()
	if notify != nil {
		notify()
	}
}
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("active mirror did not reach the expected state")
}

func TestMirrorImportDeltaExpungeAndUIDReset(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	owner := "owner@olegpustovit.com"
	account := "mirror@icloud.com"
	fake := &syncFake{mail: map[uint32]imap.Email{}, validity: 7, modseq: 1, next: 651, closed: make(chan struct{})}
	for uid := uint32(1); uid <= 650; uid++ {
		fake.mail[uid] = imap.Email{ID: fmt.Sprint(uid), From: "sender@example.com", Subject: fmt.Sprintf("Message %d", uid), Date: time.Now().Add(-time.Duration(uid) * time.Minute), Unread: true, ModSeq: 1, MessageID: fmt.Sprintf("message-%d@example.com", uid), Snippet: "A real preview"}
	}
	s.dialSync = func(_ context.Context, _, _ string, notify func()) (syncTransport, error) {
		fake.mu.Lock()
		fake.notify = notify
		fake.mu.Unlock()
		return fake, nil
	}
	mailbox, err := s.Mailbox(ctx, owner, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WaitReady(ctx, owner); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		status, _ := s.Status(ctx, owner, account)
		return status.Phase == "live" && status.Imported == 650
	})
	rows, total, err := mailbox.SearchEmails(ctx, "INBOX", "", imap.EmailFilters{Limit: 50})
	if err != nil || total != 650 || len(rows) != 50 {
		t.Fatalf("mirror search: %d rows, total=%d err=%v", len(rows), total, err)
	}
	if rows[0].Folder != "INBOX" || rows[0].UIDValidity != 7 || rows[0].ThreadID == "" {
		t.Fatal("metadata lost folder, generation or conversation identity")
	}
	if count, err := mailbox.CountEmails(ctx, "INBOX", imap.EmailFilters{UnreadOnly: true}); err != nil || count != 650 {
		t.Fatalf("unread count=%d err=%v", count, err)
	}
	cursor, err := s.Cursor(ctx, owner, account)
	if err != nil {
		t.Fatal(err)
	}
	fake.change(func() {
		m := fake.mail[650]
		m.FlagColor = "blue"
		m.Flags = []string{`\Flagged`, "$MailFlagBit2"}
		m.ModSeq = 2
		fake.mail[650] = m
		fake.modseq = 2
	})
	eventually(t, func() bool {
		var m Metadata
		err := s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": messageKey(owner, account, "INBOX", 7, "650")}).Decode(&m)
		return err == nil && m.FlagColor == "blue"
	})
	var changes []Change
	eventually(t, func() bool {
		changes, err = s.Changes(ctx, owner, account, cursor)
		if err != nil {
			return false
		}
		for _, change := range changes {
			if change.Message != nil && change.Message.ID == "650" && change.Message.FlagColor == "blue" {
				return true
			}
		}
		return false
	})
	found := false
	previous := cursor
	for _, change := range changes {
		if change.Sequence <= previous {
			t.Fatal("sync IDs are not ordered")
		}
		previous = change.Sequence
		if change.Message != nil && change.Message.ID == "650" && change.Message.FlagColor == "blue" {
			found = true
		}
	}
	if !found {
		t.Fatal("flag delta was not journaled")
	}
	if err = s.change(ctx, owner, "mcp:test-client", "agent-tx", []string{"INBOX"}, func() error {
		fake.change(func() {
			mail := fake.mail[650]
			mail.Unread = false
			mail.ModSeq = 3
			fake.mail[650] = mail
			fake.modseq = 3
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	activity, err := s.Activity(ctx, owner, account)
	index := slices.IndexFunc(activity, func(change Change) bool { return change.Entity == "msg" })
	if err != nil || index < 0 || activity[index].Actor != "mcp:test-client" || activity[index].Before == nil || activity[index].Message.Unread || activity[index].Message.Actor != "mcp:test-client" {
		t.Fatalf("mutation attribution lost to IDLE wake: %+v err=%v", activity, err)
	}
	if ack, err := s.TransactionCommitted(ctx, owner, account, "agent-tx"); err != nil || ack == 0 {
		t.Fatal("mutation receipt not persisted")
	}
	if activity, err := s.Activity(ctx, "other-owner", account); err != nil || len(activity) != 0 {
		t.Fatal("agent activity leaked across owners")
	}
	fake.change(func() {
		delete(fake.mail, 1)
		fake.mail[900] = imap.Email{ID: "900", From: "new@example.com", Subject: "New mail", MessageID: "new@example.com", Date: time.Now(), Unread: true, ModSeq: 3}
		fake.next = 901
		fake.modseq = 4
	})
	eventually(t, func() bool {
		old, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"_id": messageKey(owner, account, "INBOX", 7, "1")})
		fresh, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"_id": messageKey(owner, account, "INBOX", 7, "900")})
		count, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"owner": owner})
		return old == 0 && fresh == 1 && count == 650
	})
	fake.change(func() {
		fake.validity = 88
		fake.next = 2
		fake.modseq = 1
		fake.mail = map[uint32]imap.Email{1: {ID: "1", Subject: "A different message after reset", MessageID: "reset@example.com", Date: time.Now(), ModSeq: 1}}
	})
	eventually(t, func() bool {
		count, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"owner": owner})
		fresh, _ := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"_id": messageKey(owner, account, "INBOX", 88, "1")})
		return count == 1 && fresh == 1
	})
	_, _, other, _, err := s.Bootstrap(ctx, "other@olegpustovit.com", account)
	if err != nil || len(other) != 0 {
		t.Fatal("bootstrap leaked another owner's mailbox")
	}
}

func TestJournalRecoveryAndThreading(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	owner, account := "thread-owner", "mirror@icloud.com"
	mail := []imap.Email{
		{ID: "3", Folder: "INBOX", UIDValidity: 7, Subject: "Re: Review", MessageID: "child@example.com", References: []string{"parent@example.com"}, Date: time.Now(), BodyPlain: "not stored", BodyHTML: "not stored"},
		{ID: "2", Folder: "Sent Messages", UIDValidity: 7, Subject: "Re: Review", MessageID: "parent@example.com", References: []string{"root@example.com"}, Date: time.Now()},
		{ID: "1", Folder: "Archive", UIDValidity: 7, Subject: "Review", MessageID: "root@example.com", Date: time.Now()},
		{ID: "4", Folder: "INBOX", UIDValidity: 7, Subject: "Re: Review", MessageID: "cycle@example.com", References: []string{"child@example.com", "cycle@example.com"}, Date: time.Now()},
	}
	prepared, merges, err := s.prepareBatch(ctx, owner, account, mail)
	if err != nil {
		t.Fatal(err)
	}
	thread := prepared[0].ThreadID
	for _, message := range prepared {
		if message.ThreadID != thread {
			t.Fatal("child-before-parent or a reference cycle split the conversation")
		}
		if message.BodyPlain != "" || message.BodyHTML != "" {
			t.Fatal("mirror stored message bodies")
		}
	}
	changes := merges
	for _, message := range prepared {
		copy := message
		changes = append(changes, Change{Entity: "msg", Op: "put", Key: copy.Key, Message: &copy})
	}
	if err = s.commit(ctx, owner, account, "mcp:client", "transaction-1", changes); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Thread(ctx, owner, account, thread)
	if err != nil || len(stored) != 4 {
		t.Fatalf("thread size=%d err=%v", len(stored), err)
	}
	cursor, _ := s.Cursor(ctx, owner, account)
	crash := Change{ID: identity(owner, "crash"), Owner: owner, Account: account, Sequence: cursor + 1, Entity: "msg", Op: "put", Key: prepared[0].Key, Message: &prepared[0], Actor: "web", At: time.Now(), Committed: false}
	if _, err = s.db.Collection("mail_changelog").InsertOne(ctx, crash); err != nil {
		t.Fatal(err)
	}
	if err = s.recoverOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err = s.recoverOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var recovered Change
	if err = s.db.Collection("mail_changelog").FindOne(ctx, bson.M{"_id": crash.ID}).Decode(&recovered); err != nil || !recovered.Committed {
		t.Fatal("unfinished journal entry was not recovered")
	}
	if leaked, err := s.Changes(ctx, "another-owner", account, 0); err != nil || len(leaked) != 0 {
		t.Fatal("changelog leaked another owner's changes")
	}
	if _, err = searchConditions(`from:sender has:attachment after:not-a-date`); err == nil {
		t.Fatal("accepted malformed search date")
	}
}

func TestDisconnectInvalidatesInFlightCredentialLookup(t *testing.T) {
	s := testMirror(t)
	calls := 0
	s.credentials = func(ctx context.Context, owner string) (string, string, error) {
		calls++
		if calls == 1 {
			if err := s.Invalidate(ctx, owner, true); err != nil {
				t.Fatal(err)
			}
			return "old@icloud.com", "old-password", nil
		}
		return "", "", fmt.Errorf("account was disconnected")
	}
	if _, err := s.Start(context.Background(), "owner"); err == nil {
		t.Fatal("a lookup started before unlink resurrected the disconnected mailbox")
	}
	s.mu.Lock()
	count := len(s.monitors)
	s.mu.Unlock()
	if count != 0 || calls != 2 {
		t.Fatalf("monitors=%d credential calls=%d", count, calls)
	}
}

func TestDraftSaveRefreshesOnlyDrafts(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	fake := &syncFake{mail: map[uint32]imap.Email{}, validity: 7, modseq: 1, next: 1, closed: make(chan struct{}), names: []string{"INBOX", "Drafts", "Archive"}}
	s.dialSync = func(_ context.Context, _, _ string, notify func()) (syncTransport, error) { return fake, nil }
	mailbox, err := s.Mailbox(ctx, "owner@olegpustovit.com", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WaitReady(ctx, "owner@olegpustovit.com"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return slices.Equal(mailbox.roleFolders(ctx, "drafts"), []string{"Drafts"}) })
	if got := mailbox.roleFolders(ctx, "junk"); got != nil {
		t.Fatalf("missing role must fall back to a full refresh, got %v", got)
	}
}
