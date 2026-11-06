package mailsync

import (
	"errors"

	"github.com/olegpustovit/mcp-icloud-email/imap"
)

type rawHeaderFetcher interface {
	FetchEnvelope(string, uint32) (imap.Email, error)
}

func fetchMailboxResilient(wire syncTransport, name string, uids []uint32, since uint64, flagsOnly bool) ([]imap.Email, map[uint32]bool, error) {
	var rawHeader func(uint32) (imap.Email, error)
	if fetcher, ok := wire.(rawHeaderFetcher); ok {
		rawHeader = func(uid uint32) (imap.Email, error) { return fetcher.FetchEnvelope(name, uid) }
	}
	return fetchResilient(uids, func(batch []uint32) ([]imap.Email, error) {
		return wire.Fetch(name, batch, since, flagsOnly)
	}, rawHeader, wire.Closed())
}

func fetchResilient(uids []uint32, fetch func([]uint32) ([]imap.Email, error), rawHeader func(uint32) (imap.Email, error), closed <-chan struct{}) ([]imap.Email, map[uint32]bool, error) {
	var messages []imap.Email
	broken := map[uint32]bool{}
	for start := 0; start < len(uids); start += 50 {
		end := min(start+50, len(uids))
		batch, quarantined, err := fetchSplit(uids[start:end], fetch, rawHeader, closed)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, batch...)
		for uid := range quarantined {
			broken[uid] = true
		}
	}
	return messages, broken, nil
}

func fetchSplit(uids []uint32, fetch func([]uint32) ([]imap.Email, error), rawHeader func(uint32) (imap.Email, error), closed <-chan struct{}) ([]imap.Email, map[uint32]bool, error) {
	messages, err := fetch(uids)
	if err == nil {
		return messages, nil, nil
	}
	select {
	case <-closed:
		return nil, nil, err
	default:
	}
	if len(uids) == 1 {
		if rawHeader == nil {
			return nil, nil, err
		}
		message, fallbackErr := rawHeader(uids[0])
		if fallbackErr != nil {
			return nil, nil, errors.Join(err, fallbackErr)
		}
		return []imap.Email{message}, map[uint32]bool{uids[0]: true}, nil
	}
	middle := len(uids) / 2
	left, leftBroken, err := fetchSplit(uids[:middle], fetch, rawHeader, closed)
	if err != nil {
		return nil, nil, err
	}
	right, rightBroken, err := fetchSplit(uids[middle:], fetch, rawHeader, closed)
	if err != nil {
		return nil, nil, err
	}
	if len(rightBroken) > 0 && leftBroken == nil {
		leftBroken = map[uint32]bool{}
	}
	for uid := range rightBroken {
		leftBroken[uid] = true
	}
	return append(left, right...), leftBroken, nil
}
