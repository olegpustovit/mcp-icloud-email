package mailsync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestBlockedMutationHonorsRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	request := refreshRequest{
		ctx:  ctx,
		done: make(chan error, 1),
		apply: func(ctx context.Context) error {
			select {
			case gate <- struct{}{}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	started := time.Now()
	if err := (&Service{}).processRequest(&monitor{owner: "owner", ctx: context.Background()}, nil, request); err != nil {
		t.Fatal(err)
	}
	if err := <-request.done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked mutation error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("monitor remained blocked for %v", elapsed)
	}
}

func TestBlockedMutationDoesNotBlockArrivalSync(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const owner, account = "owner@example.com", "mirror@icloud.com"
	fake := &syncFake{
		mail:     map[uint32]imap.Email{1: {ID: "1", Subject: "one", Date: time.Now()}},
		validity: 7, next: 2, modseq: 1, closed: make(chan struct{}),
	}
	s.dialSync = func(_ context.Context, _, _ string, notify func()) (syncTransport, error) {
		fake.mu.Lock()
		fake.notify = notify
		fake.mu.Unlock()
		return fake, nil
	}
	if _, err := s.Mailbox(ctx, owner, "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(ctx, owner); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	m := s.monitors[owner]
	s.mu.Unlock()
	requestCtx, requestCancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer requestCancel()
	started := make(chan struct{})
	request := refreshRequest{
		ctx: requestCtx, done: make(chan error, 1), folders: []string{"INBOX"}, actor: "mcp:test", txID: "blocked-mutation",
		apply: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	select {
	case m.requests <- request:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("mutation did not start")
	}
	fake.change(func() {
		fake.mail[2] = imap.Email{ID: "2", Subject: "arrival", Date: time.Now(), Unread: true}
		fake.next = 3
		fake.modseq = 2
	})
	t.Logf("queued sync wake=%d", len(m.wake))
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		count, err := s.db.Collection("mail_messages").CountDocuments(ctx, bson.M{"owner": owner, "account": account, "email.id": "2"})
		if err == nil && count == 1 {
			select {
			case err := <-request.done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked mutation result = %v, want deadline exceeded", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("arrival was not committed while the mutation was blocked")
}

func TestWatchdogRestartsAfterStaleProgress(t *testing.T) {
	var progress atomic.Int64
	progress.Store(time.Now().Add(-time.Second).UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stalled := make(chan struct{})
	clock := time.Now()
	stop := watchProgress(ctx, &progress, time.Millisecond, 10*time.Millisecond, func() time.Time { return clock }, func() { close(stalled) })
	defer stop()
	select {
	case <-stalled:
	case <-time.After(time.Second):
		t.Fatal("stale sync progress did not trigger the watchdog")
	}
}
