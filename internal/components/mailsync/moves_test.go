package mailsync

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/tools"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type moveWire struct {
	tools.EmailService
	mu                      sync.Mutex
	mail                    map[string]map[string]imap.Email
	next                    map[string]uint32
	notify                  func()
	moves                   int
	lostResponse, failClear bool
	lostRenameResponse      bool
	renames                 int
}

func (f *moveWire) SearchPage(context.Context, string, string, imap.EmailFilters) (imap.SearchPage, error) {
	return imap.SearchPage{}, errors.New("search is not configured")
}
func (f *moveWire) Closed() bool { return false }
func (f *moveWire) Close() error { return nil }
func (f *moveWire) ListFolders(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.mail {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (f *moveWire) UIDValidity(_ context.Context, folder string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mail[folder]; !ok {
		return 0, errors.New("mailbox missing")
	}
	return 7, nil
}
func (f *moveWire) CreateFolder(_ context.Context, name, parent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mail[name]; ok {
		return errors.New("exists")
	}
	f.mail[name] = map[string]imap.Email{}
	f.next[name] = 1
	return nil
}
func (f *moveWire) RenameFolder(_ context.Context, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mail[from]; !ok {
		return errors.New("missing")
	}
	if _, ok := f.mail[to]; ok {
		return errors.New("exists")
	}
	f.renames++
	for name, mail := range f.mail {
		if name == from || strings.HasPrefix(name, from+"/") {
			next := to + strings.TrimPrefix(name, from)
			f.mail[next] = mail
			f.next[next] = f.next[name]
			delete(f.mail, name)
			delete(f.next, name)
		}
	}
	if f.lostRenameResponse {
		f.lostRenameResponse = false
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (f *moveWire) Relocate(_ context.Context, from imap.Location, to, marker string) (imap.Location, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, mail := range f.mail[to] {
		if slices.Contains(mail.Flags, marker) {
			return imap.Location{Folder: to, ID: id, Validity: 7}, nil
		}
	}
	if from.Validity != 7 {
		return imap.Location{}, imap.ErrLocationChanged
	}
	mail, ok := f.mail[from.Folder][from.ID]
	if !ok {
		return imap.Location{}, imap.ErrLocationChanged
	}
	if _, ok = f.mail[to]; !ok {
		return imap.Location{}, errors.New("target missing")
	}
	id := from.ID
	if from.Folder != to {
		f.moves++
		delete(f.mail[from.Folder], from.ID)
		id = fmt.Sprint(f.next[to])
		f.next[to]++
	}
	mail.ID = id
	mail.Folder = to
	mail.UIDValidity = 7
	mail.Flags = append(slices.Clone(mail.Flags), marker)
	f.mail[to][id] = mail
	if f.notify != nil {
		f.notify()
	}
	location := imap.Location{Folder: to, ID: id, Validity: 7}
	if f.lostResponse {
		f.lostResponse = false
		return imap.Location{}, io.ErrUnexpectedEOF
	}
	return location, nil
}
func (f *moveWire) ClearMoveMarker(_ context.Context, location imap.Location, marker string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failClear {
		f.failClear = false
		return io.ErrUnexpectedEOF
	}
	mail := f.mail[location.Folder][location.ID]
	mail.Flags = slices.DeleteFunc(slices.Clone(mail.Flags), func(flag string) bool { return flag == marker })
	f.mail[location.Folder][location.ID] = mail
	return nil
}
func (f *moveWire) MarkRead(_ context.Context, folder, id string, read bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	mail, ok := f.mail[folder][id]
	if !ok {
		return imap.ErrLocationChanged
	}
	mail.Unread = !read
	f.mail[folder][id] = mail
	if f.notify != nil {
		f.notify()
	}
	return nil
}

type moveSync struct {
	f      *moveWire
	closed chan struct{}
	once   sync.Once
}

func (w *moveSync) Folders() ([]imap.FolderInfo, error) {
	names, _ := w.f.ListFolders(context.Background())
	slices.SortFunc(names, func(a, b string) int {
		if a == "INBOX" {
			return -1
		}
		if b == "INBOX" {
			return 1
		}
		return strings.Compare(a, b)
	})
	rows := make([]imap.FolderInfo, len(names))
	for i, name := range names {
		rows[i] = imap.FolderInfo{Name: name, Role: map[string]string{"INBOX": "inbox", "Archive": "archive", "Snoozed": "snoozed", "Deleted Messages": "trash"}[name], Delimiter: "/"}
	}
	return rows, nil
}
func (w *moveSync) Status(folder string) (imap.FolderInfo, error) {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	mail, ok := w.f.mail[folder]
	if !ok {
		return imap.FolderInfo{}, errors.New("missing")
	}
	info := imap.FolderInfo{Name: folder, UIDValidity: 7, UIDNext: w.f.next[folder], Total: uint32(len(mail))}
	for _, m := range mail {
		if m.Unread {
			info.Unread++
		}
	}
	return info, nil
}
func (w *moveSync) UIDs(folder string) ([]uint32, error) {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	var uids []uint32
	for id := range w.f.mail[folder] {
		uid, _ := strconv.ParseUint(id, 10, 32)
		uids = append(uids, uint32(uid))
	}
	slices.Sort(uids)
	return uids, nil
}
func (w *moveSync) Fetch(folder string, uids []uint32, _ uint64, flags bool) ([]imap.Email, error) {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	var rows []imap.Email
	for _, uid := range uids {
		if mail, ok := w.f.mail[folder][fmt.Sprint(uid)]; ok {
			mail.Folder = folder
			mail.UIDValidity = 7
			mail.Flags = slices.Clone(mail.Flags)
			rows = append(rows, mail)
		}
	}
	return rows, nil
}
func (w *moveSync) Quota() (*imap.StorageQuota, error) { return nil, nil }
func (w *moveSync) CanIdle() bool                      { return false }
func (w *moveSync) CondStore() bool                    { return false }
func (w *moveSync) StartIdle() (func() error, error)   { panic("not supported") }
func (w *moveSync) Closed() <-chan struct{}            { return w.closed }
func (w *moveSync) Close() error                       { w.once.Do(func() { close(w.closed) }); return nil }
func connectMoveFake(s *Service, f *moveWire) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialCommand = func(_ context.Context, address, _ string) (*Connection, error) {
		return &Connection{Mail: f, Address: address}, nil
	}
	s.dialSync = func(_ context.Context, _, _ string, notify func()) (syncTransport, error) {
		f.mu.Lock()
		f.notify = notify
		f.mu.Unlock()
		return &moveSync{f: f, closed: make(chan struct{})}, nil
	}
}
func readMove(t *testing.T, s *Service, owner, transaction string) Move {
	t.Helper()
	var job Move
	if err := s.db.Collection("mail_moves").FindOne(context.Background(), bson.M{"owner": owner, "transaction": transaction}).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestSnoozeRestartRecoveryMoveUndoAndIsolation(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {"1": {ID: "1", From: "sender@example.com", Subject: "No Message-ID", Date: time.Now(), Unread: false}}, "Archive": {}}, next: map[string]uint32{"INBOX": 2, "Archive": 1}}
	connectMoveFake(s, f)
	owner := "owner"
	box, err := s.Mailbox(ctx, owner, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WaitReady(ctx, owner); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	if err = box.Snooze(Transaction(ctx, "snooze-one"), imap.Location{Folder: "INBOX", ID: "1", Validity: 7}, &until); err != nil {
		t.Fatal(err)
	}
	job := readMove(t, s, owner, "snooze-one")
	if job.State != "snoozed" || job.Current.Folder != "Snoozed" {
		t.Fatalf("not snoozed: %+v", job)
	}
	var metadata Metadata
	if err = s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": messageKey(owner, box.account, "Snoozed", 7, job.Current.ID)}).Decode(&metadata); err != nil || !metadata.Snoozed || metadata.SnoozeUntil == nil {
		t.Fatal("snooze metadata missing", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := newService(ctx, os.Getenv("MAIL_TEST_MONGODB_URI"), base64.StdEncoding.EncodeToString(make([]byte, 32)), s.credentials, s.db.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		resumed.cancel()
		resumed.wg.Wait()
		_ = resumed.db.Drop(context.Background())
		_ = resumed.Close(context.Background())
	})
	connectMoveFake(resumed, f)
	past := time.Now().Add(-time.Minute)
	if _, err = resumed.db.Collection("mail_moves").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"until": past, "next_attempt": past}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return readMove(t, resumed, owner, "snooze-one").State == "awake" })
	box, err = resumed.Mailbox(ctx, owner, "mcp:test-client")
	if err != nil {
		t.Fatal(err)
	}
	job = readMove(t, resumed, owner, "snooze-one")
	f.mu.Lock()
	mail := f.mail["INBOX"][job.Current.ID]
	moves := f.moves
	f.lostResponse = true
	f.mu.Unlock()
	if !mail.Unread || moves != 2 {
		t.Fatalf("restart did not wake once and mark unread: mail=%+v moves=%d", mail, moves)
	}
	if err = box.MoveWithValidity(Transaction(ctx, "move-one"), job.Current, "Archive"); err == nil {
		t.Fatal("lost MOVE response was not simulated")
	}
	if err = box.MoveWithValidity(Transaction(ctx, "move-one"), job.Current, "Archive"); err != nil {
		t.Fatal("lost MOVE response could not be recovered", err)
	}
	f.mu.Lock()
	moves = f.moves
	f.failClear = true
	f.mu.Unlock()
	if moves != 3 {
		t.Fatal("retry moved message twice")
	}
	if err = box.UndoMove(Transaction(ctx, "undo-one"), "move-one"); err == nil {
		t.Fatal("marker cleanup failure was not simulated")
	}
	undo := readMove(t, resumed, owner, "move-one")
	if undo.State != "undone" || !undo.Cleanup {
		t.Fatal("return receipt was not durable before marker cleanup")
	}
	if err = box.UndoMove(Transaction(ctx, "undo-one"), "move-one"); err != nil {
		t.Fatal("cleanup recovery failed", err)
	}
	f.mu.Lock()
	moves = f.moves
	inbox, archive := len(f.mail["INBOX"]), len(f.mail["Archive"])
	f.mu.Unlock()
	if moves != 4 || inbox != 1 || archive != 0 {
		t.Fatalf("undo retry duplicated mail: moves=%d inbox=%d archive=%d", moves, inbox, archive)
	}
	other := &Mailbox{s: resumed, owner: "other-owner", account: box.account, actor: "web"}
	if err = other.UndoMove(ctx, "move-one"); err == nil {
		t.Fatal("cross-owner move undo succeeded")
	}
	undo = readMove(t, resumed, owner, "move-one")
	if err = box.Snooze(Transaction(ctx, "someday"), undo.Current, nil); err != nil {
		t.Fatal(err)
	}
	if err = resumed.runMove(ctx); err != nil {
		t.Fatal(err)
	}
	job = readMove(t, resumed, owner, "someday")
	if job.State != "snoozed" || job.Until != nil || job.NextAttempt != nil {
		t.Fatal("Someday was scheduled automatically")
	}
	stale := job.Current
	stale.Validity = 99
	if _, err = resumed.db.Collection("mail_moves").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"current": stale, "until": past, "next_attempt": past}}); err != nil {
		t.Fatal(err)
	}
	if err = resumed.runMove(ctx); err == nil {
		t.Fatal("wake ignored a changed mailbox identity")
	}
	if err = resumed.db.Collection("mail_messages").FindOne(ctx, bson.M{"owner": owner, "email.flags": job.Marker}).Decode(&metadata); err != nil || metadata.SnoozeError == "" {
		t.Fatal("background wake failure was not visible in the replica", err)
	}
	if err = box.Unsnooze(Transaction(ctx, "unsnooze"), job.Current); err != nil {
		t.Fatal(err)
	}
	job = readMove(t, resumed, owner, "unsnooze")
	if job.Current.Folder != "INBOX" {
		t.Fatal("manual unsnooze failed")
	}
	if err = box.CreateFolder(ctx, "Clients", ""); err != nil {
		t.Fatal(err)
	}
	if err = box.CreateFolder(ctx, "Clients/Child", ""); err != nil {
		t.Fatal(err)
	}
	if err = box.MoveWithValidity(Transaction(ctx, "nested-move"), job.Current, "Clients/Child"); err != nil {
		t.Fatal(err)
	}
	if err = box.RenameFolder(ctx, "Clients", "Work"); err != nil {
		t.Fatal(err)
	}
	if moved := readMove(t, resumed, owner, "nested-move"); moved.Current.Folder != "Work/Child" {
		t.Fatal("rename orphaned a nested move receipt")
	}
	activity, err := resumed.Activity(ctx, owner, box.account)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(activity, func(change Change) bool { return change.MoveTransaction == "nested-move" })
	if index < 0 {
		t.Fatal("agent move did not have an undo receipt in activity")
	}
	if err = resumed.UndoActivity(ctx, owner, box.account, activity[index].Sequence); err != nil {
		t.Fatal("rename broke move undo", err)
	}
	nested := readMove(t, resumed, owner, "nested-move")
	external, err := f.Relocate(ctx, nested.Current, "Snoozed", "$TerminalMail_external")
	if err != nil {
		t.Fatal(err)
	}
	if err = resumed.Refresh(ctx, owner, "imap", ""); err != nil {
		t.Fatal(err)
	}
	if err = box.Snooze(Transaction(ctx, "adopt-snoozed"), external, nil); err != nil {
		t.Fatal("could not adopt mail placed in Snoozed by another client", err)
	}
	if err = box.Unsnooze(Transaction(ctx, "adopt-return"), external); err != nil {
		t.Fatal(err)
	}
	if adopted := readMove(t, resumed, owner, "adopt-snoozed"); adopted.Current.Folder != "INBOX" {
		t.Fatal("adopted Snoozed mail did not return to Inbox")
	}
}
