package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GetAttachmentHandler creates a handler for downloading email attachments
func GetAttachmentHandler(imapClient EmailReader) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get required email_id
		emailID, ok := args["email_id"].(string)
		if !ok || emailID == "" {
			return toolError("email_id is required"), nil
		}
		if err := validateEmailID(emailID); err != nil {
			return toolError(err.Error()), nil
		}

		// Get required filename
		filename, ok := args["filename"].(string)
		if !ok || filename == "" {
			return toolError("filename is required"), nil
		}
		if err := validateFilename(filename); err != nil {
			return toolError(err.Error()), nil
		}

		// Get folder (default to INBOX)
		folder, _ := args["folder"].(string)
		if folder == "" {
			folder = "INBOX"
		}

		// Get optional save_path and validate against path traversal
		savePath, _ := args["save_path"].(string)
		if savePath != "" {
			return toolError("save_path is unavailable on the server; omit it to receive the attachment data"), nil
		}

		// Get attachment from IMAP
		attachment, err := imapClient.GetAttachment(ctx, folder, emailID, filename)
		if err != nil {
			return toolError(fmt.Sprintf("failed to get attachment: %v", err)), nil
		}

		// Build response
		response := map[string]interface{}{
			"success":   true,
			"filename":  attachment.Filename,
			"size":      attachment.Size,
			"mime_type": attachment.MIMEType,
		}

		response["data"] = base64.StdEncoding.EncodeToString(attachment.Content)
		response["saved"] = false

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
