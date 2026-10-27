package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
)

// SendEmailHandler creates a handler for sending emails
func SendEmailHandler(smtpClient EmailSender, fromEmail string) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get required parameters
		subject, ok := args["subject"].(string)
		if !ok || subject == "" {
			return toolError("subject is required"), nil
		}
		if err := validateSubjectSize(subject); err != nil {
			return toolError(err.Error()), nil
		}

		body, ok := args["body"].(string)
		if !ok || body == "" {
			return toolError("body is required"), nil
		}
		if err := validateBodySize(body); err != nil {
			return toolError(err.Error()), nil
		}

		// Parse and validate To addresses
		to, err := requireAddressList(args, "to")
		if err != nil {
			return toolError(err.Error()), nil
		}

		// Build send options
		opts := smtp.SendOptions{}

		// Parse CC addresses
		opts.CC, err = parseAddressList(args, "cc")
		if err != nil {
			return toolError(err.Error()), nil
		}

		// Parse BCC addresses
		opts.BCC, err = parseAddressList(args, "bcc")
		if err != nil {
			return toolError(err.Error()), nil
		}

		// Parse HTML flag
		if html, ok := args["html"].(bool); ok {
			opts.HTML = html
		}

		// Send email
		if err := smtpClient.SendEmail(ctx, fromEmail, to, subject, body, opts); err != nil {
			return mutationError(ctx, "send email", err), nil
		}

		// Format response
		response := map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Email sent successfully to %v", to),
			"subject": subject,
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
