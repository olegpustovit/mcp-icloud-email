package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/imap"
)

// DraftEmailHandler creates a handler for saving email drafts
func DraftEmailHandler(imapClient EmailWriter, fromEmail string) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

		// Build draft options
		opts := imap.DraftOptions{}

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

		// Parse reply_to_id
		if replyToID, ok := args["reply_to_id"].(string); ok && replyToID != "" {
			opts.ReplyToID = replyToID

			// Parse folder for reply source
			if folder, ok := args["folder"].(string); ok && folder != "" {
				opts.Folder = folder
			}
		}

		// Save draft
		draftID, err := imapClient.SaveDraft(ctx, fromEmail, to, subject, body, opts)
		if err != nil {
			return mutationError(ctx, "save draft", err), nil
		}

		// Build preview string
		var preview strings.Builder
		preview.WriteString("To: " + strings.Join(to, ", ") + "\n")
		if len(opts.CC) > 0 {
			preview.WriteString("CC: " + strings.Join(opts.CC, ", ") + "\n")
		}
		preview.WriteString("Subject: " + subject + "\n")
		preview.WriteString("Body: " + body)

		previewStr := preview.String()
		if len(previewStr) > 200 {
			previewStr = previewStr[:197] + "..."
		}

		// Format response
		response := map[string]interface{}{
			"success":  true,
			"draft_id": draftID,
			"message":  "Draft saved successfully",
			"preview":  previewStr,
		}

		if opts.ReplyToID != "" {
			response["reply_to"] = opts.ReplyToID
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
