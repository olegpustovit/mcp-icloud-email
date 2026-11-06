package mailsync

import (
	"context"
	"testing"
)

type laneTestClient struct{ commandClient }

func (laneTestClient) Closed() bool { return false }
func (laneTestClient) Close() error { return nil }

func TestBulkAndInteractiveCommandLanesDoNotBlockEachOther(t *testing.T) {
	ctx := context.Background()
	s := &Service{ctx: ctx, sessions: map[string]map[commandLane]*session{}, credentials: func(context.Context, string) (string, string, error) { return "owner@example.com", "password", nil }}
	s.dialCommand = func(context.Context, string, string) (*Connection, error) {
		return &Connection{Mail: laneTestClient{}}, nil
	}
	bulk, releaseBulk, err := s.AcquireBulk(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBulk()
	interactive, releaseInteractive, err := s.Acquire(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseInteractive()
	if bulk == interactive {
		t.Fatal("bulk and interactive work shared one command connection")
	}
}
