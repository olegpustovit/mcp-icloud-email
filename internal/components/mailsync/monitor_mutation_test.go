package mailsync

import (
	"context"
	"errors"
	"testing"
	"time"
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
