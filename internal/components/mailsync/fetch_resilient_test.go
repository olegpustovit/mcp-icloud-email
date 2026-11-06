package mailsync

import (
	"errors"
	"fmt"
	"testing"

	"github.com/olegpustovit/mcp-icloud-email/imap"
)

func TestFetchResilientIsolatesAndQuarantinesOneUID(t *testing.T) {
	uids := make([]uint32, 50)
	for i := range uids {
		uids[i] = uint32(i + 1)
	}
	fetch := func(batch []uint32) ([]imap.Email, error) {
		for _, uid := range batch {
			if uid == 13 {
				return nil, errors.New("imapwire: malformed FETCH response")
			}
		}
		messages := make([]imap.Email, len(batch))
		for i, uid := range batch {
			messages[i] = imap.Email{ID: fmt.Sprint(uid)}
		}
		return messages, nil
	}
	rawHeader := func(uid uint32) (imap.Email, error) { return imap.Email{ID: fmt.Sprint(uid)}, nil }
	messages, broken, err := fetchResilient(uids, fetch, rawHeader, make(chan struct{}), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 50 || len(broken) != 1 || !broken[13] {
		t.Fatalf("fetched %d messages, quarantine=%v", len(messages), broken)
	}
}

func TestFetchResilientDoesNotSplitServerErrors(t *testing.T) {
	calls := 0
	_, _, err := fetchResilient([]uint32{1, 2, 3}, func([]uint32) ([]imap.Email, error) {
		calls++
		return nil, errors.New("server unavailable")
	}, func(uint32) (imap.Email, error) { t.Fatal("unexpected raw-header fallback"); return imap.Email{}, nil }, make(chan struct{}), 50)
	if err == nil || calls != 1 {
		t.Fatalf("got err %v after %d fetches; want one failed batch", err, calls)
	}
}

func TestFetchResilientUsesLargerFlagBatchSize(t *testing.T) {
	uids := make([]uint32, 2501)
	for i := range uids {
		uids[i] = uint32(i + 1)
	}
	var batches []int
	_, _, err := fetchResilient(uids, func(batch []uint32) ([]imap.Email, error) {
		batches = append(batches, len(batch))
		return nil, nil
	}, nil, make(chan struct{}), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 || batches[0] != 1000 || batches[1] != 1000 || batches[2] != 501 {
		t.Fatalf("got batches %v", batches)
	}
}
