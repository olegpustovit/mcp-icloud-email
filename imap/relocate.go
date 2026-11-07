package imap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap"
)

type Location struct {
	Folder   string `json:"folder" bson:"folder"`
	ID       string `json:"id" bson:"uid"`
	Validity uint32 `json:"validity" bson:"validity"`
}

var ErrLocationChanged = errors.New("message location changed")
var ErrMarkerUnsupported = errors.New("server does not support recoverable moves")

// A marker survives MOVE or COPY, including a lost response; recovery does not rely on Message-ID uniqueness.
func (c *Client) Relocate(ctx context.Context, from Location, to, marker string) (Location, error) {
	if !strings.HasPrefix(strings.ToLower(marker), "$terminalmail_") || len(marker) > 100 || strings.ContainsAny(marker, " \r\n\x00") {
		return Location{}, errors.New("invalid relocation")
	}
	if err := ctx.Err(); err != nil {
		return Location{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relocate(from, to, marker)
}
func (c *Client) relocate(from Location, to, marker string) (Location, error) {
	// go-imap canonicalizes custom flags to lowercase.
	marker = strings.ToLower(marker)
	found, err := c.findMarker(to, marker)
	if err != nil {
		return Location{}, err
	}
	if found.ID != "" {
		if from.Folder != to {
			err = c.finishCopiedMove(from, marker)
		}
		return found, err
	}
	box, err := c.client.Select(from.Folder, false)
	if err != nil {
		return Location{}, err
	}
	if from.Validity == 0 || box.UidValidity != from.Validity {
		return Location{}, ErrLocationChanged
	}
	if !supportsMoveMarker(box.PermanentFlags, marker) {
		return Location{}, ErrMarkerUnsupported
	}
	uid, err := parseUID(from.ID)
	if err != nil {
		return Location{}, err
	}
	supported, err := c.client.Support("MOVE")
	if err != nil {
		return Location{}, err
	}
	if !supported && from.Folder != to {
		uidPlus, err := c.client.Support("UIDPLUS")
		if err != nil {
			return Location{}, err
		}
		if !uidPlus {
			return Location{}, ErrMarkerUnsupported
		}
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	criteria := imap.NewSearchCriteria()
	criteria.Uid = set
	uids, err := c.client.UidSearch(criteria)
	if err != nil {
		return Location{}, err
	}
	if len(uids) != 1 || uids[0] != uid {
		return Location{}, ErrLocationChanged
	}
	if err = c.client.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{marker}, nil); err != nil {
		return Location{}, fmt.Errorf("mark relocation: %w", err)
	}
	uids, err = c.markerUIDs(set, marker)
	if err != nil {
		return Location{}, err
	}
	if len(uids) != 1 {
		return Location{}, ErrMarkerUnsupported
	}
	if from.Folder == to {
		return from, nil
	}
	if supported {
		err = c.client.UidMove(set, to)
	} else {
		err = c.client.UidCopy(set, to)
	}
	if err != nil {
		return Location{}, fmt.Errorf("relocate message (destination will be checked before retry): %w", err)
	}
	found, err = c.findMarker(to, marker)
	if err != nil {
		return Location{}, err
	}
	if found.ID == "" {
		return Location{}, errors.New("moved message destination could not be confirmed")
	}
	if !supported {
		err = c.finishCopiedMove(from, marker)
	}
	return found, err
}

// Remove the source only after its marked destination is confirmed, including on retry after COPY.
func (c *Client) finishCopiedMove(from Location, marker string) error {
	box, err := c.client.Select(from.Folder, false)
	if err != nil {
		return err
	}
	if from.Validity == 0 || box.UidValidity != from.Validity {
		return ErrLocationChanged
	}
	uid, err := parseUID(from.ID)
	if err != nil {
		return err
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	uids, err := c.markerUIDs(set, marker)
	if err != nil || len(uids) == 0 {
		return err
	}
	uidPlus, err := c.client.Support("UIDPLUS")
	if err != nil {
		return err
	}
	if !uidPlus {
		return ErrMarkerUnsupported
	}
	if err = c.client.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("mark copied source deleted: %w", err)
	}
	if err = c.expungeUID(uid); err != nil {
		return fmt.Errorf("expunge copied source: %w", err)
	}
	return nil
}
func (c *Client) findMarker(folder, marker string) (Location, error) {
	box, err := c.client.Select(folder, true)
	if err != nil {
		return Location{}, err
	}
	set := new(imap.SeqSet)
	set.AddRange(1, 0)
	var uids []uint32
	if box.Messages > 0 {
		uids, err = c.markerUIDs(set, marker)
	}
	if err != nil {
		return Location{}, err
	}
	if len(uids) > 1 {
		return Location{}, ErrLocationChanged
	}
	location := Location{Folder: folder, Validity: box.UidValidity}
	if len(uids) == 1 {
		location.ID = fmt.Sprint(uids[0])
	} else if !supportsMoveMarker(box.PermanentFlags, marker) {
		return Location{}, ErrMarkerUnsupported
	}
	return location, nil
}

// iCloud's SEARCH KEYWORD misses custom flags that FETCH FLAGS returns.
func (c *Client) markerUIDs(set *imap.SeqSet, marker string) ([]uint32, error) {
	messages := make(chan *imap.Message, 32)
	done := make(chan error, 1)
	go func() { done <- c.client.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchFlags}, messages) }()
	var uids []uint32
	for message := range messages {
		for _, flag := range message.Flags {
			if strings.EqualFold(flag, marker) {
				uids = append(uids, message.Uid)
				break
			}
		}
	}
	return uids, <-done
}
func supportsMoveMarker(flags []string, marker string) bool {
	if len(flags) == 0 {
		return true
	}
	for _, flag := range flags {
		if flag == `\*` || strings.EqualFold(flag, marker) {
			return true
		}
	}
	return false
}
func (c *Client) ClearMoveMarker(ctx context.Context, location Location, marker string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clearMoveMarker(location, marker)
}
func (c *Client) clearMoveMarker(location Location, marker string) error {
	box, err := c.client.Select(location.Folder, false)
	if err != nil {
		return err
	}
	if location.Validity == 0 || box.UidValidity != location.Validity {
		return ErrLocationChanged
	}
	uid, err := parseUID(location.ID)
	if err != nil {
		return err
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	return c.client.UidStore(set, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{strings.ToLower(marker)}, nil)
}
func (c *Client) RenameFolder(ctx context.Context, from, to string) error {
	if strings.EqualFold(from, "INBOX") || from == "" || to == "" || len(from) > 1000 || len(to) > 1000 || strings.ContainsAny(from+to, "\r\n\x00") {
		return errors.New("invalid mailbox rename")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client.Rename(from, to)
}
