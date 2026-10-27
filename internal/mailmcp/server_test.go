package mailmcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"github.com/olegpustovit/mcp-icloud-email/tools"
)

type imapStub struct{ tools.EmailService }

func (imapStub) ListFolders(context.Context) ([]string, error) { return []string{"INBOX"}, nil }

type smtpStub struct {
	tools.EmailSender
	sends int
}

func (s *smtpStub) SendEmail(context.Context, string, []string, string, string, smtp.SendOptions) error {
	s.sends++
	return nil
}

func TestNewRegistersToolsAndResolvesPerCall(t *testing.T) {
	ctx := context.Background()
	resolved, closed := 0, 0
	sender := &smtpStub{}
	s := New(func(context.Context) (Dependencies, error) {
		resolved++
		return Dependencies{IMAP: imapStub{}, SMTP: sender, FromEmail: "owner@icloud.com", Close: func() { closed++ }}, nil
	}, "test")
	if resolved != 0 {
		t.Fatal("server construction must not resolve request credentials")
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 14 {
		t.Fatalf("listed %d tools, want 14", len(listed.Tools))
	}
	if resolved != 0 {
		t.Fatal("tool discovery must not resolve request credentials")
	}
	invalid, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_email", Arguments: map[string]any{
		"to": "person@example.com", "subject": "hello", "body": "body", "unexpected": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !invalid.IsError || resolved != 0 {
		t.Fatal("invalid tool input must be rejected before resolving account credentials")
	}
	malformed, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_email", Arguments: map[string]any{
		"to": []any{12}, "subject": "hello", "body": "body",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !malformed.IsError || resolved != 0 {
		t.Fatal("malformed recipient array must be rejected before resolving credentials")
	}
	arraySend, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_email", Arguments: map[string]any{
		"to": []string{"person@example.com"}, "cc": []string{"copy@example.com"}, "bcc": []string{"blind@example.com"}, "subject": "hello", "body": "body",
	}})
	if err != nil || arraySend.IsError || sender.sends != 1 {
		t.Fatalf("address array send result=%+v err=%v sends=%d", arraySend, err, sender.sends)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_folders"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("list_folders returned tool error: %+v", result.Content)
	}
	if resolved != 2 || closed != 2 {
		t.Fatalf("resolve/close counts = %d/%d, want 2/2", resolved, closed)
	}
}
