package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	netmail "net/mail"
	netsmtp "net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/google/uuid"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmessage"
)

const (
	smtpServer = "smtp.mail.me.com"
	smtpPort   = 587
)

// Client handles SMTP operations for sending emails
type Client struct {
	username string
	password string
	submit   func(context.Context, string, []string, []byte) error
	saveSent func(context.Context, []byte) error
	aliases  []string
}

// SendOptions contains optional parameters for sending emails
type SendOptions struct {
	CC          []string
	BCC         []string
	HTML        bool
	Headers     map[string]string
	From        string
	Attachments []mailmessage.Attachment
}

// NewClient creates a new SMTP client
func NewClient(username, password string, saveSent func(context.Context, []byte) error) *Client {
	c := &Client{username: username, password: password, saveSent: saveSent}
	c.submit = c.send
	return c
}

// The account owner maintains these identities; iCloud validates ownership on send.
func (c *Client) SetAliases(addresses []string) { c.aliases = addresses }

// SentCopyError means SMTP accepted the message; callers must not retry sending it.
type SentCopyError struct{ Err error }

func (e *SentCopyError) Error() string {
	return "email sent, but saving the Sent copy failed: " + e.Err.Error()
}
func (e *SentCopyError) Unwrap() error { return e.Err }

// SendEmail sends an email via SMTP
func (c *Client) SendEmail(ctx context.Context, from string, to []string, subject, body string, opts SendOptions) error {
	if strings.ContainsAny(subject, "\r\n") || len(subject) > 1000 || len(body) > 1<<20 || len(to)+len(opts.CC)+len(opts.BCC) > 100 {
		return fmt.Errorf("invalid message headers or size")
	}
	fromAddress, err := netmail.ParseAddress(from)
	allowed := err == nil && strings.EqualFold(fromAddress.Address, c.username)
	if err == nil {
		for _, alias := range c.aliases {
			allowed = allowed || strings.EqualFold(fromAddress.Address, alias)
		}
	}
	if !allowed {
		return fmt.Errorf("from address must match the authenticated iCloud account")
	}
	to, err = envelopeAddresses(to)
	if err != nil {
		return fmt.Errorf("invalid recipient: %w", err)
	}
	opts.CC, err = envelopeAddresses(opts.CC)
	if err != nil {
		return fmt.Errorf("invalid CC recipient: %w", err)
	}
	opts.BCC, err = envelopeAddresses(opts.BCC)
	if err != nil {
		return fmt.Errorf("invalid BCC recipient: %w", err)
	}
	for key, value := range opts.Headers {
		if !strings.EqualFold(key, "In-Reply-To") && !strings.EqualFold(key, "References") && (!strings.EqualFold(key, "Auto-Submitted") || value != "auto-generated") {
			return fmt.Errorf("custom header %q is not allowed", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a line break", key)
		}
	}
	from = fromAddress.Address

	// Create message header
	var h mail.Header
	h.SetDate(time.Now())
	h.SetAddressList("From", []*mail.Address{{Name: fromAddress.Name, Address: from}})

	// Set To addresses
	toAddrs := make([]*mail.Address, 0, len(to))
	for _, addr := range to {
		toAddrs = append(toAddrs, &mail.Address{Address: addr})
	}
	h.SetAddressList("To", toAddrs)

	// Set CC addresses
	if len(opts.CC) > 0 {
		ccAddrs := make([]*mail.Address, 0, len(opts.CC))
		for _, addr := range opts.CC {
			ccAddrs = append(ccAddrs, &mail.Address{Address: addr})
		}
		h.SetAddressList("Cc", ccAddrs)
	}

	// Set BCC addresses (they go in envelope but not headers)
	// BCC is intentionally NOT added to headers

	// Set subject
	h.SetSubject(subject)

	// Generate Message-ID
	messageID := fmt.Sprintf("<%s@%s>", uuid.New().String(), smtpServer)
	h.Set("Message-ID", messageID)

	// Set custom headers
	for key, value := range opts.Headers {
		h.Set(key, value)
	}

	raw, err := mailmessage.Encode(h, body, opts.HTML, opts.Attachments)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}

	// Build recipient list (To + CC + BCC)
	recipients := make([]string, 0, len(to)+len(opts.CC)+len(opts.BCC))
	recipients = append(recipients, to...)
	recipients = append(recipients, opts.CC...)
	recipients = append(recipients, opts.BCC...)

	if c.saveSent == nil {
		return fmt.Errorf("sent mailbox storage is not configured")
	}
	if err := c.submit(ctx, from, recipients, raw); err != nil {
		return err
	}
	// Finish filing an accepted message even if the requesting client disconnects.
	archiveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := c.saveSent(archiveCtx, raw); err != nil {
		return &SentCopyError{Err: err}
	}
	return nil
}

func (c *Client) send(ctx context.Context, from string, recipients []string, message []byte) error {
	addr := fmt.Sprintf("%s:%d", smtpServer, smtpPort)
	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("failed to set SMTP deadline: %w", err)
	}
	client, err := netsmtp.NewClient(conn, smtpServer)
	if err != nil {
		return fmt.Errorf("failed to start SMTP session: %w", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("SMTP greeting failed: %w", err)
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("SMTP server does not support required STARTTLS")
	}
	if err := client.StartTLS(&tls.Config{ServerName: smtpServer, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("SMTP TLS negotiation failed: %w", err)
	}
	if err := client.Auth(netsmtp.PlainAuth("", c.username, c.password, smtpServer)); err != nil {
		return fmt.Errorf("SMTP authentication failed: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP sender rejected: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("SMTP recipient rejected: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP message submission failed: %w", err)
	}
	if _, err := w.Write(message); err != nil {
		_ = w.Close()
		return fmt.Errorf("SMTP delivery status unknown after message write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP delivery status unknown after message submission: %w", err)
	}
	// DATA succeeded: a QUIT failure must not report a failed send or skip the Sent copy.
	if err := client.Quit(); err != nil {
		slog.WarnContext(ctx, "SMTP accepted message; session shutdown failed", "error", err)
	}
	return nil
}

// ReplyToEmail replies to an existing email
func (c *Client) ReplyToEmail(ctx context.Context, original *imap.Email, body string, replyAll bool, opts SendOptions) error {
	// Build recipient list
	to := []string{original.From}

	var cc []string
	if replyAll {
		// Add all To recipients except ourselves
		for _, addr := range original.To {
			if !isAccountAddress(addr, c.username) {
				cc = append(cc, addr)
			}
		}
		// Add all CC recipients except ourselves
		for _, addr := range original.CC {
			if !isAccountAddress(addr, c.username) {
				cc = append(cc, addr)
			}
		}
	}

	// Merge with provided CC
	if len(opts.CC) > 0 {
		cc = append(cc, opts.CC...)
	}

	// Build subject with Re: prefix
	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Build reply headers
	headers := make(map[string]string)
	if original.MessageID != "" {
		headers["In-Reply-To"] = original.MessageID

		// Build References header
		refs := []string{}
		if len(original.References) > 0 {
			refs = append(refs, original.References...)
		}
		refs = append(refs, original.MessageID)
		headers["References"] = strings.Join(refs, " ")
	}

	// Merge with provided headers
	for key, value := range opts.Headers {
		headers[key] = value
	}

	// Send the reply
	sendOpts := SendOptions{
		CC:          cc,
		BCC:         opts.BCC,
		HTML:        opts.HTML,
		Attachments: opts.Attachments,
		Headers:     headers,
	}

	from := opts.From
	if from == "" {
		from = c.username
	}
	return c.SendEmail(ctx, from, to, subject, body, sendOpts)
}

func envelopeAddresses(addresses []string) ([]string, error) {
	result := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		address, err := netmail.ParseAddress(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, address.Address)
	}
	return result, nil
}

func isAccountAddress(raw, account string) bool {
	address, err := netmail.ParseAddress(raw)
	return err == nil && strings.EqualFold(address.Address, account)
}
