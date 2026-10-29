package mailsync

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Mailbox struct {
	s                     *Service
	owner, account, actor string
}
type transactionKey struct{}

func Transaction(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, transactionKey{}, id)
}
func transactionID(ctx context.Context) string {
	id, _ := ctx.Value(transactionKey{}).(string)
	return id
}

func (s *Service) Mailbox(ctx context.Context, owner, actor string) (*Mailbox, error) {
	if strings.HasPrefix(actor, "mcp:") {
		s.touch(owner)
	}
	m, err := s.Start(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &Mailbox{s: s, owner: owner, account: m.account, actor: actor}, nil
}
func (m *Mailbox) ListFolders(ctx context.Context) ([]string, error) {
	if err := m.s.WaitReady(ctx, m.owner); err != nil {
		return nil, err
	}
	folders, err := m.s.Folders(ctx, m.owner, m.account)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(folders))
	for i, f := range folders {
		names[i] = f.Name
	}
	return names, nil
}
func (m *Mailbox) UIDValidity(ctx context.Context, folder string) (uint32, error) {
	connection, release, err := m.s.Acquire(ctx, m.owner)
	if err != nil {
		return 0, err
	}
	defer release()
	if connection.Address != m.account {
		return 0, errors.New("iCloud account changed")
	}
	return connection.Mail.UIDValidity(ctx, folder)
}
func (m *Mailbox) folder(ctx context.Context, name string) (Folder, error) {
	if err := m.s.WaitReady(ctx, m.owner); err != nil {
		return Folder{}, err
	}
	var folder Folder
	err := m.s.db.Collection("mail_folders").FindOne(ctx, bson.M{"owner": m.owner, "account": m.account, "info.name": name}).Decode(&folder)
	if errors.Is(err, mongo.ErrNoDocuments) {
		if err = m.s.Refresh(ctx, m.owner, "imap", "", name); err != nil {
			return folder, err
		}
		err = m.s.db.Collection("mail_folders").FindOne(ctx, bson.M{"owner": m.owner, "account": m.account, "info.name": name}).Decode(&folder)
	}
	return folder, err
}
func mailFilter(folder, query string, filters imap.EmailFilters) bson.M {
	filter := bson.M{}
	if folder != "" && folder != "*" {
		filter["email.folder"] = folder
	}
	if filters.UnreadOnly {
		filter["email.unread"] = true
	}
	date := bson.M{}
	if filters.Since != nil {
		date["$gte"] = *filters.Since
	} else if filters.LastDays > 0 {
		date["$gte"] = time.Now().AddDate(0, 0, -filters.LastDays)
	}
	if filters.Before != nil {
		date["$lt"] = *filters.Before
	}
	if len(date) > 0 {
		filter["email.date"] = date
	}
	if query != "" {
		pattern := bson.M{"$regex": regexp.QuoteMeta(query), "$options": "i"}
		filter["$or"] = []bson.M{{"email.subject": pattern}, {"email.from": pattern}, {"email.snippet": pattern}, {"email.to": pattern}}
	}
	return filter
}
func (m *Mailbox) SearchEmails(ctx context.Context, folder, query string, filters imap.EmailFilters) ([]imap.Email, int, error) {
	f, err := m.folder(ctx, folder)
	if err != nil {
		return nil, 0, err
	}
	// During import and for full-body text queries, keep the existing IMAP search contract.
	if !f.Complete || query != "" {
		connection, release, err := m.s.Acquire(ctx, m.owner)
		if err != nil {
			return nil, 0, err
		}
		defer release()
		if connection.Address != m.account {
			return nil, 0, errors.New("iCloud account changed")
		}
		mail, total, err := connection.Mail.SearchEmails(ctx, folder, query, filters)
		for i := range mail {
			mail[i].Folder = folder
		}
		return mail, total, err
	}
	filter := mailFilter(folder, query, filters)
	filter["owner"] = m.owner
	filter["account"] = m.account
	count, err := m.s.db.Collection("mail_messages").CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	find := options.Find().SetSort(bson.D{{Key: "email.date", Value: -1}, {Key: "_id", Value: 1}}).SetSkip(int64(max(filters.Offset, 0)))
	if filters.Limit > 0 {
		find.SetLimit(int64(filters.Limit))
	}
	rows, err := m.s.db.Collection("mail_messages").Find(ctx, filter, find)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close(ctx) }()
	var stored []Metadata
	if err = rows.All(ctx, &stored); err != nil {
		return nil, 0, err
	}
	mail := make([]imap.Email, len(stored))
	for i, row := range stored {
		mail[i] = row.Email
	}
	return mail, int(count), nil
}
func (m *Mailbox) CountEmails(ctx context.Context, folder string, filters imap.EmailFilters) (int, error) {
	f, err := m.folder(ctx, folder)
	if err != nil {
		return 0, err
	}
	if filters.LastDays == 0 && filters.Since == nil && filters.Before == nil {
		if filters.UnreadOnly {
			return int(f.Unread), nil
		}
		return int(f.Total), nil
	}
	if !f.Complete {
		connection, release, err := m.s.Acquire(ctx, m.owner)
		if err != nil {
			return 0, err
		}
		defer release()
		return connection.Mail.CountEmails(ctx, folder, filters)
	}
	filter := mailFilter(folder, "", filters)
	filter["owner"] = m.owner
	filter["account"] = m.account
	count, err := m.s.db.Collection("mail_messages").CountDocuments(ctx, filter)
	return int(count), err
}
func (m *Mailbox) GetEmail(ctx context.Context, folder, id string) (*imap.Email, error) {
	connection, release, err := m.s.Acquire(ctx, m.owner)
	if err != nil {
		return nil, err
	}
	defer release()
	if connection.Address != m.account {
		return nil, errors.New("iCloud account changed")
	}
	email, err := connection.Mail.GetEmail(ctx, folder, id)
	if err != nil {
		return nil, err
	}
	email.Folder = folder
	var metadata Metadata
	if err = m.s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": messageKey(m.owner, m.account, folder, email.UIDValidity, id), "owner": m.owner, "account": m.account}).Decode(&metadata); err == nil {
		email.ThreadID = metadata.ThreadID
		email.ListID = metadata.ListID
		email.Unsubscribe = metadata.Unsubscribe
		email.UnsubscribePost = metadata.UnsubscribePost
	}
	return email, nil
}
func (m *Mailbox) GetAttachment(ctx context.Context, folder, id, filename string) (*imap.AttachmentData, error) {
	connection, release, err := m.s.Acquire(ctx, m.owner)
	if err != nil {
		return nil, err
	}
	defer release()
	if connection.Address != m.account {
		return nil, errors.New("iCloud account changed")
	}
	return connection.Mail.GetAttachment(ctx, folder, id, filename)
}
func (m *Mailbox) change(ctx context.Context, folders []string, apply func(*Connection) error) error {
	return m.changeCommand(ctx, folders, false, apply)
}
func (m *Mailbox) changeCommand(ctx context.Context, folders []string, allowRename bool, apply func(*Connection) error) error {
	return m.s.change(ctx, m.owner, m.actor, transactionID(ctx), folders, func() error {
		connection, release, err := m.s.Acquire(ctx, m.owner)
		if err != nil {
			return err
		}
		defer release()
		if connection.Address != m.account {
			return errors.New("iCloud account changed")
		}
		if !allowRename {
			if err := m.s.checkRenamePending(ctx, m.owner, m.account); err != nil {
				return err
			}
		}
		return apply(connection)
	})
}
func (m *Mailbox) MarkRead(ctx context.Context, folder, id string, read bool) error {
	return m.change(ctx, []string{folder}, func(c *Connection) error { return c.Mail.MarkRead(ctx, folder, id, read) })
}
func (m *Mailbox) FlagEmail(ctx context.Context, folder, id, kind, color string) error {
	return m.change(ctx, []string{folder}, func(c *Connection) error { return c.Mail.FlagEmail(ctx, folder, id, kind, color) })
}
func (m *Mailbox) MoveEmail(ctx context.Context, from, to, id string) error {
	validity, err := m.UIDValidity(ctx, from)
	if err != nil {
		return err
	}
	return m.MoveWithValidity(ctx, imap.Location{Folder: from, ID: id, Validity: validity}, to)
}
func (m *Mailbox) DeleteEmail(ctx context.Context, folder, id string, permanent bool) error {
	if !permanent {
		folders, err := m.s.Folders(ctx, m.owner, m.account)
		if err != nil {
			return err
		}
		for _, target := range folders {
			if target.Role == "trash" {
				return m.MoveEmail(ctx, folder, target.Name, id)
			}
		}
		return errors.New("trash mailbox unavailable")
	}
	return m.change(ctx, []string{folder}, func(c *Connection) error { return c.Mail.DeleteEmail(ctx, folder, id, permanent) })
}
func (m *Mailbox) CreateFolder(ctx context.Context, name, parent string) error {
	return m.change(ctx, nil, func(c *Connection) error { return c.Mail.CreateFolder(ctx, name, parent) })
}
func (m *Mailbox) DeleteFolder(ctx context.Context, name string, force bool) (empty bool, count int, err error) {
	folders, err := m.s.Folders(ctx, m.owner, m.account)
	if err != nil {
		return false, 0, err
	}
	for _, folder := range folders {
		if folder.Name == name && folder.Role != "" {
			return false, 0, errors.New("system mailboxes cannot be deleted")
		}
	}
	err = m.change(ctx, nil, func(c *Connection) error { empty, count, err = c.Mail.DeleteFolder(ctx, name, force); return err })
	return
}
func (m *Mailbox) SaveDraft(ctx context.Context, from string, to []string, subject, body string, opts imap.DraftOptions) (id string, err error) {
	err = m.change(ctx, m.roleFolders(ctx, "drafts"), func(c *Connection) error { id, err = c.Mail.SaveDraft(ctx, from, to, subject, body, opts); return err })
	return
}

// roleFolders scopes the post-change refresh; nil (no such role yet) falls back to refreshing every folder.
func (m *Mailbox) roleFolders(ctx context.Context, role string) []string {
	folders, err := m.s.Folders(ctx, m.owner, m.account)
	if err != nil {
		return nil
	}
	for _, folder := range folders {
		if folder.Role == role {
			return []string{folder.Name}
		}
	}
	return nil
}
func (m *Mailbox) SendEmail(ctx context.Context, from string, to []string, subject, body string, opts smtp.SendOptions) error {
	return m.change(ctx, nil, func(c *Connection) error {
		identity, err := m.s.SenderIdentity(ctx, m.owner, from)
		if err != nil {
			return err
		}
		parsed, _ := mail.ParseAddress(identity)
		c.Sender.SetAliases([]string{parsed.Address})
		return c.Sender.SendEmail(ctx, identity, to, subject, body, opts)
	})
}
func (m *Mailbox) ReplyToEmail(ctx context.Context, original *imap.Email, body string, all bool, opts smtp.SendOptions) error {
	return m.change(ctx, nil, func(c *Connection) error {
		identity, err := m.s.SenderIdentity(ctx, m.owner, opts.From)
		if err != nil {
			return err
		}
		parsed, _ := mail.ParseAddress(identity)
		c.Sender.SetAliases([]string{parsed.Address})
		opts.From = identity
		return c.Sender.ReplyToEmail(ctx, original, body, all, opts)
	})
}

func (s *Service) MirrorSearch(ctx context.Context, owner, account, folder, query string, limit, offset int) ([]Metadata, int64, error) {
	if err := s.WaitReady(ctx, owner); err != nil {
		return nil, 0, err
	}
	filter := mailFilter(folder, "", imap.EmailFilters{})
	if folder == "flagged" {
		delete(filter, "email.folder")
		filter["email.flagcolor"] = bson.M{"$nin": []any{"", nil}}
	}
	if folder == "vip" {
		delete(filter, "email.folder")
		prefs, err := s.Preferences(ctx, owner)
		if err != nil {
			return nil, 0, err
		}
		senders := make([]bson.M, 0, len(prefs.VIP))
		for _, sender := range prefs.VIP {
			senders = append(senders, bson.M{"email.from": bson.M{"$regex": "(?i)(^|<)" + regexp.QuoteMeta(sender) + ">?$"}})
		}
		if len(senders) == 0 {
			return []Metadata{}, 0, nil
		}
		filter["$or"] = senders
	}
	if query != "" {
		conditions, err := searchConditions(query)
		if err != nil {
			return nil, 0, err
		}
		filter["$and"] = conditions
	}
	filter["owner"] = owner
	filter["account"] = account
	count, err := s.db.Collection("mail_messages").CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	find := options.Find().SetSort(bson.D{{Key: "email.date", Value: -1}, {Key: "_id", Value: 1}}).SetSkip(int64(max(offset, 0))).SetLimit(int64(min(max(limit, 1), 2000)))
	rows, err := s.db.Collection("mail_messages").Find(ctx, filter, find)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close(ctx) }()
	mail := []Metadata{}
	err = rows.All(ctx, &mail)
	return mail, count, err
}

var searchToken = regexp.MustCompile(`(?:[^\s"]+|"[^"]*")+`)

func searchConditions(query string) ([]bson.M, error) {
	conditions := []bson.M{}
	for _, token := range searchToken.FindAllString(query, -1) {
		key, value, hasKey := strings.Cut(token, ":")
		if !hasKey {
			value = key
			key = ""
		}
		value = strings.Trim(value, `"`)
		pattern := bson.M{"$regex": regexp.QuoteMeta(value), "$options": "i"}
		switch strings.ToLower(key) {
		case "from", "to", "subject":
			conditions = append(conditions, bson.M{"email." + strings.ToLower(key): pattern})
		case "in":
			conditions = append(conditions, bson.M{"email.folder": pattern})
		case "is":
			switch value {
			case "unread":
				conditions = append(conditions, bson.M{"email.unread": true})
			case "read":
				conditions = append(conditions, bson.M{"email.unread": false})
			case "flagged":
				conditions = append(conditions, bson.M{"email.flagcolor": bson.M{"$nin": []any{"", nil}}})
			default:
				return nil, errors.New("unknown is: search filter")
			}
		case "has":
			if value != "attachment" {
				return nil, errors.New("unknown has: search filter")
			}
			conditions = append(conditions, bson.M{"email.attachments.0": bson.M{"$exists": true}})
		case "after", "before":
			date, err := time.Parse("2006-01-02", value)
			if err != nil {
				return nil, errors.New("search dates must use YYYY-MM-DD")
			}
			op := "$gte"
			if key == "before" {
				op = "$lt"
			}
			conditions = append(conditions, bson.M{"email.date": bson.M{op: date}})
		default:
			conditions = append(conditions, bson.M{"$or": []bson.M{{"email.subject": pattern}, {"email.from": pattern}, {"email.to": pattern}, {"email.snippet": pattern}}})
		}
	}
	return conditions, nil
}
