package tools

import (
	"context"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	smtppkg "github.com/olegpustovit/mcp-icloud-email/smtp"
)

// EmailReader defines read-only IMAP operations.
type EmailReader interface {
	ListFolders(ctx context.Context) ([]string, error)
	SearchEmails(ctx context.Context, folder, query string, filters imap.EmailFilters) ([]imap.Email, int, error)
	GetEmail(ctx context.Context, folder, emailID string) (*imap.Email, error)
	CountEmails(ctx context.Context, folder string, filters imap.EmailFilters) (int, error)
	GetAttachment(ctx context.Context, folder, emailID, filename string) (*imap.AttachmentData, error)
}

// EmailWriter defines mutating IMAP operations.
type EmailWriter interface {
	MarkRead(ctx context.Context, folder, emailID string, read bool) error
	MoveEmail(ctx context.Context, fromFolder, toFolder, emailID string) error
	DeleteEmail(ctx context.Context, folder, emailID string, permanent bool) error
	FlagEmail(ctx context.Context, folder, emailID, flagType, color string) error
	SaveDraft(ctx context.Context, from string, to []string, subject, body string, opts imap.DraftOptions) (string, error)
	CreateFolder(ctx context.Context, name, parent string) error
	DeleteFolder(ctx context.Context, name string, force bool) (wasEmpty bool, emailCount int, err error)
}

// EmailService combines all IMAP operations. The concrete *imap.Client satisfies this.
type EmailService interface {
	EmailReader
	EmailWriter
}

// EmailSender defines SMTP operations.
type EmailSender interface {
	SendEmail(ctx context.Context, from string, to []string, subject, body string, opts smtppkg.SendOptions) error
	ReplyToEmail(ctx context.Context, original *imap.Email, body string, replyAll bool, opts smtppkg.SendOptions) error
}
