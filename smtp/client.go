package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	netmail "net/mail"
	netsmtp "net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/google/uuid"
	"github.com/olegpustovit/mcp-icloud-email/imap"
)

const (
	smtpServer = "smtp.mail.me.com"
	smtpPort   = 587
)

// Client handles SMTP operations for sending emails
type Client struct {
	username string
	password string
}

// SendOptions contains optional parameters for sending emails
type SendOptions struct {
	CC      []string
	BCC     []string
	HTML    bool
	Headers map[string]string
}

// NewClient creates a new SMTP client
func NewClient(username, password string) *Client {
	return &Client{
		username: username,
		password: password,
	}
}

// SendEmail sends an email via SMTP
func (c *Client) SendEmail(ctx context.Context, from string, to []string, subject, body string, opts SendOptions) error {
	fromAddress, err := netmail.ParseAddress(from)
	if err != nil || !strings.EqualFold(fromAddress.Address, c.username) {
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
		if !strings.EqualFold(key, "In-Reply-To") && !strings.EqualFold(key, "References") {
			return fmt.Errorf("custom header %q is not allowed", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a line break", key)
		}
	}
	from = fromAddress.Address

	// Create message buffer
	var buf bytes.Buffer

	// Create message header
	var h mail.Header
	h.SetDate(time.Now())
	h.SetAddressList("From", []*mail.Address{{Address: from}})

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
	messageID := fmt.Sprintf("<%s.%s@%s>", uuid.New().String(), c.username, smtpServer)
	h.Set("Message-ID", messageID)

	// Set custom headers
	for key, value := range opts.Headers {
		h.Set(key, value)
	}

	// Create message writer
	var mw *mail.Writer

	if opts.HTML {
		// Multipart alternative for HTML and plain text
		h.SetContentType("multipart/alternative", nil)
		mw, err = mail.CreateWriter(&buf, h)
		if err != nil {
			return fmt.Errorf("failed to create message writer: %w", err)
		}

		// Plain text part
		var textHeader mail.InlineHeader
		textHeader.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		textPart, err := mw.CreateSingleInline(textHeader)
		if err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to create text part: %w", err)
		}
		plainBody := stripHTML(body)
		if _, err := textPart.Write([]byte(plainBody)); err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to write text part: %w", err)
		}
		_ = textPart.Close()

		// HTML part
		var htmlHeader mail.InlineHeader
		htmlHeader.SetContentType("text/html", map[string]string{"charset": "utf-8"})
		htmlPart, err := mw.CreateSingleInline(htmlHeader)
		if err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to create HTML part: %w", err)
		}
		if _, err := htmlPart.Write([]byte(body)); err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to write HTML part: %w", err)
		}
		_ = htmlPart.Close()

		_ = mw.Close()
	} else {
		// Plain text only
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		mw, err = mail.CreateWriter(&buf, h)
		if err != nil {
			return fmt.Errorf("failed to create message writer: %w", err)
		}

		// Create inline part for plain text
		var textHeader mail.InlineHeader
		textHeader.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		textPart, err := mw.CreateSingleInline(textHeader)
		if err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to create text part: %w", err)
		}
		if _, err := textPart.Write([]byte(body)); err != nil {
			_ = mw.Close()
			return fmt.Errorf("failed to write body: %w", err)
		}
		_ = textPart.Close()
		_ = mw.Close()
	}

	// Build recipient list (To + CC + BCC)
	recipients := make([]string, 0, len(to)+len(opts.CC)+len(opts.BCC))
	recipients = append(recipients, to...)
	recipients = append(recipients, opts.CC...)
	recipients = append(recipients, opts.BCC...)

	// Send via SMTP
	return c.send(ctx, from, recipients, buf.Bytes())
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
	if err := client.Quit(); err != nil && err != io.EOF {
		return fmt.Errorf("SMTP accepted message but session shutdown failed: %w", err)
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
		CC:      cc,
		BCC:     opts.BCC,
		HTML:    opts.HTML,
		Headers: headers,
	}

	return c.SendEmail(ctx, c.username, to, subject, body, sendOpts)
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

// stripHTML removes HTML tags for plain text version (basic implementation)
func stripHTML(html string) string {
	// Simple HTML stripping - replace common tags with newlines
	text := strings.ReplaceAll(html, "<br>", "\n")
	text = strings.ReplaceAll(text, "<br/>", "\n")
	text = strings.ReplaceAll(text, "<br />", "\n")
	text = strings.ReplaceAll(text, "</p>", "\n\n")
	text = strings.ReplaceAll(text, "</div>", "\n")

	// Remove remaining tags
	inTag := false
	var result strings.Builder
	for _, char := range text {
		if char == '<' {
			inTag = true
			continue
		}
		if char == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(char)
		}
	}

	return strings.TrimSpace(result.String())
}
