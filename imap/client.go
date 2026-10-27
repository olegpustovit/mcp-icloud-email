package imap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	message "github.com/emersion/go-message/mail"
	"github.com/google/uuid"
)

const (
	imapServer     = "imap.mail.me.com"
	imapPort       = 993
	timeout        = 30 * time.Second
	maxMessageSize = 20 << 20
)

func parseUID(id string) (uint32, error) {
	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil || uid == 0 {
		return 0, fmt.Errorf("invalid email ID %q: expected a positive numeric UID", id)
	}
	return uint32(uid), nil
}

// Client wraps the IMAP client with iCloud-specific functionality
type Client struct {
	mu       sync.Mutex
	client   *client.Client
	username string
}

// Email represents a complete email message
type Email struct {
	ID          string       `json:"id"`
	From        string       `json:"from"`
	To          []string     `json:"to"`
	CC          []string     `json:"cc"`
	BCC         []string     `json:"bcc"`
	Subject     string       `json:"subject"`
	Date        time.Time    `json:"date"`
	BodyPlain   string       `json:"bodyPlain,omitempty"`
	BodyHTML    string       `json:"bodyHTML,omitempty"`
	Snippet     string       `json:"snippet,omitempty"`
	Unread      bool         `json:"unread"`
	Attachments []Attachment `json:"attachments,omitempty"`
	MessageID   string       `json:"messageId,omitempty"`
	References  []string     `json:"references,omitempty"`
}

// Attachment represents an email attachment
type Attachment struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// AttachmentData contains full attachment data including content
type AttachmentData struct {
	Filename string
	Content  []byte
	MIMEType string
	Size     int64
}

// DraftOptions contains options for saving drafts
type DraftOptions struct {
	CC        []string
	BCC       []string
	HTML      bool
	ReplyToID string
	Folder    string
}

// EmailFilters contains filter options for searching emails
type EmailFilters struct {
	LastDays   int
	Since      *time.Time
	Before     *time.Time
	UnreadOnly bool
	Limit      int
	Offset     int
}

// NewClient creates a new IMAP client configured for iCloud
func NewClient(email, password string) (*Client, error) {
	// Connect to iCloud IMAP server with TLS
	addr := fmt.Sprintf("%s:%d", imapServer, imapPort)
	c, err := client.DialWithDialerTLS(&net.Dialer{Timeout: timeout}, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	c.Timeout = timeout

	// Login
	if err := c.Login(email, password); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	// Test connection by selecting INBOX
	if _, err := c.Select("INBOX", false); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}

	return &Client{
		client:   c,
		username: email,
	}, nil
}

// Close closes the IMAP connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client.Logout()
	}
	return nil
}

// ListFolders lists all available mailboxes/folders
func (c *Client) ListFolders(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listFolders()
}

// listFolders is the internal implementation (caller must hold c.mu)
func (c *Client) listFolders() ([]string, error) {
	mailboxes := make(chan *imap.MailboxInfo, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.client.List("", "*", mailboxes)
	}()

	folders := []string{}
	for m := range mailboxes {
		folders = append(folders, m.Name)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to list folders: %w", err)
	}

	return folders, nil
}

// SearchEmails searches for emails in a folder with filters
func (c *Client) SearchEmails(ctx context.Context, folder, query string, filters EmailFilters) ([]Email, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return nil, 0, fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Build search criteria
	criteria := imap.NewSearchCriteria()

	// Apply date filters
	if filters.Since != nil {
		criteria.Since = *filters.Since
	} else if filters.LastDays > 0 {
		since := time.Now().AddDate(0, 0, -filters.LastDays)
		criteria.Since = since
	}

	if filters.Before != nil {
		criteria.Before = *filters.Before
	}

	// Apply unread filter
	if filters.UnreadOnly {
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}

	// Apply text search if provided
	if query != "" {
		criteria.Text = []string{query}
	}

	// Search for messages
	uids, err := c.client.UidSearch(criteria)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search emails: %w", err)
	}

	total := len(uids)
	if total == 0 {
		return []Email{}, 0, nil
	}

	// Apply offset and limit (UIDs are ascending, most recent = highest)
	if filters.Offset > 0 && filters.Offset < len(uids) {
		uids = uids[:len(uids)-filters.Offset]
	} else if filters.Offset >= len(uids) {
		return []Email{}, total, nil
	}
	if filters.Limit > 0 && len(uids) > filters.Limit {
		uids = uids[len(uids)-filters.Limit:]
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	// Fetch envelope and flags for the messages
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.client.UidFetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid}, messages)
	}()

	emails := []Email{}
	for msg := range messages {
		email := c.parseMessageData(msg)
		if email != nil {
			emails = append(emails, *email)
		}
	}

	if err := <-done; err != nil {
		return nil, 0, fmt.Errorf("failed to fetch messages: %w", err)
	}

	return emails, total, nil
}

// GetEmail retrieves a full email by UID
func (c *Client) GetEmail(ctx context.Context, folder, emailID string) (*Email, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getEmail(folder, emailID)
}

// getEmail is the internal implementation (caller must hold c.mu)
func (c *Client) getEmail(folder, emailID string) (*Email, error) {
	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return nil, fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Parse UID
	uid, err := parseUID(emailID)
	if err != nil {
		return nil, err
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	// Fetch full message
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, maxMessageSize + 1}}
	go func() {
		done <- c.client.UidFetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid, section.FetchItem()}, messages)
	}()

	msg := <-messages
	if msg == nil {
		if err := <-done; err != nil {
			return nil, fmt.Errorf("failed to fetch message: %w", err)
		}
		return nil, fmt.Errorf("email not found")
	}

	email := c.parseMessageData(msg)

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	if email == nil {
		return nil, fmt.Errorf("failed to parse email")
	}
	var literal imap.Literal
	for _, body := range msg.Body {
		literal = body
		break
	}
	if literal == nil {
		return nil, fmt.Errorf("failed to get message body")
	}
	body, err := readBoundedBody(literal)
	if err != nil {
		return nil, err
	}
	if err := c.parseEmailBody(email, bytes.NewReader(body)); err != nil {
		return nil, fmt.Errorf("failed to parse email body: %w", err)
	}

	return email, nil
}

// CountEmails counts emails matching filters
func (c *Client) CountEmails(ctx context.Context, folder string, filters EmailFilters) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.countEmails(folder, filters)
}

// countEmails is the internal implementation (caller must hold c.mu)
func (c *Client) countEmails(folder string, filters EmailFilters) (int, error) {
	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return 0, fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Build search criteria
	criteria := imap.NewSearchCriteria()

	if filters.Since != nil {
		criteria.Since = *filters.Since
	} else if filters.LastDays > 0 {
		since := time.Now().AddDate(0, 0, -filters.LastDays)
		criteria.Since = since
	}

	if filters.Before != nil {
		criteria.Before = *filters.Before
	}

	if filters.UnreadOnly {
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}

	// Search for messages
	uids, err := c.client.UidSearch(criteria)
	if err != nil {
		return 0, fmt.Errorf("failed to search emails: %w", err)
	}

	return len(uids), nil
}

// MarkRead marks an email as read or unread
func (c *Client) MarkRead(ctx context.Context, folder, emailID string, read bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Parse UID
	uid, err := parseUID(emailID)
	if err != nil {
		return err
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	// Store flags
	var item imap.StoreItem
	if read {
		item = imap.FormatFlagsOp(imap.AddFlags, true)
	} else {
		item = imap.FormatFlagsOp(imap.RemoveFlags, true)
	}

	flags := []interface{}{imap.SeenFlag}
	if err := c.client.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark email: %w", err)
	}

	return nil
}

// MoveEmail moves an email from one folder to another
func (c *Client) MoveEmail(ctx context.Context, fromFolder, toFolder, emailID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.moveEmail(fromFolder, toFolder, emailID)
}

// moveEmail is the internal implementation (caller must hold c.mu)
func (c *Client) moveEmail(fromFolder, toFolder, emailID string) error {
	// Select the source mailbox
	if _, err := c.client.Select(fromFolder, false); err != nil {
		return fmt.Errorf("failed to select folder %s: %w", fromFolder, err)
	}

	// Parse UID
	var uid uint32
	if _, err := fmt.Sscanf(emailID, "%d", &uid); err != nil {
		return fmt.Errorf("invalid email ID format: %w", err)
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	moveSupported, err := c.client.Support("MOVE")
	if err != nil {
		return fmt.Errorf("failed to check MOVE support: %w", err)
	}
	if !moveSupported {
		return fmt.Errorf("server does not support safe MOVE; refusing copy/delete fallback")
	}
	if err := c.client.UidMove(seqSet, toFolder); err != nil {
		return fmt.Errorf("failed to move email: %w", err)
	}

	return nil
}

// DeleteEmail deletes an email (moves to trash or permanently deletes)
func (c *Client) DeleteEmail(ctx context.Context, folder, emailID string, permanent bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if permanent {
		// Select the mailbox
		if _, err := c.client.Select(folder, false); err != nil {
			return fmt.Errorf("failed to select folder %s: %w", folder, err)
		}

		// Parse UID
		uid, err := parseUID(emailID)
		if err != nil {
			return err
		}

		// Create sequence set
		seqSet := new(imap.SeqSet)
		seqSet.AddNum(uid)

		// UID EXPUNGE prevents removing other messages marked deleted by another client.
		supported, err := c.client.Support("UIDPLUS")
		if err != nil {
			return fmt.Errorf("failed to check UIDPLUS support: %w", err)
		}
		if !supported {
			return fmt.Errorf("server does not support safe permanent deletion (UIDPLUS required)")
		}

		// Mark as deleted
		item := imap.FormatFlagsOp(imap.AddFlags, true)
		flags := []interface{}{imap.DeletedFlag}
		if err := c.client.UidStore(seqSet, item, flags, nil); err != nil {
			return fmt.Errorf("failed to mark email as deleted: %w", err)
		}

		// Expunge to permanently delete
		if err := c.expungeUID(uid); err != nil {
			return fmt.Errorf("failed to expunge: %w", err)
		}
	} else {
		// Move to Trash folder (use internal moveEmail to avoid deadlock)
		trashFolder := "Deleted Messages"
		if err := c.moveEmail(folder, trashFolder, emailID); err != nil {
			// Try alternate trash folder name
			trashFolder = "Trash"
			if err := c.moveEmail(folder, trashFolder, emailID); err != nil {
				return fmt.Errorf("failed to move to trash: %w", err)
			}
		}
	}

	return nil
}

// expungeUID removes only the requested deleted message; EXPUNGE would remove every deleted message in the mailbox.
func (c *Client) expungeUID(uid uint32) error {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)
	status, err := c.client.Execute(&imap.Command{Name: "UID", Arguments: []interface{}{imap.RawString("EXPUNGE"), seqSet}}, nil)
	if err != nil {
		return err
	}
	return status.Err()
}

// parseMessageData parses IMAP message data into Email struct
func (c *Client) parseMessageData(msg *imap.Message) *Email {
	if msg.Envelope == nil {
		return nil
	}

	// Check if message has Seen flag
	unread := true
	for _, flag := range msg.Flags {
		if flag == imap.SeenFlag {
			unread = false
			break
		}
	}

	email := &Email{
		ID:      fmt.Sprintf("%d", msg.Uid),
		Subject: msg.Envelope.Subject,
		Date:    msg.Envelope.Date,
		Unread:  unread,
	}

	// Parse From
	if len(msg.Envelope.From) > 0 {
		email.From = formatAddress(msg.Envelope.From[0])
	}

	// Parse To
	email.To = make([]string, 0, len(msg.Envelope.To))
	for _, addr := range msg.Envelope.To {
		email.To = append(email.To, formatAddress(addr))
	}

	// Parse CC
	email.CC = make([]string, 0, len(msg.Envelope.Cc))
	for _, addr := range msg.Envelope.Cc {
		email.CC = append(email.CC, formatAddress(addr))
	}

	// Parse BCC
	email.BCC = make([]string, 0, len(msg.Envelope.Bcc))
	for _, addr := range msg.Envelope.Bcc {
		email.BCC = append(email.BCC, formatAddress(addr))
	}

	// Store Message-ID
	email.MessageID = msg.Envelope.MessageId

	// Parse In-Reply-To and References
	if msg.Envelope.InReplyTo != "" {
		email.References = append(email.References, msg.Envelope.InReplyTo)
	}

	// Create snippet from subject for preview.
	if len(email.Subject) > 200 {
		email.Snippet = email.Subject[:197] + "..."
	} else {
		email.Snippet = email.Subject
	}

	return email
}

// parseEmailBody parses the email body and attachments
func (c *Client) parseEmailBody(email *Email, rawMessage io.Reader) error {
	// CreateReader needs the top-level MIME headers to resolve content type and boundary.
	mr, err := message.CreateReader(rawMessage)
	if err != nil {
		return fmt.Errorf("failed to create message reader: %w", err)
	}

	if err := c.processMessagePart(email, mr); err != nil {
		return err
	}

	// Create snippet from plain text body
	if email.BodyPlain != "" {
		snippet := strings.TrimSpace(email.BodyPlain)
		if len(snippet) > 200 {
			email.Snippet = snippet[:197] + "..."
		} else {
			email.Snippet = snippet
		}
	} else if email.BodyHTML != "" {
		// Use subject as snippet if no plain text
		snippet := email.Subject
		if len(snippet) > 200 {
			email.Snippet = snippet[:197] + "..."
		} else {
			email.Snippet = snippet
		}
	}
	return nil
}

func readBoundedBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxMessageSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read email body: %w", err)
	}
	if len(data) > maxMessageSize {
		return nil, fmt.Errorf("email message exceeds maximum size of %d bytes", maxMessageSize)
	}
	return data, nil
}

// processMessagePart recursively processes message parts
func (c *Client) processMessagePart(email *Email, mr *message.Reader) error {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read message part: %w", err)
		}

		switch h := part.Header.(type) {
		case *message.InlineHeader:
			contentType, _, _ := h.ContentType()
			body, err := io.ReadAll(part.Body)
			if err != nil {
				return fmt.Errorf("failed to read message body part: %w", err)
			}

			if strings.HasPrefix(contentType, "text/plain") {
				email.BodyPlain = string(body)
			} else if strings.HasPrefix(contentType, "text/html") {
				email.BodyHTML = string(body)
			}

		case *message.AttachmentHeader:
			filename, _ := h.Filename()
			if filename != "" {
				// Count size without reading full content
				size, err := io.Copy(io.Discard, part.Body)
				if err != nil {
					return fmt.Errorf("failed to read attachment metadata: %w", err)
				}
				email.Attachments = append(email.Attachments, Attachment{
					Filename: filename,
					Size:     size,
				})
			}

		}
	}
	return nil
}

// formatAddress formats an IMAP address into a string
func formatAddress(addr *imap.Address) string {
	if addr.PersonalName != "" {
		return fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName)
	}
	return fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName)
}

// GetUsername returns the authenticated username
func (c *Client) GetUsername() string {
	return c.username
}

// SaveDraft saves an email as a draft in the Drafts folder
func (c *Client) SaveDraft(ctx context.Context, from string, to []string, subject, body string, opts DraftOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Try common draft folder names
	draftFolders := []string{"Drafts", "INBOX.Drafts", "[Gmail]/Drafts"}
	var draftFolder string

	// Find which draft folder exists
	folders, err := c.listFolders()
	if err != nil {
		return "", fmt.Errorf("failed to list folders: %w", err)
	}

	for _, df := range draftFolders {
		for _, f := range folders {
			if f == df {
				draftFolder = df
				break
			}
		}
		if draftFolder != "" {
			break
		}
	}

	if draftFolder == "" {
		draftFolder = "Drafts" // fallback default
	}

	// Build email message
	var buf strings.Builder

	// Headers
	buf.WriteString("From: " + from + "\r\n")
	buf.WriteString("To: " + strings.Join(to, ", ") + "\r\n")

	if len(opts.CC) > 0 {
		buf.WriteString("Cc: " + strings.Join(opts.CC, ", ") + "\r\n")
	}

	if len(opts.BCC) > 0 {
		buf.WriteString("Bcc: " + strings.Join(opts.BCC, ", ") + "\r\n")
	}

	// Handle reply headers if this is a reply draft
	if opts.ReplyToID != "" {
		folder := opts.Folder
		if folder == "" {
			folder = "INBOX"
		}

		originalEmail, err := c.getEmail(folder, opts.ReplyToID)
		if err != nil {
			return "", fmt.Errorf("failed to get original email for reply: %w", err)
		}

		// Build reply subject
		var replySubject string
		if !strings.HasPrefix(strings.ToLower(originalEmail.Subject), "re:") {
			replySubject = "Re: " + originalEmail.Subject
		} else {
			replySubject = originalEmail.Subject
		}
		subject = replySubject

		// Add reply headers
		if originalEmail.MessageID != "" {
			buf.WriteString("In-Reply-To: " + originalEmail.MessageID + "\r\n")

			// Build References
			refs := originalEmail.References
			if len(refs) == 0 && originalEmail.MessageID != "" {
				refs = []string{originalEmail.MessageID}
			} else if originalEmail.MessageID != "" {
				refs = append(refs, originalEmail.MessageID)
			}
			if len(refs) > 0 {
				buf.WriteString("References: " + strings.Join(refs, " ") + "\r\n")
			}
		}
	}

	buf.WriteString("Subject: " + subject + "\r\n")
	buf.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")

	// Generate Message-ID
	messageID := fmt.Sprintf("<%s.%s@mcp-icloud-email>", uuid.New().String(), c.username)
	buf.WriteString("Message-ID: " + messageID + "\r\n")

	// Content type
	if opts.HTML {
		buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	} else {
		buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	}

	buf.WriteString("\r\n")
	buf.WriteString(body)

	// Append to Drafts folder with \Draft flag
	flags := []string{imap.DraftFlag}
	date := time.Now()

	if err := c.client.Append(draftFolder, flags, date, strings.NewReader(buf.String())); err != nil {
		return "", fmt.Errorf("failed to append draft: %w", err)
	}

	// Get the UID of the appended message (select folder and get last message)
	mbox, err := c.client.Select(draftFolder, false)
	if err != nil {
		return "", fmt.Errorf("failed to select draft folder: %w", err)
	}

	// Return the last UID as the draft ID
	draftID := fmt.Sprintf("%d", mbox.Messages)

	return draftID, nil
}

// GetAttachment downloads a specific attachment from an email
func (c *Client) GetAttachment(ctx context.Context, folder, emailID, filename string) (*AttachmentData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return nil, fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Parse UID
	uid, err := parseUID(emailID)
	if err != nil {
		return nil, err
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	// First, fetch BODYSTRUCTURE to find the attachment
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)

	go func() {
		done <- c.client.UidFetch(seqSet, []imap.FetchItem{imap.FetchBodyStructure}, messages)
	}()

	msg := <-messages
	if msg == nil {
		if err := <-done; err != nil {
			return nil, fmt.Errorf("failed to fetch email structure: %w", err)
		}
		return nil, fmt.Errorf("email not found")
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch email structure: %w", err)
	}

	// Parse BODYSTRUCTURE to find attachment part
	// This is a simplified implementation - for production, you'd need more robust parsing
	// For now, we'll fetch the entire message and parse it

	// Fetch full message body
	messages2 := make(chan *imap.Message, 1)
	done2 := make(chan error, 1)
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, maxMessageSize + 1}}

	go func() {
		done2 <- c.client.UidFetch(seqSet, []imap.FetchItem{section.FetchItem()}, messages2)
	}()

	msg2 := <-messages2
	if msg2 == nil {
		if err := <-done2; err != nil {
			return nil, fmt.Errorf("failed to fetch message body: %w", err)
		}
		return nil, fmt.Errorf("email not found")
	}

	if err := <-done2; err != nil {
		return nil, fmt.Errorf("failed to fetch message body: %w", err)
	}

	// Parse the message
	var bodyLiteral imap.Literal
	for _, literal := range msg2.Body {
		bodyLiteral = literal
		break
	}

	if bodyLiteral == nil {
		return nil, fmt.Errorf("failed to get message body")
	}
	body, err := readBoundedBody(bodyLiteral)
	if err != nil {
		return nil, err
	}

	return findAttachment(body, filename)
}

func findAttachment(rawMessage []byte, filename string) (*AttachmentData, error) {
	mr, err := message.CreateReader(bytes.NewReader(rawMessage))
	if err != nil {
		return nil, fmt.Errorf("failed to create message reader: %w", err)
	}

	// Look for the attachment
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read message part: %w", err)
		}

		if h, ok := part.Header.(*message.AttachmentHeader); ok {
			attachFilename, _ := h.Filename()
			if attachFilename == filename {
				// Found the attachment
				content, err := io.ReadAll(part.Body)
				if err != nil {
					return nil, fmt.Errorf("failed to read attachment content: %w", err)
				}

				mimeType, _, _ := h.ContentType()

				return &AttachmentData{
					Filename: attachFilename,
					Content:  content,
					MIMEType: mimeType,
					Size:     int64(len(content)),
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("attachment '%s' not found in email", filename)
}

// FlagEmail sets or removes flags on an email
func (c *Client) FlagEmail(ctx context.Context, folder, emailID, flagType, color string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Select the mailbox
	if _, err := c.client.Select(folder, false); err != nil {
		return fmt.Errorf("failed to select folder %s: %w", folder, err)
	}

	// Parse UID
	uid, err := parseUID(emailID)
	if err != nil {
		return err
	}

	// Create sequence set
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	if flagType == "none" {
		// Remove all flags
		item := imap.FormatFlagsOp(imap.RemoveFlags, true)
		flags := []interface{}{
			imap.FlaggedFlag,
			"$FollowUp",
			"$Important",
			"$Deadline",
			"$FlagRed",
			"$FlagOrange",
			"$FlagYellow",
			"$FlagGreen",
			"$FlagBlue",
			"$FlagPurple",
		}

		// Try to remove flags (may fail if keywords not supported, which is ok)
		_ = c.client.UidStore(seqSet, item, flags, nil)
		return nil
	}

	// Build flag list
	flags := []interface{}{imap.FlaggedFlag}

	// Add flag type keyword
	switch flagType {
	case "follow-up":
		flags = append(flags, "$FollowUp")
	case "important":
		flags = append(flags, "$Important")
	case "deadline":
		flags = append(flags, "$Deadline")
	default:
		return fmt.Errorf("invalid flag type: %s", flagType)
	}

	// Add color keyword if provided
	if color != "" {
		switch color {
		case "red":
			flags = append(flags, "$FlagRed")
		case "orange":
			flags = append(flags, "$FlagOrange")
		case "yellow":
			flags = append(flags, "$FlagYellow")
		case "green":
			flags = append(flags, "$FlagGreen")
		case "blue":
			flags = append(flags, "$FlagBlue")
		case "purple":
			flags = append(flags, "$FlagPurple")
		default:
			return fmt.Errorf("invalid color: %s", color)
		}
	}

	// Set the flags
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.client.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("failed to set flags: %w", err)
	}

	return nil
}

// CreateFolder creates a new mailbox folder
func (c *Client) CreateFolder(ctx context.Context, name, parent string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Construct full folder path
	folderPath := name
	if parent != "" {
		folderPath = parent + "/" + name
	}

	// Create the folder
	if err := c.client.Create(folderPath); err != nil {
		return fmt.Errorf("failed to create folder %s: %w", folderPath, err)
	}

	return nil
}

// DeleteFolder deletes a mailbox folder
func (c *Client) DeleteFolder(ctx context.Context, name string, force bool) (wasEmpty bool, emailCount int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if folder exists and count emails
	count, countErr := c.countEmails(name, EmailFilters{})
	if countErr != nil {
		// If we can't select the folder, it might not exist
		err = fmt.Errorf("failed to access folder %s: %w", name, countErr)
		return false, 0, err
	}

	// If folder is not empty and force is false, return error
	if count > 0 && !force {
		return false, count, fmt.Errorf("folder %s is not empty (contains %d emails)", name, count)
	}

	// Delete the folder
	if deleteErr := c.client.Delete(name); deleteErr != nil {
		err = fmt.Errorf("failed to delete folder %s: %w", name, deleteErr)
		return false, count, err
	}

	wasEmpty = (count == 0)
	return wasEmpty, count, nil
}
