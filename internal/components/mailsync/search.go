package mailsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var ErrInvalidSearchCursor = errors.New("invalid search cursor")

type SearchPage struct {
	Messages []Metadata `json:"messages"`
	Cursor   int64      `json:"syncId"`
	More     bool       `json:"more"`
	Reset    bool       `json:"reset,omitempty"`
	Next     string     `json:"next"`
}

func (s *Service) RemoteSearch(ctx context.Context, owner, account, query string, next string) (SearchPage, error) {
	result := SearchPage{Messages: []Metadata{}}
	plan, err := imap.ParseQuery(query)
	if err != nil {
		return result, err
	}
	var position searchPosition
	if next != "" {
		raw, decodeErr := base64.RawURLEncoding.DecodeString(next)
		if decodeErr != nil || len(raw) > 2048 || json.Unmarshal(raw, &position) != nil || position.Folder == "" || !validMailbox(position.Folder) || position.Before > 0 && position.Validity == 0 {
			return result, ErrInvalidSearchCursor
		}
	}
	if strings.TrimSpace(query) == "" {
		return result, errors.New("invalid search page")
	}
	if err = s.WaitReady(ctx, owner); err != nil {
		return result, err
	}
	folders, err := s.Folders(ctx, owner, account)
	if err != nil {
		return result, err
	}
	result.Cursor, err = s.Cursor(ctx, owner, account)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, release, err := s.Acquire(ctx, owner)
	if err != nil {
		return result, err
	}
	defer release()
	if c.Address != account {
		return result, errors.New("iCloud account changed")
	}
	if err = s.checkRenamePending(ctx, owner, account); err != nil {
		return result, err
	}
	const limit = 200
	eligible := folders[:0]
	for _, folder := range folders {
		if plan.IncludesFolder(folder.Name, folder.Role) {
			eligible = append(eligible, folder)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Name < eligible[j].Name })
	start := 0
	if position.Folder != "" {
		start = -1
		for index, folder := range eligible {
			if folder.Name == position.Folder {
				start = index
				break
			}
		}
		if start < 0 {
			result.Reset = true
			return result, nil
		}
	}
	for index := start; index < len(eligible); index++ {
		result.Next = ""
		result.More = false
		folder := eligible[index]
		page, err := c.Mail.SearchPage(ctx, folder.Name, query, imap.EmailFilters{Advanced: true, Limit: limit, BeforeUID: position.Before, ExpectedValidity: position.Validity})
		if errors.Is(err, imap.ErrLocationChanged) {
			result.Reset = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
		mails := page.Messages
		if page.NextUID > 0 {
			result.Next = searchPosition{Folder: folder.Name, Before: page.NextUID, Validity: page.UIDValidity}.encode()
		} else if index+1 < len(eligible) {
			result.Next = searchPosition{Folder: eligible[index+1].Name}.encode()
		}
		result.More = result.Next != ""

		keys := make([]string, len(mails))
		for i, mail := range mails {
			keys[i] = messageKey(owner, account, folder.Name, mail.UIDValidity, mail.ID)
		}
		stored := map[string]Metadata{}
		if len(keys) > 0 {
			rows, err := s.db.Collection("mail_messages").Find(ctx, bson.M{"owner": owner, "account": account, "_id": bson.M{"$in": keys}})
			if err != nil {
				return result, err
			}
			for rows.Next(ctx) {
				var item Metadata
				if err = rows.Decode(&item); err != nil {
					_ = rows.Close(ctx)
					return result, err
				}
				stored[item.Key] = item
			}
			err = rows.Err()
			_ = rows.Close(ctx)
			if err != nil {
				return result, err
			}
		}
		for i, mail := range mails {
			if !plan.MatchesMetadata(mail) {
				continue
			}
			row, found := stored[keys[i]]
			if !found {
				row = Metadata{Key: keys[i], Owner: owner, Account: account, Email: mail}
				row.Folder = folder.Name
				row.ThreadID = keys[i]
			} else {
				row.Unread = mail.Unread
				row.Flags = mail.Flags
				row.FlagColor = mail.FlagColor
			}
			row.BodyPlain = ""
			row.BodyHTML = ""
			row.Version = result.Cursor
			result.Messages = append(result.Messages, row)
		}
		if len(mails) > 0 || page.NextUID > 0 {
			break
		}
		position = searchPosition{}

	}
	sort.Slice(result.Messages, func(i, j int) bool {
		a, b := result.Messages[i], result.Messages[j]
		if a.Date.Equal(b.Date) {
			return a.Key < b.Key
		}
		return a.Date.After(b.Date)
	})
	return result, nil
}

type searchPosition struct {
	Folder   string `json:"folder"`
	Before   uint32 `json:"before"`
	Validity uint32 `json:"validity"`
}

func (p searchPosition) encode() string {
	raw, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(raw)
}
