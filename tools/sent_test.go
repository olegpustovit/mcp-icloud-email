package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
)

func TestSentCopyFailureRemainsSuccessfulForBothTools(t *testing.T) {
	for _, reply := range []bool{false, true} {
		sender := &MockEmailSender{Err: &smtp.SentCopyError{Err: errors.New("IMAP failed")}}
		handler := SendEmailHandler(sender, "owner@icloud.com")
		args := map[string]interface{}{"to": "to@example.com", "subject": "test", "body": "test"}
		if reply {
			handler = ReplyEmailHandler(&MockEmailService{Email: &imap.Email{ID: "1", From: "to@example.com", Subject: "test"}}, sender)
			args = map[string]interface{}{"email_id": "1", "body": "test"}
		}
		result, err := handler(context.Background(), req(args))
		if err != nil || result.IsError {
			t.Fatalf("reply=%t must report accepted send: %v %+v", reply, err, result)
		}
		data := resultJSON(t, result)
		if data["success"] != true || data["saved_to_sent"] != false || !strings.Contains(data["warning"].(string), "Do not resend") {
			t.Fatalf("wrong result: %v", data)
		}
	}
}
