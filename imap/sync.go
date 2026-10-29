package imap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"

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
}

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
	n := func() {
		if notify != nil {
			notify()
		}
	}
	return &imapclient.Options{Dialer: &net.Dialer{Timeout: timeout}, WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(*imapclient.UnilateralDataMailbox) { n() }, Expunge: func(uint32) { n() }, Fetch: func(*imapclient.FetchMessageData) { n() },
	}}
}
func NewSyncClient(ctx context.Context, address, password string, notify func()) (*SyncClient, error) {
	wire, err := imapclient.DialTLS(net.JoinHostPort(imapServer, strconv.Itoa(imapPort)), syncOptions(notify))
	if err != nil {
		return nil, fmt.Errorf("connect IMAP sync: %w", err)
	}
	c := &SyncClient{wire: wire}
	c.stopCancel = context.AfterFunc(ctx, func() { _ = wire.Close() })
	if err = wire.Login(address, password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %v", ErrSyncAuthentication, err)
	}
	return c, nil
}
func (c *SyncClient) Close() error {
	if c.stopCancel != nil {
		c.stopCancel()
	}
	return c.wire.Close()
}
func (c *SyncClient) Closed() <-chan struct{} { return c.wire.Closed() }
func (c *SyncClient) CondStore() bool         { return c.wire.Caps().Has(imapv2.CapCondStore) }
func (c *SyncClient) CanIdle() bool           { return c.wire.Caps().Has(imapv2.CapIdle) }
func (c *SyncClient) StartIdle() (func() error, error) {
	if _, err := c.wire.Select("INBOX", &imapv2.SelectOptions{ReadOnly: true, CondStore: c.CondStore()}).Wait(); err != nil {
		return nil, err
	}
	idle, err := c.wire.Idle()
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			stopErr = idle.Close()
			if err := idle.Wait(); stopErr == nil {
				stopErr = err
			}
		})
		return stopErr
	}, nil
}
func (c *SyncClient) Folders() ([]FolderInfo, error) {
	options := &imapv2.ListOptions{ReturnSpecialUse: c.wire.Caps().Has(imapv2.CapSpecialUse)}
	rows, err := c.wire.List("", "*", options).Collect()
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
	data, err := c.wire.Status(name, &imapv2.StatusOptions{NumMessages: true, NumUnseen: true, UIDNext: true, UIDValidity: true, HighestModSeq: c.CondStore()}).Wait()
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
	if _, err := c.wire.Select(folder, &imapv2.SelectOptions{ReadOnly: true, CondStore: c.CondStore()}).Wait(); err != nil {
		return nil, err
	}
	data, err := c.wire.UIDSearch(&imapv2.SearchCriteria{}, nil).Wait()
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
	selected, err := c.wire.Select(folder, &imapv2.SelectOptions{ReadOnly: true, CondStore: c.CondStore()}).Wait()
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
		// shortcut: plain BODY skips the extension data iCloud sometimes malforms (the parse error kills the sync connection, which then refetches the same message forever); attachments named only in Content-Disposition lose their list indicator until opened. Upgrade if go-imap tolerates bad extension data.
		options.BodyStructure = &imapv2.FetchItemBodyStructure{}
		options.BodySection = []*imapv2.FetchItemBodySection{header, preview}
	}
	rows, err := c.wire.Fetch(set, options).Collect()
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
func (c *SyncClient) Quota() (*StorageQuota, error) {
	if !c.wire.Caps().Has(imapv2.CapQuota) {
		return nil, nil
	}
	rows, err := c.wire.GetQuotaRoot("INBOX").Wait()
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
	if row.BodyStructure != nil {
		row.BodyStructure.Walk(func(_ []int, part imapv2.BodyStructure) bool {
			if single, ok := part.(*imapv2.BodyStructureSinglePart); ok {
				if name := single.Filename(); name != "" {
					email.Attachments = append(email.Attachments, Attachment{Filename: name, Size: int64(single.Size)})
				}
			}
			return true
		})
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
