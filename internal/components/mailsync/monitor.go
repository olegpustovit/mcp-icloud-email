package mailsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type syncTransport interface {
	Folders() ([]imap.FolderInfo, error)
	Status(string) (imap.FolderInfo, error)
	UIDs(string) ([]uint32, error)
	Fetch(string, []uint32, uint64, bool) ([]imap.Email, error)
	Quota() (*imap.StorageQuota, error)
	CanIdle() bool
	CondStore() bool
	StartIdle() (func() error, error)
	Closed() <-chan struct{}
	Close() error
}
type refreshRequest struct {
	folders     []string
	actor, txID string
	done        chan error
	apply       func(context.Context) error
	ctx         context.Context
}
type monitor struct {
	owner, account, password string
	fingerprint              [32]byte
	ctx                      context.Context
	cancel                   context.CancelFunc
	done                     chan struct{}
	ready                    chan struct{}
	wake                     chan struct{}
	requests                 chan refreshRequest
	folders                  map[string]Folder
	order                    []string
	messages                 map[string]map[uint32]Metadata
	backfill                 int
	quota                    *imap.StorageQuota
	lastProgress             atomic.Int64
}

func (s *Service) Start(ctx context.Context, owner string) (*monitor, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	state := s.owner(owner)
	for {
		s.mu.Lock()
		generation := state.generation
		s.mu.Unlock()
		account, password, err := s.credentials(ctx, owner)
		if err != nil {
			return nil, err
		}
		fingerprint := sha256.Sum256([]byte(account + "\x00" + password))
		s.mu.Lock()
		if generation != state.generation {
			s.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		if err := s.ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if m := s.monitors[owner]; m != nil && m.fingerprint == fingerprint {
			s.mu.Unlock()
			return m, nil
		}
		old := s.monitors[owner]
		workerCtx, cancel := context.WithCancel(s.ctx)
		m := &monitor{owner: owner, account: account, password: password, fingerprint: fingerprint, ctx: workerCtx, cancel: cancel, done: make(chan struct{}), ready: make(chan struct{}), wake: make(chan struct{}, 1), requests: make(chan refreshRequest, 16), folders: map[string]Folder{}, messages: map[string]map[uint32]Metadata{}}
		s.monitors[owner] = m
		s.wg.Add(1)
		s.mu.Unlock()
		if old != nil {
			old.cancel()
		}
		go func() {
			defer s.wg.Done()
			defer close(m.done)
			if old != nil {
				select {
				case <-old.done:
				case <-m.ctx.Done():
					return
				}
			}
			s.monitor(m)
		}()
		return m, nil
	}
}
func (s *Service) WaitReady(ctx context.Context, owner string) error {
	m, err := s.Start(ctx, owner)
	if err != nil {
		return err
	}
	select {
	case <-m.ready:
		return nil
	case <-m.done:
		return errors.New("mail sync stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) Refresh(ctx context.Context, owner, actor, txID string, folders ...string) error {
	return s.change(ctx, owner, actor, txID, folders, nil)
}
func (s *Service) change(ctx context.Context, owner, actor, txID string, folders []string, apply func(context.Context) error) error {
	var cancel context.CancelFunc
	if apply != nil {
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	m, err := s.Start(ctx, owner)
	if err != nil {
		return err
	}
	request := refreshRequest{folders: folders, actor: actor, txID: txID, done: make(chan error, 1), apply: apply, ctx: ctx}
	select {
	case m.requests <- request:
	case <-m.done:
		return errors.New("mail sync stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		return err
	case <-m.done:
		return errors.New("mail sync stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) monitor(m *monitor) {
	backoff := time.Second
	for m.ctx.Err() == nil {
		wire, err := s.dialSync(m.ctx, m.account, m.password, func() {
			select {
			case m.wake <- struct{}{}:
			default:
			}
		})
		if err == nil {
			stop := context.AfterFunc(m.ctx, func() { _ = wire.Close() })
			err = s.monitorConnected(m, wire)
			stop()
			_ = wire.Close()
		}
		if m.ctx.Err() != nil {
			return
		}
		slog.Warn("mail sync reconnecting", "owner", m.owner, "error", err)
		phase, problem := "reconnecting", "Synchronization is temporarily unavailable. Retrying."
		if errors.Is(err, imap.ErrSyncAuthentication) {
			phase = "auth_failed"
			problem = "iCloud rejected your app-specific password. Relink your account to resume."
		}
		_ = s.syncStatus(m, phase, "", problem)
		delay := backoff + time.Duration(rand.Float64()*float64(backoff)/3) //nolint:gosec // Reconnect jitter is not a security value.
		select {
		case <-time.After(delay):
		case <-m.ctx.Done():
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}
func (s *Service) monitorConnected(m *monitor, wire syncTransport) error {
	m.lastProgress.Store(time.Now().UnixNano())
	stopWatchdog := watchProgress(m.ctx, &m.lastProgress, 15*time.Second, 3*time.Minute, func() {
		age := time.Since(time.Unix(0, m.lastProgress.Load()))
		slog.Error("mail sync stalled", "owner", m.owner, "age", age)
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		if status, err := s.Status(ctx, m.owner, m.account); err == nil {
			status.Phase = "stalled"
			status.Error = "Synchronization made no progress for three minutes."
			status.At = time.Now().UTC()
			_ = s.commit(ctx, m.owner, m.account, "system", "", []Change{{Entity: "status", Op: "put", Key: m.owner, Status: &status}})
		}
		cancel()
		_ = wire.Close()
	})
	defer stopWatchdog()
	folders, err := wire.Folders()
	if err != nil {
		return err
	}
	stored, err := s.Folders(m.ctx, m.owner, m.account)
	if err != nil {
		return err
	}
	for _, folder := range stored {
		m.folders[folder.Name] = folder
	}
	m.order = nil
	for _, info := range folders {
		m.order = append(m.order, info.Name)
		folder := m.folders[info.Name]
		folder.Key = identity(m.owner, m.account, info.Name)
		folder.Owner = m.owner
		folder.Account = m.account
		folder.Name = info.Name
		folder.Role = info.Role
		folder.Attributes = info.Attributes
		folder.Delimiter = info.Delimiter
		m.folders[info.Name] = folder
	}
	for _, old := range stored {
		if !slices.Contains(m.order, old.Name) {
			reset := old
			reset.UIDValidity = 0
			reset.Total = 0
			reset.Unread = 0
			if err := s.commit(m.ctx, m.owner, m.account, "imap", "", []Change{{Entity: "folder", Op: "reset", Key: old.Key, Folder: &reset}, {Entity: "folder", Op: "del", Key: old.Key}}); err != nil {
				return err
			}
			delete(m.folders, old.Name)
			delete(m.messages, old.Name)
		}
	}
	mode := "POLL"
	if wire.CanIdle() {
		mode = "IDLE"
	}
	if wire.CondStore() {
		mode += " + CONDSTORE"
	}
	if err = s.syncStatus(m, "importing", mode, ""); err != nil {
		return err
	}
	for i, name := range m.order {
		if err = s.reconcileFolder(m, wire, name, false, "imap", ""); err != nil {
			return err
		}
		if i == 0 {
			select {
			case <-m.ready:
			default:
				close(m.ready)
			}
		}
		if err = s.syncStatus(m, "importing", mode, ""); err != nil {
			return err
		}
		// Deliver urgent browser/MCP refreshes between initial mailbox imports.
		select {
		case request := <-m.requests:
			if err = s.processRequest(m, wire, request); err != nil {
				return err
			}
		default:
		}
	}
	if quota, quotaErr := wire.Quota(); quotaErr == nil {
		m.quota = quota
	} else {
		slog.Warn("read mail storage quota", "error", quotaErr)
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var events <-chan imap.SyncEvent
	if transport, ok := wire.(interface{ Events() <-chan imap.SyncEvent }); ok {
		events = transport.Events()
	}
	for m.ctx.Err() == nil {
		complete := true
		for _, folder := range m.folders {
			if !folder.Complete {
				complete = false
				break
			}
		}
		phase := "live"
		if !complete {
			phase = "importing"
		}
		if err = s.syncStatus(m, phase, mode, ""); err != nil {
			return err
		}
		var stopIdle func() error
		if wire.CanIdle() && complete {
			stopIdle, err = wire.StartIdle()
			if err != nil {
				return err
			}
		}
		var backfill <-chan time.Time
		if !complete {
			backfill = time.After(40 * time.Millisecond)
		}
		var request refreshRequest
		var event imap.SyncEvent
		work := ""
		select {
		case <-m.ctx.Done():
			work = "stop"
		case <-wire.Closed():
			work = "closed"
		case <-m.wake:
			work = "inbox"
		case event = <-events:
			work = "event"
		case request = <-m.requests:
			work = "request"
		case <-ticker.C:
			work = "poll"
		case <-backfill:
			work = "backfill"
		}
		if stopIdle != nil {
			if stopErr := stopIdle(); stopErr != nil && m.ctx.Err() == nil {
				return stopErr
			}
		}
		switch work {
		case "stop":
			return m.ctx.Err()
		case "closed":
			return errors.New("IMAP IDLE connection closed")
		case "request":
			if err = s.processRequest(m, wire, request); err != nil {
				return err
			}
		case "inbox":
			if err = s.reconcileFolder(m, wire, "INBOX", false, "imap", ""); err != nil {
				return err
			}
		case "event":
			if event.Kind == "vanished" {
				err = s.reconcileVanished(m, wire, event)
			} else if event.Kind == "fetch" && event.Folder != "" {
				uids, ok := event.Numbers()
				if ok && len(uids) > 0 {
					err = s.reconcileUIDs(m, wire, event.Folder, uids)
				} else {
					err = s.reconcileFolder(m, wire, event.Folder, false, "imap", "")
				}
			} else if event.Kind == "mailbox" && event.Folder != "" {
				err = s.reconcileMailboxEvent(m, wire, event.Folder)
			} else if event.Folder == "" {
				err = s.reconcileFolder(m, wire, "INBOX", false, "imap", "")
			} else {
				err = s.reconcileFolder(m, wire, event.Folder, false, "imap", "")
			}
			if err != nil {
				return err
			}
		case "poll":
			err = s.pollFolders(m, wire)
			if err != nil {
				return err
			}
		case "backfill":
			for n := 0; n < len(m.order); n++ {
				index := (m.backfill + n) % len(m.order)
				name := m.order[index]
				if !m.folders[name].Complete {
					err = s.reconcileFolder(m, wire, name, true, "imap", "")
					m.backfill = (index + 1) % len(m.order)
					if err != nil {
						return err
					}
					break
				}
			}
		}
		m.lastProgress.Store(time.Now().UnixNano())
	}
	return m.ctx.Err()
}

func watchProgress(ctx context.Context, lastProgress *atomic.Int64, interval, staleAfter time.Duration, onStall func()) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if age := time.Since(time.Unix(0, lastProgress.Load())); age > staleAfter {
					onStall()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// Hold reconciliation while a shared command runs so IDLE cannot steal its actor/tx identity.
func (s *Service) processRequest(m *monitor, wire syncTransport, request refreshRequest) error {
	if request.ctx != nil && request.ctx.Err() != nil {
		request.done <- request.ctx.Err()
		return nil
	}
	var applied error
	if request.apply != nil {
		applied = request.apply(request.ctx)
	}
	var sent *smtp.SentCopyError
	var saved *imap.DraftSavedError
	if applied != nil && !errors.As(applied, &sent) && !errors.As(applied, &saved) {
		request.done <- applied
		return nil
	}
	if err := s.refreshFolders(m, wire, request); err != nil {
		request.done <- errors.Join(applied, fmt.Errorf("mail operation applied; mirror refresh failed: %w", err))
		return err
	}
	request.done <- errors.Join(applied, s.acknowledge(m.ctx, m.owner, m.account, request.actor, request.txID))
	return nil
}
func (s *Service) refreshFolders(m *monitor, wire syncTransport, request refreshRequest) error {
	names := request.folders
	if len(names) == 0 {
		current, err := wire.Folders()
		if err != nil {
			return err
		}
		names = []string{}
		for _, info := range current {
			names = append(names, info.Name)
			if _, ok := m.folders[info.Name]; !ok {
				m.folders[info.Name] = Folder{Key: identity(m.owner, m.account, info.Name), Owner: m.owner, Account: m.account, FolderInfo: info}
				m.order = append(m.order, info.Name)
			}
		}
		for name, old := range m.folders {
			if slices.Contains(names, name) {
				continue
			}
			if err := s.commit(m.ctx, m.owner, m.account, request.actor, request.txID, []Change{{Entity: "folder", Op: "reset", Key: old.Key, Folder: &old}, {Entity: "folder", Op: "del", Key: old.Key}}); err != nil {
				return err
			}
			delete(m.folders, name)
			delete(m.messages, name)
		}
		m.order = slices.Clone(names)
	}
	for _, name := range names {
		if err := s.reconcileFolder(m, wire, name, false, request.actor, request.txID); err != nil {
			return err
		}
	}
	return nil
}

type folderStatusTransport interface {
	FolderStatuses() ([]imap.FolderInfo, error)
}

func (s *Service) pollFolders(m *monitor, wire syncTransport) error {
	var current []imap.FolderInfo
	var err error
	if transport, ok := wire.(folderStatusTransport); ok {
		current, err = transport.FolderStatuses()
	} else {
		current, err = wire.Folders()
		if err == nil {
			for i := range current {
				status, statusErr := wire.Status(current[i].Name)
				if statusErr != nil {
					return statusErr
				}
				status.Role, status.Attributes, status.Delimiter = current[i].Role, current[i].Attributes, current[i].Delimiter
				current[i] = status
			}
		}
	}
	if err != nil {
		return err
	}
	if len(current) != len(m.order) {
		return s.refreshFolders(m, wire, refreshRequest{actor: "imap"})
	}
	for i, info := range current {
		if m.order[i] != info.Name {
			return s.refreshFolders(m, wire, refreshRequest{actor: "imap"})
		}
	}
	for _, info := range current {
		previous := m.folders[info.Name]
		if previous.UIDValidity == info.UIDValidity && previous.UIDNext == info.UIDNext && previous.HighestModSeq == info.HighestModSeq && previous.Total == info.Total && previous.Unread == info.Unread && previous.Role == info.Role && slices.Equal(previous.Attributes, info.Attributes) {
			continue
		}
		if err := s.reconcileFolder(m, wire, info.Name, false, "imap", ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileMailboxEvent(m *monitor, wire syncTransport, name string) error {
	previous := m.folders[name]
	info, err := wire.Status(name)
	if err != nil {
		return err
	}
	if previous.UIDValidity != info.UIDValidity || info.UIDNext < previous.UIDNext {
		return s.reconcileFolder(m, wire, name, false, "imap", "")
	}
	count := uint64(info.UIDNext - previous.UIDNext)
	if count == 0 || count > 1000 {
		return s.reconcileFolder(m, wire, name, false, "imap", "")
	}
	uids := make([]uint32, count)
	for i := range uids {
		uids[i] = previous.UIDNext + uint32(i)
	}
	return s.reconcileUIDs(m, wire, name, uids)
}

func (s *Service) reconcileUIDs(m *monitor, wire syncTransport, name string, uids []uint32) error {
	started := time.Now()
	previous, cached := m.folders[name], m.messages[name]
	if previous.UIDValidity == 0 || cached == nil {
		return s.reconcileFolder(m, wire, name, false, "imap", "")
	}
	known := make(map[uint32]Metadata, len(cached))
	for uid, mail := range cached {
		known[uid] = mail
	}
	existing, added := []uint32{}, []uint32{}
	for _, uid := range uids {
		if _, ok := known[uid]; ok {
			existing = append(existing, uid)
		} else {
			added = append(added, uid)
		}
	}
	changes := []Change{}
	arrivals := 0
	if len(existing) > 0 {
		updates, broken, err := fetchMailboxResilient(wire, name, existing, 0, true)
		if err != nil {
			return err
		}
		for _, update := range updates {
			var uid uint32
			_, _ = fmt.Sscan(update.ID, &uid)
			old, ok := known[uid]
			if !ok {
				continue
			}
			next := old
			next.Flags, next.Unread, next.FlagColor, next.ModSeq = update.Flags, update.Unread, update.FlagColor, update.ModSeq
			next.Broken = next.Broken || broken[uid]
			next.Fingerprint = metadataFingerprint(next.Email)
			if next.Fingerprint != old.Fingerprint || next.Broken != old.Broken {
				changes = append(changes, Change{Entity: "msg", Op: "put", Key: next.Key, Message: &next, Before: &old})
				known[uid] = next
			}
		}
	}
	if len(added) > 0 {
		fetched, broken, err := fetchMailboxResilient(wire, name, added, 0, false)
		if err != nil {
			return err
		}
		prepared, merged, err := s.prepareBatch(m.ctx, m.owner, m.account, fetched)
		if err != nil {
			return err
		}
		changes = append(changes, merged...)
		for i := range prepared {
			var uid uint32
			_, _ = fmt.Sscan(prepared[i].ID, &uid)
			prepared[i].Broken = broken[uid]
		}
		var rules []Rule
		rulesLoaded := false
		for _, metadata := range prepared {
			var uid uint32
			_, _ = fmt.Sscan(metadata.ID, &uid)
			arrival := previous.Ready && uid >= previous.UIDNext && strings.EqualFold(name, "INBOX")
			if arrival {
				arrivals++
			}
			if arrival && !rulesLoaded {
				prefs, err := s.Preferences(m.ctx, m.owner)
				if err != nil {
					return err
				}
				rules = prefs.Rules
				rulesLoaded = true
			}
			copy := metadata
			changes = append(changes, Change{Entity: "msg", Op: "put", Key: metadata.Key, Message: &copy, Arrival: arrival})
			if arrival {
				if job := newRuleJob(m.owner, m.account, rules, metadata); job != nil {
					changes = append(changes, Change{Entity: "rule-job", Op: "put", Key: job.ID, RuleJob: job})
				}
			}
			known[uid] = metadata
		}
	}
	info, err := wire.Status(name)
	if err != nil {
		return err
	}
	if info.UIDValidity != previous.UIDValidity {
		return errors.New("mailbox generation changed during unilateral FETCH")
	}
	folder := previous
	folder.FolderInfo = info
	folder.Role, folder.Attributes, folder.Delimiter = previous.Role, previous.Attributes, previous.Delimiter
	folder.Imported = int64(len(known))
	folder.Complete = folder.Imported >= int64(info.Total)
	if !reflect.DeepEqual(folder, previous) {
		copy := folder
		changes = append(changes, Change{Entity: "folder", Op: "put", Key: folder.Key, Folder: &copy})
	}
	updates := make([]Metadata, 0, len(changes))
	indices := make([]int, 0, len(changes))
	for i, change := range changes {
		if change.Entity == "msg" && change.Op == "put" && change.Message != nil {
			updates = append(updates, *change.Message)
			indices = append(indices, i)
		}
	}
	if len(updates) > 0 {
		if err = s.decorateMoves(m.ctx, m.owner, m.account, updates); err != nil {
			return err
		}
		for i, index := range indices {
			changes[index].Message = &updates[i]
		}
	}
	if err = s.commit(m.ctx, m.owner, m.account, "imap", "", changes); err != nil {
		return err
	}
	m.messages[name], m.folders[name] = known, folder
	if arrivals > 0 && strings.EqualFold(name, "INBOX") {
		s.queuePrefetch(m.owner, m.account)
	}
	slog.Info("mail sync reconcile", "folder", name, "duration_ms", time.Since(started).Milliseconds(), "fetched", len(added), "flags", len(existing), "deleted", 0)
	return nil
}

func (s *Service) reconcileVanished(m *monitor, wire syncTransport, event imap.SyncEvent) error {
	if event.Folder == "" {
		return s.reconcileFolder(m, wire, "INBOX", false, "imap", "")
	}
	if m.messages[event.Folder] == nil {
		return s.reconcileFolder(m, wire, event.Folder, false, "imap", "")
	}
	started := time.Now()
	previous := m.folders[event.Folder]
	known := make(map[uint32]Metadata, len(m.messages[event.Folder]))
	changes := make([]Change, 0)
	for uid, mail := range m.messages[event.Folder] {
		if event.HasUID(uid) {
			before := mail
			changes = append(changes, Change{Entity: "msg", Op: "del", Key: mail.Key, Before: &before})
			continue
		}
		known[uid] = mail
	}
	if len(changes) == 0 {
		return nil
	}
	deleted := len(changes)
	info, err := wire.Status(event.Folder)
	if err != nil {
		return err
	}
	folder := previous
	folder.FolderInfo = info
	folder.Role, folder.Attributes, folder.Delimiter = previous.Role, previous.Attributes, previous.Delimiter
	folder.Imported = int64(len(known))
	folder.Complete = folder.Imported >= int64(info.Total)
	if !reflect.DeepEqual(folder, previous) {
		copy := folder
		changes = append(changes, Change{Entity: "folder", Op: "put", Key: folder.Key, Folder: &copy})
	}
	if err = s.commit(m.ctx, m.owner, m.account, "imap", "", changes); err != nil {
		return err
	}
	m.messages[event.Folder] = known
	m.folders[event.Folder] = folder
	slog.Info("mail sync reconcile", "folder", event.Folder, "duration_ms", time.Since(started).Milliseconds(), "fetched", 0, "flags", 0, "deleted", deleted)
	return nil
}

func (s *Service) syncStatus(m *monitor, phase, mode, problem string) error {
	now := time.Now().UTC()
	status := SyncStatus{Owner: m.owner, Account: m.account, Phase: phase, Mode: mode, Error: problem, Quota: m.quota, At: now}
	select {
	case <-m.ready:
		status.Ready = true
	default:
	}
	for _, folder := range m.folders {
		status.Total += int64(folder.Total)
		status.Imported += folder.Imported
	}
	previous, err := s.Status(m.ctx, m.owner, m.account)
	if err != nil {
		return err
	}
	if previous.Phase == status.Phase && previous.Mode == status.Mode && previous.Error == status.Error && previous.Ready == status.Ready && previous.Imported == status.Imported && previous.Total == status.Total && now.Sub(previous.At) < time.Minute && ((previous.Quota == nil && status.Quota == nil) || (previous.Quota != nil && status.Quota != nil && *previous.Quota == *status.Quota)) {
		return nil
	}
	return s.commit(m.ctx, m.owner, m.account, "system", "", []Change{{Entity: "status", Op: "put", Key: m.owner, Status: &status}})
}
func (s *Service) reconcileFolder(m *monitor, wire syncTransport, name string, backfill bool, actor, txID string) error {
	started := time.Now()
	if m.messages == nil {
		m.messages = map[string]map[uint32]Metadata{}
	}
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	info, err := wire.Status(name)
	if err != nil {
		return fmt.Errorf("status %s: %w", name, err)
	}
	previous := m.folders[name]
	if previous.Ready && previous.UIDValidity == info.UIDValidity {
		if transport, ok := wire.(interface {
			SelectQResync(string, uint32, uint64) error
		}); ok {
			if err := transport.SelectQResync(name, info.UIDValidity, previous.HighestModSeq); err != nil {
				return err
			}
		}
	}
	folder := previous
	folder.Key = identity(m.owner, m.account, name)
	folder.Owner = m.owner
	folder.Account = m.account
	folder.FolderInfo = info
	folder.Attributes = previous.Attributes
	folder.Delimiter = previous.Delimiter
	if previous.Role != "" {
		folder.Role = previous.Role
	}
	reset := previous.UIDValidity != 0 && previous.UIDValidity != info.UIDValidity
	if previous.Ready && !backfill && !reset && wire.CondStore() && info.HighestModSeq > 0 && previous.HighestModSeq == info.HighestModSeq && previous.UIDNext == info.UIDNext && previous.Total == info.Total && previous.Unread == info.Unread {
		slog.Info("mail sync reconcile", "folder", name, "duration_ms", time.Since(started).Milliseconds(), "fetched", 0, "flags", 0, "deleted", 0)
		return nil
	}
	if reset {
		folder.Imported = 0
		folder.BeforeUID = 0
		folder.Ready = false
		folder.Complete = false
		folder.FullFlagSync = time.Time{}
	}
	var changes []Change
	if reset {
		fresh := folder
		changes = append(changes, Change{Entity: "folder", Op: "reset", Key: folder.Key, Folder: &fresh})
	}
	known := make(map[uint32]Metadata)
	if cached := m.messages[name]; cached != nil && !reset {
		for uid, mail := range cached {
			known[uid] = mail
		}
	} else {
		existing, err := s.metadata(m.ctx, m.owner, m.account, bson.M{"email.folder": name, "email.uidvalidity": info.UIDValidity}, 0)
		if err != nil {
			return err
		}
		known = make(map[uint32]Metadata, len(existing))
		for _, mail := range existing {
			var uid uint32
			_, _ = fmt.Sscan(mail.ID, &uid)
			known[uid] = mail
		}
	}
	var allUIDs []uint32
	needUIDs := !folder.Ready || backfill || reset || previous.Total != info.Total || previous.UIDNext != info.UIDNext
	if needUIDs {
		allUIDs, err = wire.UIDs(name)
		if err != nil {
			return err
		}
		live := make(map[uint32]bool, len(allUIDs))
		for _, uid := range allUIDs {
			live[uid] = true
		}
		for uid, mail := range known {
			if !live[uid] {
				copy := mail
				changes = append(changes, Change{Entity: "msg", Op: "del", Key: mail.Key, Before: &copy})
				delete(known, uid)
			}
		}
	}
	var fetch []uint32
	if needUIDs {
		limit := 500
		if folder.Ready {
			limit = 1000
		}
		for i := len(allUIDs) - 1; i >= 0 && len(fetch) < limit; i-- {
			uid := allUIDs[i]
			if _, ok := known[uid]; !ok {
				fetch = append(fetch, uid)
			}
		}
	}
	fetched, flagsUpdated, arrivals := 0, 0, 0
	fetchRan := false
	if len(fetch) > 0 {
		mail, broken, err := fetchMailboxResilient(wire, name, fetch, 0, false)
		if err != nil {
			return err
		}
		fetchRan = true
		fetched = len(mail)

		prepared, merged, err := s.prepareBatch(m.ctx, m.owner, m.account, mail)
		if err != nil {
			return err
		}
		for i := range prepared {
			var uid uint32
			_, _ = fmt.Sscan(prepared[i].ID, &uid)
			prepared[i].Broken = broken[uid]
		}
		deleted := make(map[string]bool)
		for _, change := range changes {
			if change.Entity == "msg" && change.Op == "del" {
				deleted[change.Key] = true
			}
		}
		for _, change := range merged {
			if deleted[change.Key] || reset && change.Message != nil && change.Message.Folder == name && change.Message.UIDValidity != info.UIDValidity {
				continue
			}
			changes = append(changes, change)
		}
		var rules []Rule
		if previous.Ready && !reset && (folder.Role == "inbox" || strings.EqualFold(name, "INBOX")) {
			prefs, err := s.Preferences(m.ctx, m.owner)
			if err != nil {
				return err
			}
			rules = prefs.Rules
		}
		for _, metadata := range prepared {
			var uid uint32
			_, _ = fmt.Sscan(metadata.ID, &uid)
			copy := metadata
			arrival := previous.Ready && !reset && uid >= previous.UIDNext && (folder.Role == "inbox" || strings.EqualFold(name, "INBOX"))
			changes = append(changes, Change{Entity: "msg", Op: "put", Key: metadata.Key, Message: &copy, Arrival: arrival})
			if arrival {
				arrivals++
				if job := newRuleJob(m.owner, m.account, rules, metadata); job != nil {
					changes = append(changes, Change{Entity: "rule-job", Op: "put", Key: job.ID, RuleJob: job})
				}
			}
			known[uid] = metadata
		}

	}
	if len(known) > 0 && (!backfill || reset) {
		uids := make([]uint32, 0, len(known))
		for uid := range known {
			uids = append(uids, uid)
		}
		slices.Sort(uids)
		since := uint64(0)
		switch {
		case wire.CondStore() && info.HighestModSeq > 0 && !reset:
			since = previous.HighestModSeq
		case time.Since(folder.FullFlagSync) < time.Hour && len(uids) > 2000:
			uids = uids[len(uids)-2000:]
		default:
			folder.FullFlagSync = time.Now().UTC()
		}
		if since == 0 || since != info.HighestModSeq || !wire.CondStore() {
			flags, broken, err := fetchMailboxResilient(wire, name, uids, since, true)
			if err != nil {
				return err
			}
			fetchRan = true
			for _, update := range flags {
				var uid uint32
				_, _ = fmt.Sscan(update.ID, &uid)
				old, ok := known[uid]
				if !ok {
					continue
				}
				next := old
				next.Broken = next.Broken || broken[uid]
				next.Flags = update.Flags
				next.Unread = update.Unread
				next.FlagColor = update.FlagColor
				next.ModSeq = update.ModSeq
				next.Fingerprint = metadataFingerprint(next.Email)
				if old.Fingerprint != next.Fingerprint || old.Broken != next.Broken {
					changes = append(changes, Change{Entity: "msg", Op: "put", Key: next.Key, Message: &next, Before: &old})
					known[uid] = next
					flagsUpdated++
				}
			}
		}
	}
	folder.Ready = true
	folder.Imported = int64(len(known))
	folder.Complete = folder.Imported >= int64(info.Total)
	if len(allUIDs) > 0 && len(fetch) > 0 {
		folder.BeforeUID = fetch[len(fetch)-1]
	}
	if folder.Complete {
		folder.BeforeUID = 0
	}
	if !reflect.DeepEqual(folder, previous) {
		copy := folder
		changes = append(changes, Change{Entity: "folder", Op: "put", Key: folder.Key, Folder: &copy})
	}
	// A mailbox can be recreated while SEARCH/FETCH is in flight; never mix generations.
	if fetchRan {
		latest, err := wire.Status(name)
		if err != nil {
			return err
		}
		if latest.UIDValidity != info.UIDValidity {
			return errors.New("mailbox generation changed during reconciliation")
		}
	}
	updates := make([]Metadata, 0, len(changes))
	indices := make([]int, 0, len(changes))
	for i, change := range changes {
		if change.Entity == "msg" && change.Op == "put" && change.Message != nil {
			updates = append(updates, *change.Message)
			indices = append(indices, i)
		}
	}
	if len(updates) > 0 {
		if err = s.decorateMoves(m.ctx, m.owner, m.account, updates); err != nil {
			return err
		}
		for i, index := range indices {
			changes[index].Message = &updates[i]
		}
	}
	if err = s.commit(m.ctx, m.owner, m.account, actor, txID, changes); err != nil {
		return err
	}
	m.folders[name] = folder
	m.messages[name] = known
	if arrivals > 0 && strings.EqualFold(name, "INBOX") {
		s.queuePrefetch(m.owner, m.account)
	}
	deleted := 0
	for _, change := range changes {
		if change.Entity == "msg" && change.Op == "del" {
			deleted++
		}
	}
	slog.Info("mail sync reconcile", "folder", name, "duration_ms", time.Since(started).Milliseconds(), "fetched", fetched, "flags", flagsUpdated, "deleted", deleted)
	return nil
}

func metadataFingerprint(email imap.Email) string {
	email.Flags = slices.Clone(email.Flags)
	slices.Sort(email.Flags)
	raw, _ := json.Marshal(email)
	return identity(string(raw))
}
