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
				return nil, errors.New("malformed FETCH response")
			}
		}
		messages := make([]imap.Email, len(batch))
		for i, uid := range batch {
			messages[i] = imap.Email{ID: fmt.Sprint(uid)}
		}
		return messages, nil
	}
	rawHeader := func(uid uint32) (imap.Email, error) { return imap.Email{ID: fmt.Sprint(uid)}, nil }
	messages, broken, err := fetchResilient(uids, fetch, rawHeader, make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 50 || len(broken) != 1 || !broken[13] {
		t.Fatalf("fetched %d messages, quarantine=%v", len(messages), broken)
	}
}
