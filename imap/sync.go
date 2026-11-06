package imap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// SyncClient owns the account's read-only metadata/IDLE connection.
// The command connection remains available to browser and MCP mutations.
type SyncClient struct {
	wire       *imapclient.Client
	stopCancel func() bool
	selected   string
	selectData *imapv2.SelectData
	events     chan SyncEvent
	selectedTo *atomic.Value
	qresync    bool
}

type SyncEvent struct {
	Kind    string
	Folder  string
	UIDs    imapv2.UIDSet
	Earlier bool
}

func (e SyncEvent) HasUID(uid uint32) bool {
	return e.UIDs.Contains(imapv2.UID(uid))
}

func (e SyncEvent) Numbers() ([]uint32, bool) {
	nums, ok := e.UIDs.Nums()
	if !ok {
		return nil, false
	}
	result := make([]uint32, len(nums))
	for i, uid := range nums {
		result[i] = uint32(uid)
	}
	return result, true
}

func syncCommand[T any](c *SyncClient, name string, timeout time.Duration, run func() (T, error)) (T, error) {
	started := time.Now()
	logSlow := func(err error) {
		if elapsed := time.Since(started); elapsed >= 5*time.Second {
			folder := ""
			if c.selectedTo != nil {
				folder, _ = c.selectedTo.Load().(string)
			}
			slog.Warn("slow IMAP sync command", "command", name, "folder", folder, "duration_ms", elapsed.Milliseconds(), "error", err)
		}
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	timer := time.NewTimer(timeout)
	go func() {
		value, err := run()
		done <- result{value, err}
	}()
	select {
	case result := <-done:
		timer.Stop()
		logSlow(result.err)
		return result.value, result.err
	case <-timer.C:
		_ = c.wire.Close()
		var zero T
		err := fmt.Errorf("IMAP %s timed out after %s", name, timeout)
		logSlow(err)
		return zero, err
	}
}

const syncCommandTimeout = 20 * time.Second
const syncFetchTimeout = 60 * time.Second

var ErrSyncAuthentication = errors.New("iCloud app-specific password was rejected")

type FolderInfo struct {
	Name          string   `json:"name" bson:"name"`
	Role          string   `json:"role" bson:"role"`
	Delimiter     string   `json:"delimiter" bson:"delimiter"`
	Attributes    []string `json:"attributes" bson:"attributes"`
	UIDValidity   uint32   `json:"uidValidity" bson:"uid_validity"`
	UIDNext       uint32   `json:"uidNext" bson:"uid_next"`
	HighestModSeq uint64   `json:"highestModSeq" bson:"highest_mod_seq"`
	Total         uint32   `json:"total" bson:"total"`
	Unread        uint32   `json:"unread" bson:"unread"`
}
type StorageQuota struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

func syncOptions(notify func()) *imapclient.Options {
	return syncOptionsWithEvents(notify, nil, nil)
}

func syncOptionsWithEvents(notify func(), events chan<- SyncEvent, selected *atomic.Value) *imapclient.Options {
	n := func() {
		if notify != nil {
			notify()
		}
	}
	emit := func(kind string, uids imapv2.UIDSet, earlier bool) {
		if events != nil {
			folder := ""
			if selected != nil {
				if value := selected.Load(); value != nil {
					folder, _ = value.(string)
				}
			}
			select {
			case events <- SyncEvent{Kind: kind, Folder: folder, UIDs: uids, Earlier: earlier}:
				return
			default:
			}
		}
		n()
	}
	return &imapclient.Options{Dialer: &net.Dialer{Timeout: timeout}, WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(*imapclient.UnilateralDataMailbox) { emit("mailbox", nil, false) },
		Expunge: func(uint32) { emit("expunge", nil, false) },
		Fetch: func(message *imapclient.FetchMessageData) {
			var uids imapv2.UIDSet
			for item := message.Next(); item != nil; item = message.Next() {
				if data, ok := item.(imapclient.FetchItemDataUID); ok {
					uids.AddNum(data.UID)
				}
			}
			emit("fetch", uids, false)
		},
		Vanished: func(uids imapv2.UIDSet, earlier bool) {
			emit("vanished", uids, earlier)
		},
	}}
}
func NewSyncClient(ctx context.Context, address, password string, notify func()) (*SyncClient, error) {
	events := make(chan SyncEvent, 64)
	selected := &atomic.Value{}
	selected.Store("")
	wire, err := imapclient.DialTLS(net.JoinHostPort(imapServer, strconv.Itoa(imapPort)), syncOptionsWithEvents(notify, events, selected))
	if err != nil {
		return nil, fmt.Errorf("connect IMAP sync: %w", err)
	}
	c := &SyncClient{wire: wire, events: events, selectedTo: selected}
	c.stopCancel = context.AfterFunc(ctx, func() { _ = wire.Close() })
	if _, err = syncCommand(c, "LOGIN", syncCommandTimeout, func() (struct{}, error) { return struct{}{}, wire.Login(address, password).Wait() }); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %v", ErrSyncAuthentication, err)
	}
	if wire.Caps().Has(imapv2.CapEnable) && wire.Caps().Has(imapv2.CapQResync) {
		if _, err = syncCommand(c, "ENABLE QRESYNC", syncCommandTimeout, func() (*imapclient.EnableData, error) { return wire.Enable(imapv2.CapQResync).Wait() }); err != nil {
			_ = c.Close()
			return nil, err
		}
		c.qresync = true
	}
	return c, nil
}
func (c *SyncClient) Close() error {
	if c.stopCancel != nil {
		c.stopCancel()
	}
	return c.wire.Close()
}
func (c *SyncClient) Closed() <-chan struct{}  { return c.wire.Closed() }
func (c *SyncClient) Events() <-chan SyncEvent { return c.events }
func (c *SyncClient) CondStore() bool          { return c.wire.Caps().Has(imapv2.CapCondStore) }
func (c *SyncClient) CanIdle() bool            { return c.wire.Caps().Has(imapv2.CapIdle) }
func (c *SyncClient) selectFolder(name string) (*imapv2.SelectData, error) {
	return c.selectFolderOptions(name, &imapv2.SelectOptions{ReadOnly: true, CondStore: c.CondStore()})
}

func (c *SyncClient) selectFolderOptions(name string, options *imapv2.SelectOptions) (*imapv2.SelectData, error) {
	if c.selected == name && c.selectData != nil {
		return c.selectData, nil
	}
	if c.selectedTo != nil {
		c.selectedTo.Store(name)
	}
	data, err := syncCommand(c, "SELECT", syncCommandTimeout, func() (*imapv2.SelectData, error) {
		return c.wire.Select(name, options).Wait()
	})
	if err != nil {
		c.selected, c.selectData = "", nil
		if c.selectedTo != nil {
			c.selectedTo.Store("")
		}
		return nil, err
	}
	c.selected, c.selectData = name, data
	return data, nil
}

func (c *SyncClient) SelectQResync(name string, uidValidity uint32, modSeq uint64) error {
	options := &imapv2.SelectOptions{ReadOnly: true, CondStore: c.CondStore()}
	if c.qresync && uidValidity > 0 && modSeq > 0 {
		options.QResync = &imapv2.SelectOptionsQResync{UIDValidity: uidValidity, ModSeq: modSeq}
	}
	_, err := c.selectFolderOptions(name, options)
	return err
}
func (c *SyncClient) StartIdle() (func() error, error) {
	if _, err := c.selectFolder("INBOX"); err != nil {
		return nil, err
	}
	idle, err := syncCommand(c, "IDLE", syncCommandTimeout, c.wire.Idle)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			stopErr = idle.Close()
			if _, err := syncCommand(c, "IDLE", syncCommandTimeout, func() (struct{}, error) { return struct{}{}, idle.Wait() }); stopErr == nil {
				stopErr = err
			}
		})
		return stopErr
	}, nil
}
func (c *SyncClient) FolderStatuses() ([]FolderInfo, error) {
	status := &imapv2.StatusOptions{NumMessages: true, NumUnseen: true, UIDNext: true, UIDValidity: true, HighestModSeq: c.CondStore()}
	if !c.wire.Caps().Has(imapv2.CapListStatus) {
		folders, err := c.Folders()
		if err != nil {
			return nil, err
		}
		for i := range folders {
			updated, err := c.Status(folders[i].Name)
			if err != nil {
				return nil, err
			}
			updated.Role, updated.Delimiter, updated.Attributes = folders[i].Role, folders[i].Delimiter, folders[i].Attributes
			folders[i] = updated
		}
		return folders, nil
	}
	rows, err := syncCommand(c, "LIST STATUS", syncCommandTimeout, func() ([]*imapv2.ListData, error) {
		return c.wire.List("", "*", &imapv2.ListOptions{ReturnStatus: status, ReturnSpecialUse: c.wire.Caps().Has(imapv2.CapSpecialUse)}).Collect()
	})
	if err != nil {
		return nil, err
	}
	folders := make([]FolderInfo, 0, len(rows))
	for _, row := range rows {
		if slices.Contains(row.Attrs, imapv2.MailboxAttrNoSelect) || row.Status == nil {
			continue
		}
		attrs := make([]string, len(row.Attrs))
		for i, attr := range row.Attrs {
			attrs[i] = string(attr)
		}
		data := row.Status
		folder := FolderInfo{Name: row.Mailbox, Role: folderRole(row.Mailbox, attrs), Delimiter: string(row.Delim), UIDValidity: data.UIDValidity, UIDNext: uint32(data.UIDNext), HighestModSeq: data.HighestModSeq, Attributes: attrs}
		if data.NumMessages != nil {
			folder.Total = *data.NumMessages
		}
		if data.NumUnseen != nil {
			folder.Unread = *data.NumUnseen
		}
		folders = append(folders, folder)
	}
	slices.SortStableFunc(folders, func(a, b FolderInfo) int {
		if a.Role == "inbox" {
			return -1
		}
		if b.Role == "inbox" {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return folders, nil
}
func (c *SyncClient) Folders() ([]FolderInfo, error) {
	options := &imapv2.ListOptions{ReturnSpecialUse: c.wire.Caps().Has(imapv2.CapSpecialUse)}
	rows, err := syncCommand(c, "LIST", syncCommandTimeout, func() ([]*imapv2.ListData, error) { return c.wire.List("", "*", options).Collect() })
	if err != nil {
		return nil, err
	}
	folders := []FolderInfo{}
	for _, row := range rows {
		attrs := make([]string, len(row.Attrs))
		skip := false
		for i, attr := range row.Attrs {
			attrs[i] = string(attr)
			if attr == imapv2.MailboxAttrNoSelect {
				skip = true
			}
		}
		if skip {
			continue
		}
		folders = append(folders, FolderInfo{Name: row.Mailbox, Role: folderRole(row.Mailbox, attrs), Delimiter: string(row.Delim), Attributes: attrs})
	}
	slices.SortStableFunc(folders, func(a, b FolderInfo) int {
		if a.Role == "inbox" {
			return -1
		}
		if b.Role == "inbox" {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return folders, nil
}
func folderRole(name string, attrs []string) string {
	roles := map[string]string{`\inbox`: "inbox", `\drafts`: "drafts", `\sent`: "sent", `\archive`: "archive", `\junk`: "junk", `\trash`: "trash", `\snoozed`: "snoozed"}
	for _, attr := range attrs {
		if role := roles[strings.ToLower(attr)]; role != "" {
			return role
		}
	}
	return map[string]string{"inbox": "inbox", "drafts": "drafts", "sent": "sent", "sent messages": "sent", "archive": "archive", "junk": "junk", "spam": "junk", "deleted messages": "trash", "trash": "trash", "snoozed": "snoozed"}[strings.ToLower(name)]
}
func (c *SyncClient) Status(name string) (FolderInfo, error) {
	data, err := syncCommand(c, "STATUS", syncCommandTimeout, func() (*imapv2.StatusData, error) {
		return c.wire.Status(name, &imapv2.StatusOptions{NumMessages: true, NumUnseen: true, UIDNext: true, UIDValidity: true, HighestModSeq: c.CondStore()}).Wait()
	})
	if err != nil {
		return FolderInfo{}, err
	}
	f := FolderInfo{Name: name, Role: folderRole(name, nil), UIDValidity: data.UIDValidity, UIDNext: uint32(data.UIDNext), HighestModSeq: data.HighestModSeq}
	if data.NumMessages != nil {
		f.Total = *data.NumMessages
	}
	if data.NumUnseen != nil {
		f.Unread = *data.NumUnseen
	}
	return f, nil
}
func (c *SyncClient) UIDs(folder string) ([]uint32, error) {
	if _, err := c.selectFolder(folder); err != nil {
		return nil, err
	}
	data, err := syncCommand(c, "UID SEARCH", syncCommandTimeout, func() (*imapv2.SearchData, error) { return c.wire.UIDSearch(&imapv2.SearchCriteria{}, nil).Wait() })
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	out := make([]uint32, len(uids))
	for i, uid := range uids {
		out[i] = uint32(uid)
	}
	slices.Sort(out)
	return out, nil
}
func (c *SyncClient) Fetch(folder string, uids []uint32, since uint64, flagsOnly bool) ([]Email, error) {
	if len(uids) == 0 {
		return []Email{}, nil
	}
	selected, err := c.selectFolder(folder)
	if err != nil {
		return nil, err
	}
	var set imapv2.UIDSet
	for _, uid := range uids {
		set.AddNum(imapv2.UID(uid))
	}
	condstore := c.CondStore() && selected.HighestModSeq > 0
	options := &imapv2.FetchOptions{UID: true, Flags: true, ModSeq: condstore}
	if condstore {
		options.ChangedSince = since
	}
	header := &imapv2.FetchItemBodySection{Specifier: imapv2.PartSpecifierHeader, Peek: true, HeaderFields: []string{"References", "In-Reply-To", "List-Id", "List-Unsubscribe", "List-Unsubscribe-Post", "Auto-Submitted", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"}}
	preview := &imapv2.FetchItemBodySection{Specifier: imapv2.PartSpecifierText, Peek: true, Partial: &imapv2.SectionPartial{Size: 512}}
	if !flagsOnly {
		options.Envelope = true
		options.InternalDate = true
		options.RFC822Size = true
		// No BODYSTRUCTURE: iCloud returns structures go-imap rejects, and the parse error kills the sync connection, which then refetches the same message forever.
		options.BodySection = []*imapv2.FetchItemBodySection{header, preview}
	}
	rows, err := syncCommand(c, "UID FETCH", syncFetchTimeout, func() ([]*imapclient.FetchMessageBuffer, error) { return c.wire.Fetch(set, options).Collect() })
	if err != nil {
		return nil, err
	}
	out := make([]Email, 0, len(rows))
	for _, row := range rows {
		email := syncEmail(row, folder, selected.UIDValidity)
		if !flagsOnly {
			readSyncHeaders(&email, row.FindBodySection(header))
			email.Snippet = syncSnippet(row.FindBodySection(header), row.FindBodySection(preview))
		}
		out = append(out, email)
	}
	return out, nil
}

func (c *SyncClient) FetchEnvelope(folder string, uid uint32) (Email, error) {
	selected, err := c.selectFolder(folder)
	if err != nil {
		return Email{}, err
	}
	var set imapv2.UIDSet
	set.AddNum(imapv2.UID(uid))
	header := &imapv2.FetchItemBodySection{Specifier: imapv2.PartSpecifierHeader, Peek: true}
	options := &imapv2.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true, BodySection: []*imapv2.FetchItemBodySection{header}}
	rows, err := syncCommand(c, "UID FETCH HEADER", syncCommandTimeout, func() ([]*imapclient.FetchMessageBuffer, error) {
		return c.wire.Fetch(set, options).Collect()
	})
	if err != nil {
		return Email{}, err
	}
	if len(rows) != 1 || uint32(rows[0].UID) != uid {
		return Email{}, errors.New("raw header FETCH returned no matching UID")
	}
	email := syncEmail(rows[0], folder, selected.UIDValidity)
	readSyncHeaders(&email, rows[0].FindBodySection(header))
	return email, nil
}

func (c *SyncClient) Quota() (*StorageQuota, error) {
	if !c.wire.Caps().Has(imapv2.CapQuota) {
		return nil, nil
	}
	rows, err := syncCommand(c, "GETQUOTAROOT", syncCommandTimeout, func() ([]imapclient.QuotaData, error) { return c.wire.GetQuotaRoot("INBOX").Wait() })
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if storage, ok := row.Resources[imapv2.QuotaResourceStorage]; ok {
			return &StorageQuota{Used: storage.Usage * 1024, Limit: storage.Limit * 1024}, nil
		}
	}
	return nil, nil
}
func syncEmail(row *imapclient.FetchMessageBuffer, folder string, validity uint32) Email {
	flags := make([]string, len(row.Flags))
	for i, flag := range row.Flags {
		flags[i] = string(flag)
	}
	email := Email{ID: strconv.FormatUint(uint64(row.UID), 10), Folder: folder, UIDValidity: validity, Date: row.InternalDate, Flags: flags, Unread: !slices.Contains(row.Flags, imapv2.FlagSeen), FlagColor: flagColor(flags), Size: row.RFC822Size, ModSeq: row.ModSeq}
	addresses := func(list []imapv2.Address) []string {
		result := []string{}
		for _, a := range list {
			if a.Addr() != "" {
				result = append(result, displayAddress(a.Name, a.Addr()))
			}
		}
		return result
	}
	if e := row.Envelope; e != nil {
		email.Subject = e.Subject
		from := addresses(e.From)
		if len(from) > 0 {
			email.From = from[0]
		}
		email.To = addresses(e.To)
		email.CC = addresses(e.Cc)
		email.BCC = addresses(e.Bcc)
		email.MessageID = strings.Trim(e.MessageID, "<>")
		email.References = append([]string(nil), e.InReplyTo...)
		if !e.Date.IsZero() {
			email.Date = e.Date
		}
	}
	return email
}
func readSyncHeaders(email *Email, raw []byte) {
	header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	if err != nil {
		return
	}
	if references := header.Get("References"); references != "" {
		email.References = messageIDs(references)
	}
	email.ListID = header.Get("List-Id")
	email.Unsubscribe = header.Get("List-Unsubscribe")
	email.UnsubscribePost = header.Get("List-Unsubscribe-Post")
	email.AutoSubmitted = header.Get("Auto-Submitted")
	// shortcut: multipart/mixed stands in for attachment presence in list metadata; opening the message lists the real files.
	if media, _, err := mime.ParseMediaType(header.Get("Content-Type")); err == nil && media == "multipart/mixed" {
		email.HasAttachments = true
	}
}
func messageIDs(value string) []string {
	var ids []string
	for _, part := range strings.Fields(value) {
		id := strings.Trim(part, "<>")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
func syncSnippet(header, body []byte) string {
	reader, err := gomail.CreateReader(bytes.NewReader(append(append([]byte(nil), header...), body...)))
	if err != nil {
		return ""
	}
	var plain string
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		inline, ok := part.Header.(*gomail.InlineHeader)
		if !ok {
			continue
		}
		typ, _, _ := inline.ContentType()
		content, _ := io.ReadAll(io.LimitReader(part.Body, 2048))
		if typ == "text/plain" {
			plain = string(content)
			break
		}
		if typ == "text/html" {
			z := html.NewTokenizer(bytes.NewReader(content))
			var text strings.Builder
			skip := false
			for {
				token := z.Next()
				if token == html.ErrorToken {
					break
				}
				if token == html.StartTagToken || token == html.EndTagToken {
					tag, _ := z.TagName()
					if string(tag) == "style" || string(tag) == "script" {
						skip = token == html.StartTagToken
					}
				}
				if token == html.TextToken && !skip {
					text.Write(z.Text())
					text.WriteByte(' ')
				}
			}
			plain = text.String()
		}
	}
	text := strings.Join(strings.Fields(plain), " ")
	runes := []rune(strings.ToValidUTF8(text, ""))
	if len(runes) > 200 {
		return string(runes[:197]) + "…"
	}
	return string(runes)
}

// displayAddress keeps decoded UTF-8 names readable; mail.Address.String re-encodes them as RFC 2047 words.
func displayAddress(name, address string) string {
	if name == "" {
		return address
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `" <` + address + ">"
}
