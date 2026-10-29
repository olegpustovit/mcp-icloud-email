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

// A marker survives MOVE, including a lost response; recovery does not rely on Message-ID uniqueness.
func (c *Client) Relocate(ctx context.Context, from Location, to, marker string) (Location, error) {
	if !strings.HasPrefix(marker, "$TerminalMail_") || len(marker) > 100 || strings.ContainsAny(marker, " \r\n\x00") {
		return Location{}, errors.New("invalid relocation")
	}
	if err := ctx.Err(); err != nil {
		return Location{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	found, err := c.findMarker(to, marker)
	if err != nil {
		return Location{}, err
	}
	if found.ID != "" {
		return found, nil
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
		return Location{}, ErrMarkerUnsupported
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
	criteria.WithFlags = []string{marker}
	uids, err = c.client.UidSearch(criteria)
	if err != nil {
		return Location{}, err
	}
	if len(uids) != 1 {
		return Location{}, ErrMarkerUnsupported
	}
	if from.Folder == to {
		return from, nil
	}
	if err = c.client.UidMove(set, to); err != nil {
		return Location{}, fmt.Errorf("relocate message (destination will be checked before retry): %w", err)
	}
	found, err = c.findMarker(to, marker)
	if err != nil {
		return Location{}, err
	}
	if found.ID == "" {
		return Location{}, errors.New("moved message destination could not be confirmed")
	}
	return found, nil
}
func (c *Client) findMarker(folder, marker string) (Location, error) {
	box, err := c.client.Select(folder, true)
	if err != nil {
		return Location{}, err
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{marker}
	uids, err := c.client.UidSearch(criteria)
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
	return c.client.UidStore(set, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{marker}, nil)
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
