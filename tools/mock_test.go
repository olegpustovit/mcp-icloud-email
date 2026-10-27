package tools

import (
	"context"
	"fmt"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	smtppkg "github.com/olegpustovit/mcp-icloud-email/smtp"
)

// MockEmailService implements EmailService for testing.
type MockEmailService struct {
	// Return values
	Folders    []string
	Emails     []imap.Email
	Email      *imap.Email
	Count      int
	Attachment *imap.AttachmentData
	DraftID    string
	WasEmpty   bool
	EmailCount int

	// Error injection
	Err error

	// Call tracking
	LastMethod     string
	LastFolder     string
	LastEmailID    string
	LastQuery      string
	LastFilters    imap.EmailFilters
	LastRead       bool
	LastFromFolder string
	LastToFolder   string
	LastPermanent  bool
	LastFlagType   string
	LastColor      string
	LastFrom       string
	LastTo         []string
	LastSubject    string
	LastBody       string
	LastDraftOpts  imap.DraftOptions
	LastName       string
	LastParent     string
	LastForce      bool
	LastFilename   string
	CallCount      int
}

func (m *MockEmailService) ListFolders(ctx context.Context) ([]string, error) {
	m.LastMethod = "ListFolders"
	m.CallCount++
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Folders, nil
}

func (m *MockEmailService) SearchEmails(ctx context.Context, folder, query string, filters imap.EmailFilters) ([]imap.Email, int, error) {
	m.LastMethod = "SearchEmails"
	m.LastFolder = folder
	m.LastQuery = query
	m.LastFilters = filters
	m.CallCount++
	if m.Err != nil {
		return nil, 0, m.Err
	}
	return m.Emails, len(m.Emails), nil
}

func (m *MockEmailService) GetEmail(ctx context.Context, folder, emailID string) (*imap.Email, error) {
	m.LastMethod = "GetEmail"
	m.LastFolder = folder
	m.LastEmailID = emailID
	m.CallCount++
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Email, nil
}

func (m *MockEmailService) CountEmails(ctx context.Context, folder string, filters imap.EmailFilters) (int, error) {
	m.LastMethod = "CountEmails"
	m.LastFolder = folder
	m.LastFilters = filters
	m.CallCount++
	if m.Err != nil {
		return 0, m.Err
	}
	return m.Count, nil
}

func (m *MockEmailService) GetAttachment(ctx context.Context, folder, emailID, filename string) (*imap.AttachmentData, error) {
	m.LastMethod = "GetAttachment"
	m.LastFolder = folder
	m.LastEmailID = emailID
	m.LastFilename = filename
	m.CallCount++
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Attachment, nil
}

func (m *MockEmailService) MarkRead(ctx context.Context, folder, emailID string, read bool) error {
	m.LastMethod = "MarkRead"
	m.LastFolder = folder
	m.LastEmailID = emailID
	m.LastRead = read
	m.CallCount++
	return m.Err
}

func (m *MockEmailService) MoveEmail(ctx context.Context, fromFolder, toFolder, emailID string) error {
	m.LastMethod = "MoveEmail"
	m.LastFromFolder = fromFolder
	m.LastToFolder = toFolder
	m.LastEmailID = emailID
	m.CallCount++
	return m.Err
}

func (m *MockEmailService) DeleteEmail(ctx context.Context, folder, emailID string, permanent bool) error {
	m.LastMethod = "DeleteEmail"
	m.LastFolder = folder
	m.LastEmailID = emailID
	m.LastPermanent = permanent
	m.CallCount++
	return m.Err
}

func (m *MockEmailService) FlagEmail(ctx context.Context, folder, emailID, flagType, color string) error {
	m.LastMethod = "FlagEmail"
	m.LastFolder = folder
	m.LastEmailID = emailID
	m.LastFlagType = flagType
	m.LastColor = color
	m.CallCount++
	return m.Err
}

func (m *MockEmailService) SaveDraft(ctx context.Context, from string, to []string, subject, body string, opts imap.DraftOptions) (string, error) {
	m.LastMethod = "SaveDraft"
	m.LastFrom = from
	m.LastTo = to
	m.LastSubject = subject
	m.LastBody = body
	m.LastDraftOpts = opts
	m.CallCount++
	if m.Err != nil {
		return "", m.Err
	}
	return m.DraftID, nil
}

func (m *MockEmailService) CreateFolder(ctx context.Context, name, parent string) error {
	m.LastMethod = "CreateFolder"
	m.LastName = name
	m.LastParent = parent
	m.CallCount++
	return m.Err
}

func (m *MockEmailService) DeleteFolder(ctx context.Context, name string, force bool) (bool, int, error) {
	m.LastMethod = "DeleteFolder"
	m.LastName = name
	m.LastForce = force
	m.CallCount++
	if m.Err != nil {
		return false, m.EmailCount, m.Err
	}
	return m.WasEmpty, m.EmailCount, nil
}

// MockEmailSender implements EmailSender for testing.
type MockEmailSender struct {
	Err          error
	LastMethod   string
	LastFrom     string
	LastTo       []string
	LastSubject  string
	LastBody     string
	LastOpts     smtppkg.SendOptions
	LastOriginal *imap.Email
	LastReplyAll bool
	CallCount    int
}

func (m *MockEmailSender) SendEmail(ctx context.Context, from string, to []string, subject, body string, opts smtppkg.SendOptions) error {
	m.LastMethod = "SendEmail"
	m.LastFrom = from
	m.LastTo = to
	m.LastSubject = subject
	m.LastBody = body
	m.LastOpts = opts
	m.CallCount++
	return m.Err
}

func (m *MockEmailSender) ReplyToEmail(ctx context.Context, original *imap.Email, body string, replyAll bool, opts smtppkg.SendOptions) error {
	m.LastMethod = "ReplyToEmail"
	m.LastOriginal = original
	m.LastBody = body
	m.LastReplyAll = replyAll
	m.LastOpts = opts
	m.CallCount++
	return m.Err
}

// newErrMock returns a mock with an error pre-configured
func newErrMock(msg string) *MockEmailService {
	return &MockEmailService{Err: fmt.Errorf("%s", msg)}
}
