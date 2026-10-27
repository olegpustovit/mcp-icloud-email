package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	imappkg "github.com/olegpustovit/mcp-icloud-email/imap"
)

// req builds a mcp.CallToolRequest with the given arguments.
func req(args map[string]interface{}) *mcp.CallToolRequest {
	raw, _ := json.Marshal(args)
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: raw}}
}

// resultJSON unmarshals the text content of a successful result.
func resultJSON(t *testing.T, result *mcp.CallToolResult) map[string]interface{} {
	t.Helper()
	if result.IsError {
		t.Fatalf("expected success but got error: %+v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content but got none")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text.Text), &m); err != nil {
		t.Fatalf("failed to unmarshal result JSON: %v", err)
	}
	return m
}

// resultErrText extracts the error message from an error result.
func resultErrText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected error result but got success: %+v", result.Content)
	}
	if len(result.Content) == 0 {
		return ""
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return text.Text
}

// --- ListFolders ---

func TestListFoldersHandler(t *testing.T) {
	tests := []struct {
		name    string
		mock    *MockEmailService
		wantErr bool
	}{
		{
			name: "happy path",
			mock: &MockEmailService{
				Folders: []string{"INBOX", "Sent", "Drafts", "Trash"},
			},
		},
		{
			name:    "backend error",
			mock:    newErrMock("connection lost"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := ListFoldersHandler(tt.mock)
			result, err := handler(context.Background(), req(nil))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			data := resultJSON(t, result)
			if int(data["count"].(float64)) != len(tt.mock.Folders) {
				t.Errorf("count = %v, want %d", data["count"], len(tt.mock.Folders))
			}
		})
	}
}

func TestGetAttachmentHandlerRejectsServerFilesystemWrite(t *testing.T) {
	handler := GetAttachmentHandler(&MockEmailService{})
	result, err := handler(context.Background(), req(map[string]interface{}{
		"email_id": "1", "filename": "report.pdf", "save_path": "/etc/hosts",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if message := resultErrText(t, result); !strings.Contains(message, "unavailable on the server") {
		t.Fatalf("unexpected error: %s", message)
	}
}

// --- GetEmail ---

func TestGetEmailHandler(t *testing.T) {
	sampleEmail := &imappkg.Email{
		ID:      "123",
		From:    "alice@example.com",
		Subject: "Hello",
	}

	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{"email_id": "123"},
			mock: &MockEmailService{Email: sampleEmail},
		},
		{
			name: "with folder",
			args: map[string]interface{}{"email_id": "123", "folder": "Sent"},
			mock: &MockEmailService{Email: sampleEmail},
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "empty email_id",
			args:    map[string]interface{}{"email_id": ""},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "123"},
			mock:    newErrMock("not found"),
			wantErr: true,
			errMsg:  "failed to get email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := GetEmailHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["id"] != "123" {
				t.Errorf("id = %v, want 123", data["id"])
			}
			if tt.args["folder"] != nil {
				if tt.mock.LastFolder != tt.args["folder"].(string) {
					t.Errorf("folder = %q, want %q", tt.mock.LastFolder, tt.args["folder"])
				}
			} else {
				if tt.mock.LastFolder != "INBOX" {
					t.Errorf("default folder = %q, want INBOX", tt.mock.LastFolder)
				}
			}
		})
	}
}

// --- SearchEmails ---

func TestSearchEmailsHandler(t *testing.T) {
	now := time.Now()
	emails := []imappkg.Email{
		{ID: "1", Subject: "First"},
		{ID: "2", Subject: "Second"},
	}

	tests := []struct {
		name      string
		args      map[string]interface{}
		mock      *MockEmailService
		wantErr   bool
		checkMock func(t *testing.T, m *MockEmailService)
	}{
		{
			name: "defaults",
			args: map[string]interface{}{},
			mock: &MockEmailService{Emails: emails},
			checkMock: func(t *testing.T, m *MockEmailService) {
				if m.LastFolder != "INBOX" {
					t.Errorf("folder = %q, want INBOX", m.LastFolder)
				}
				if m.LastFilters.LastDays != 30 {
					t.Errorf("lastDays = %d, want 30", m.LastFilters.LastDays)
				}
				if m.LastFilters.Limit != 50 {
					t.Errorf("limit = %d, want 50", m.LastFilters.Limit)
				}
			},
		},
		{
			name: "with query and folder",
			args: map[string]interface{}{"query": "invoice", "folder": "Sent", "limit": float64(10)},
			mock: &MockEmailService{Emails: emails},
			checkMock: func(t *testing.T, m *MockEmailService) {
				if m.LastQuery != "invoice" {
					t.Errorf("query = %q, want invoice", m.LastQuery)
				}
				if m.LastFolder != "Sent" {
					t.Errorf("folder = %q, want Sent", m.LastFolder)
				}
				if m.LastFilters.Limit != 10 {
					t.Errorf("limit = %d, want 10", m.LastFilters.Limit)
				}
			},
		},
		{
			name: "limit capped at 200",
			args: map[string]interface{}{"limit": float64(999)},
			mock: &MockEmailService{Emails: emails},
			checkMock: func(t *testing.T, m *MockEmailService) {
				if m.LastFilters.Limit != 200 {
					t.Errorf("limit = %d, want 200 (capped)", m.LastFilters.Limit)
				}
			},
		},
		{
			name: "since overrides last_days",
			args: map[string]interface{}{"since": now.Format(time.RFC3339)},
			mock: &MockEmailService{Emails: emails},
			checkMock: func(t *testing.T, m *MockEmailService) {
				if m.LastFilters.Since == nil {
					t.Fatal("expected Since filter to be set")
				}
				if m.LastFilters.LastDays != 0 {
					t.Errorf("lastDays should be 0 when since is set, got %d", m.LastFilters.LastDays)
				}
			},
		},
		{
			name:    "invalid since format",
			args:    map[string]interface{}{"since": "not-a-date"},
			mock:    &MockEmailService{},
			wantErr: true,
		},
		{
			name:    "invalid before format",
			args:    map[string]interface{}{"before": "not-a-date"},
			mock:    &MockEmailService{},
			wantErr: true,
		},
		{
			name: "offset passed to filters",
			args: map[string]interface{}{"offset": float64(20)},
			mock: &MockEmailService{Emails: emails},
			checkMock: func(t *testing.T, m *MockEmailService) {
				if m.LastFilters.Offset != 20 {
					t.Errorf("offset = %d, want 20", m.LastFilters.Offset)
				}
			},
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{},
			mock:    newErrMock("IMAP error"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := SearchEmailsHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			data := resultJSON(t, result)
			if data["folder"] != tt.mock.LastFolder {
				t.Errorf("response folder = %v, mock folder = %v", data["folder"], tt.mock.LastFolder)
			}
			if total, ok := data["total"].(float64); !ok || total < 0 {
				t.Errorf("expected non-negative total in response, got %v", data["total"])
			}
			if tt.checkMock != nil {
				tt.checkMock(t, tt.mock)
			}
		})
	}
}

// --- CountEmails ---

func TestCountEmailsHandler(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]interface{}
		mock      *MockEmailService
		wantErr   bool
		wantCount int
	}{
		{
			name:      "defaults",
			args:      map[string]interface{}{},
			mock:      &MockEmailService{Count: 42},
			wantCount: 42,
		},
		{
			name:      "with filters",
			args:      map[string]interface{}{"folder": "Sent", "last_days": float64(7), "unread_only": true},
			mock:      &MockEmailService{Count: 5},
			wantCount: 5,
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CountEmailsHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			data := resultJSON(t, result)
			if int(data["count"].(float64)) != tt.wantCount {
				t.Errorf("count = %v, want %d", data["count"], tt.wantCount)
			}
		})
	}
}

// --- MarkRead ---

func TestMarkReadHandler(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		mock     *MockEmailService
		wantErr  bool
		wantRead bool
	}{
		{
			name:     "mark read (default)",
			args:     map[string]interface{}{"email_id": "100"},
			mock:     &MockEmailService{},
			wantRead: true,
		},
		{
			name:     "mark unread",
			args:     map[string]interface{}{"email_id": "100", "read": false},
			mock:     &MockEmailService{},
			wantRead: false,
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{},
			mock:    &MockEmailService{},
			wantErr: true,
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "100"},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := MarkReadHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			resultJSON(t, result)
			if tt.mock.LastRead != tt.wantRead {
				t.Errorf("read = %v, want %v", tt.mock.LastRead, tt.wantRead)
			}
		})
	}
}

// --- MoveEmail ---

func TestMoveEmailHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{"email_id": "100", "to_folder": "Archive"},
			mock: &MockEmailService{},
		},
		{
			name: "with from_folder",
			args: map[string]interface{}{"email_id": "100", "from_folder": "Sent", "to_folder": "Archive"},
			mock: &MockEmailService{},
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{"to_folder": "Archive"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "missing to_folder",
			args:    map[string]interface{}{"email_id": "100"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "to_folder is required",
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "100", "to_folder": "Archive"},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := MoveEmailHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
		})
	}
}

// --- DeleteEmail ---

func TestDeleteEmailHandler(t *testing.T) {
	tests := []struct {
		name          string
		args          map[string]interface{}
		mock          *MockEmailService
		wantErr       bool
		wantPermanent bool
	}{
		{
			name:          "move to trash (default)",
			args:          map[string]interface{}{"email_id": "100"},
			mock:          &MockEmailService{},
			wantPermanent: false,
		},
		{
			name:          "permanent delete",
			args:          map[string]interface{}{"email_id": "100", "permanent": true},
			mock:          &MockEmailService{},
			wantPermanent: true,
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{},
			mock:    &MockEmailService{},
			wantErr: true,
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "100"},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := DeleteEmailHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			resultJSON(t, result)
			if tt.mock.LastPermanent != tt.wantPermanent {
				t.Errorf("permanent = %v, want %v", tt.mock.LastPermanent, tt.wantPermanent)
			}
		})
	}
}

// --- FlagEmail ---

func TestFlagEmailHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "flag follow-up",
			args: map[string]interface{}{"email_id": "100", "flag": "follow-up"},
			mock: &MockEmailService{},
		},
		{
			name: "flag with color",
			args: map[string]interface{}{"email_id": "100", "flag": "important", "color": "red"},
			mock: &MockEmailService{},
		},
		{
			name: "remove flags",
			args: map[string]interface{}{"email_id": "100", "flag": "none"},
			mock: &MockEmailService{},
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{"flag": "important"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "missing flag",
			args:    map[string]interface{}{"email_id": "100"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "flag is required",
		},
		{
			name:    "invalid flag type",
			args:    map[string]interface{}{"email_id": "100", "flag": "bogus"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "flag must be one of",
		},
		{
			name:    "invalid color",
			args:    map[string]interface{}{"email_id": "100", "flag": "important", "color": "magenta"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "color must be one of",
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "100", "flag": "important"},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := FlagEmailHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			resultJSON(t, result)
		})
	}
}

// --- SendEmail ---

func TestSendEmailHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailSender
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{
				"to":      "bob@example.com",
				"subject": "Hi",
				"body":    "Hello Bob",
			},
			mock: &MockEmailSender{},
		},
		{
			name: "with CC and BCC",
			args: map[string]interface{}{
				"to":      "bob@example.com",
				"subject": "Hi",
				"body":    "Hello",
				"cc":      "carol@example.com",
				"bcc":     "dave@example.com",
				"html":    true,
			},
			mock: &MockEmailSender{},
		},
		{
			name: "array of to addresses",
			args: map[string]interface{}{
				"to":      []interface{}{"a@example.com", "b@example.com"},
				"subject": "Hi",
				"body":    "Hello",
			},
			mock: &MockEmailSender{},
		},
		{
			name:    "missing to",
			args:    map[string]interface{}{"subject": "Hi", "body": "Hello"},
			mock:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "to is required",
		},
		{
			name:    "missing subject",
			args:    map[string]interface{}{"to": "bob@example.com", "body": "Hello"},
			mock:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "subject is required",
		},
		{
			name:    "missing body",
			args:    map[string]interface{}{"to": "bob@example.com", "subject": "Hi"},
			mock:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "body is required",
		},
		{
			name: "invalid to address",
			args: map[string]interface{}{
				"to":      "not-an-email",
				"subject": "Hi",
				"body":    "Hello",
			},
			mock:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name: "backend error",
			args: map[string]interface{}{
				"to":      "bob@example.com",
				"subject": "Hi",
				"body":    "Hello",
			},
			mock:    &MockEmailSender{Err: fmt.Errorf("SMTP fail")},
			wantErr: true,
			errMsg:  "failed to send email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := SendEmailHandler(tt.mock, "me@icloud.com")
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
		})
	}
}

// --- ReplyEmail ---

func TestReplyEmailHandler(t *testing.T) {
	original := &imappkg.Email{
		ID:        "100",
		From:      "alice@example.com",
		Subject:   "Original",
		MessageID: "<msg@example.com>",
	}

	tests := []struct {
		name    string
		args    map[string]interface{}
		imap    *MockEmailService
		smtp    *MockEmailSender
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{"email_id": "100", "body": "Thanks!"},
			imap: &MockEmailService{Email: original},
			smtp: &MockEmailSender{},
		},
		{
			name: "reply all with HTML",
			args: map[string]interface{}{"email_id": "100", "body": "<p>Thanks!</p>", "reply_all": true, "html": true},
			imap: &MockEmailService{Email: original},
			smtp: &MockEmailSender{},
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{"body": "reply"},
			imap:    &MockEmailService{},
			smtp:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "missing body",
			args:    map[string]interface{}{"email_id": "100"},
			imap:    &MockEmailService{},
			smtp:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "body is required",
		},
		{
			name:    "IMAP error fetching original",
			args:    map[string]interface{}{"email_id": "100", "body": "reply"},
			imap:    newErrMock("not found"),
			smtp:    &MockEmailSender{},
			wantErr: true,
			errMsg:  "failed to get original email",
		},
		{
			name:    "SMTP error sending reply",
			args:    map[string]interface{}{"email_id": "100", "body": "reply"},
			imap:    &MockEmailService{Email: original},
			smtp:    &MockEmailSender{Err: fmt.Errorf("SMTP fail")},
			wantErr: true,
			errMsg:  "failed to send reply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := ReplyEmailHandler(tt.imap, tt.smtp)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
		})
	}
}

// --- DraftEmail ---

func TestDraftEmailHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{
				"to":      "bob@example.com",
				"subject": "Draft subject",
				"body":    "Draft body",
			},
			mock: &MockEmailService{DraftID: "999"},
		},
		{
			name: "with reply_to_id",
			args: map[string]interface{}{
				"to":          "bob@example.com",
				"subject":     "Re: Something",
				"body":        "Reply draft",
				"reply_to_id": "123",
				"folder":      "Sent",
			},
			mock: &MockEmailService{DraftID: "1000"},
		},
		{
			name:    "missing to",
			args:    map[string]interface{}{"subject": "Hi", "body": "Hello"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "to is required",
		},
		{
			name:    "missing subject",
			args:    map[string]interface{}{"to": "bob@example.com", "body": "Hello"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "subject is required",
		},
		{
			name:    "missing body",
			args:    map[string]interface{}{"to": "bob@example.com", "subject": "Hi"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "body is required",
		},
		{
			name: "backend error",
			args: map[string]interface{}{
				"to":      "bob@example.com",
				"subject": "Hi",
				"body":    "Hello",
			},
			mock:    newErrMock("IMAP error"),
			wantErr: true,
			errMsg:  "failed to save draft",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := DraftEmailHandler(tt.mock, "me@icloud.com")
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
			if data["draft_id"] == nil || data["draft_id"] == "" {
				t.Error("expected draft_id in response")
			}
		})
	}
}

// --- GetAttachment ---

func TestGetAttachmentHandler(t *testing.T) {
	attachment := &imappkg.AttachmentData{
		Filename: "doc.pdf",
		Content:  []byte("fake-pdf-content"),
		MIMEType: "application/pdf",
		Size:     16,
	}

	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "return base64",
			args: map[string]interface{}{"email_id": "100", "filename": "doc.pdf"},
			mock: &MockEmailService{Attachment: attachment},
		},
		{
			name:    "missing email_id",
			args:    map[string]interface{}{"filename": "doc.pdf"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "email_id is required",
		},
		{
			name:    "missing filename",
			args:    map[string]interface{}{"email_id": "100"},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "filename is required",
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"email_id": "100", "filename": "doc.pdf"},
			mock:    newErrMock("not found"),
			wantErr: true,
			errMsg:  "failed to get attachment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := GetAttachmentHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
			if data["data"] == nil {
				t.Error("expected base64 data in response")
			}
		})
	}
}

// --- CreateFolder ---

func TestCreateFolderHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
		errMsg  string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{"name": "Projects"},
			mock: &MockEmailService{},
		},
		{
			name: "with parent",
			args: map[string]interface{}{"name": "Work", "parent": "Projects"},
			mock: &MockEmailService{},
		},
		{
			name:    "missing name",
			args:    map[string]interface{}{},
			mock:    &MockEmailService{},
			wantErr: true,
			errMsg:  "name parameter is required",
		},
		{
			name:    "backend error",
			args:    map[string]interface{}{"name": "Test"},
			mock:    newErrMock("fail"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CreateFolderHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				msg := resultErrText(t, result)
				if tt.errMsg != "" && !strings.Contains(msg, tt.errMsg) {
					t.Errorf("error = %q, want containing %q", msg, tt.errMsg)
				}
				return
			}
			data := resultJSON(t, result)
			if data["success"] != true {
				t.Error("expected success=true")
			}
		})
	}
}

// --- DeleteFolder ---

func TestDeleteFolderHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		mock    *MockEmailService
		wantErr bool
	}{
		{
			name: "empty folder",
			args: map[string]interface{}{"name": "OldFolder"},
			mock: &MockEmailService{WasEmpty: true, EmailCount: 0},
		},
		{
			name: "non-empty with force",
			args: map[string]interface{}{"name": "OldFolder", "force": true},
			mock: &MockEmailService{WasEmpty: false, EmailCount: 5},
		},
		{
			name: "non-empty without force returns structured error",
			args: map[string]interface{}{"name": "OldFolder"},
			mock: &MockEmailService{
				EmailCount: 3,
				Err:        fmt.Errorf("folder OldFolder is not empty (contains 3 emails)"),
			},
		},
		{
			name:    "missing name",
			args:    map[string]interface{}{},
			mock:    &MockEmailService{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := DeleteFolderHandler(tt.mock)
			result, err := handler(context.Background(), req(tt.args))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr {
				if !result.IsError {
					t.Fatal("expected error result")
				}
				return
			}
			// Either success or structured "not empty" response - both are valid non-error results
			if !result.IsError {
				resultJSON(t, result)
			}
		})
	}
}

// --- Helpers ---

func TestParseAddressList(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		key     string
		want    int
		wantErr bool
	}{
		{
			name: "string address",
			args: map[string]interface{}{"to": "alice@example.com"},
			key:  "to",
			want: 1,
		},
		{
			name: "array of addresses",
			args: map[string]interface{}{"to": []interface{}{"alice@example.com", "bob@example.com"}},
			key:  "to",
			want: 2,
		},
		{
			name: "missing key returns nil",
			args: map[string]interface{}{},
			key:  "to",
			want: 0,
		},
		{
			name: "nil value returns nil",
			args: map[string]interface{}{"to": nil},
			key:  "to",
			want: 0,
		},
		{
			name:    "invalid email",
			args:    map[string]interface{}{"to": "not-an-email"},
			key:     "to",
			wantErr: true,
		},
		{
			name:    "wrong type",
			args:    map[string]interface{}{"to": 42},
			key:     "to",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseAddressList(tt.args, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tt.want {
				t.Errorf("got %d addresses, want %d", len(result), tt.want)
			}
		})
	}
}

func TestRequireAddressList(t *testing.T) {
	// Empty list should error
	_, err := requireAddressList(map[string]interface{}{}, "to")
	if err == nil {
		t.Error("expected error for missing required field")
	}

	// Non-empty should succeed
	addrs, err := requireAddressList(map[string]interface{}{"to": "a@b.com"}, "to")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Errorf("got %d addresses, want 1", len(addrs))
	}
}
