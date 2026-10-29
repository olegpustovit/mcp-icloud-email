package mailsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
)

type searchWire struct{ *moveWire }

func (w *searchWire) SearchPage(ctx context.Context, folder, query string, filters imap.EmailFilters) (imap.SearchPage, error) {
	if err := ctx.Err(); err != nil {
		return imap.SearchPage{}, err
	}
	if filters.ExpectedValidity != 0 && filters.ExpectedValidity != 7 {
		return imap.SearchPage{}, imap.ErrLocationChanged
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var rows []imap.Email
	for id, mail := range w.mail[folder] {
		uid, _ := strconv.ParseUint(id, 10, 32)
		if filters.BeforeUID > 0 && uint32(uid) >= filters.BeforeUID {
			continue
		}
		mail.Folder = folder
		mail.UIDValidity = 7
		rows = append(rows, mail)
	}
	sort.Slice(rows, func(i, j int) bool { a, _ := strconv.Atoi(rows[i].ID); b, _ := strconv.Atoi(rows[j].ID); return a > b })
	page := imap.SearchPage{Total: len(rows), UIDValidity: 7}
	page.Messages = rows[:min(filters.Limit, len(rows))]
	if len(rows) > filters.Limit {
		uid, _ := strconv.ParseUint(page.Messages[len(page.Messages)-1].ID, 10, 32)
		page.NextUID = uint32(uid)
	}
	return page, nil
}
func TestRemoteSearchPaginationRolesIdentityAndOwnerIsolation(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {}, "Archive": {}, "Deleted Messages": {}}, next: map[string]uint32{"INBOX": 2, "Archive": 502, "Deleted Messages": 2}}
	for folder, count := range map[string]int{"INBOX": 1, "Archive": 501, "Deleted Messages": 1} {
		for i := 1; i <= count; i++ {
			id := fmt.Sprint(i)
			f.mail[folder][id] = imap.Email{ID: id, From: "mira@example.com", To: []string{"me@example.com"}, Subject: "Needle " + id, BodyPlain: "needle in body", Date: time.Now().Add(-time.Duration(i) * time.Minute)}
		}
	}
	connectMoveFake(s, f)
	s.dialCommand = func(_ context.Context, address, _ string) (*Connection, error) {
		return &Connection{Mail: &searchWire{f}, Address: address}, nil
	}
	box, err := s.Mailbox(ctx, "owner", "web")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		folders, err := s.Folders(ctx, "owner", box.account)
		if err != nil || len(folders) != 3 {
			return false
		}
		for _, folder := range folders {
			if !folder.Complete {
				return false
			}
		}
		return true
	})
	foreign := Metadata{Key: messageKey("other", box.account, "Archive", 7, "1"), Owner: "other", Account: box.account, Email: imap.Email{ID: "1", Folder: "Archive", UIDValidity: 7, Subject: "Foreign subject", ThreadID: "foreign-thread"}}
	if _, err = s.db.Collection("mail_messages").InsertOne(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	next := ""
	pages := 0
	for {
		result, err := s.RemoteSearch(ctx, "owner", box.account, "needle", next)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if pages > 10 {
			t.Fatal("search cursor did not terminate")
		}
		if len(result.Messages) > 200 {
			t.Fatal("unbounded search response")
		}
		for _, row := range result.Messages {
			if seen[row.Key] {
				t.Fatal("repeated search row")
			}
			seen[row.Key] = true
			if row.Owner != "owner" || row.Account != box.account || row.ThreadID == "foreign-thread" || row.Folder == "Deleted Messages" || row.BodyPlain != "" || row.BodyHTML != "" {
				t.Fatalf("invalid search scope or payload: %+v", row)
			}
			if row.Key != messageKey("owner", box.account, row.Folder, 7, row.ID) {
				t.Fatal("UID identity changed")
			}
		}
		if pages == 1 {
			f.mu.Lock()
			delete(f.mail["Archive"], "500")
			f.mu.Unlock()
		}
		if pages == 2 {
			f.mu.Lock()
			f.mail["Archive"]["999"] = imap.Email{ID: "999", Subject: "Needle arrival", Date: time.Now(), BodyPlain: "needle"}
			f.mu.Unlock()
		}

		if result.More != (result.Next != "") {
			t.Fatal("inconsistent cursor")
		}
		next = result.Next
		if next == "" {
			break
		}
	}
	if len(seen) != 502 || pages != 4 {
		t.Fatalf("got %d messages in %d pages", len(seen), pages)
	}
	if seen[messageKey("owner", box.account, "Archive", 7, "999")] {
		t.Fatal("new arrival changed the existing UID search boundary")
	}
	reset, resetErr := s.RemoteSearch(ctx, "owner", box.account, "needle", searchPosition{Folder: "Archive", Before: 302, Validity: 99}.encode())
	if resetErr != nil || !reset.Reset {
		t.Fatal("changed mailbox generation did not reset search", resetErr)
	}

	scoped, err := s.RemoteSearch(ctx, "owner", box.account, "in:archive needle", "")
	if err != nil || len(scoped.Messages) != 200 {
		t.Fatal(err, len(scoped.Messages))
	}
	for _, mail := range scoped.Messages {
		if mail.Folder != "Archive" {
			t.Fatal("folder operator ignored")
		}
	}
	for _, cursor := range []string{"bad", "-1:0", "0:-1", "999:0"} {
		if _, err = s.RemoteSearch(ctx, "owner", box.account, "needle", cursor); err == nil {
			t.Fatal("invalid cursor accepted", cursor)
		}
	}
	if _, err = s.RemoteSearch(ctx, "owner", "other@icloud.com", "needle", ""); err == nil {
		t.Fatal("changed account was searched")
	}
	if _, err = s.db.Collection("mail_folder_ops").InsertOne(ctx, folderOperation{ID: "pending", Owner: "owner", Account: box.account, State: "pending", NextAttempt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RemoteSearch(ctx, "owner", box.account, "needle", ""); !errors.Is(err, ErrRenamePending) {
		t.Fatal("search raced a rename", err)
	}
}
