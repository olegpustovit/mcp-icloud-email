package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MoveEmailHandler creates a handler for moving emails between folders
func MoveEmailHandler(client EmailWriter) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get required parameters
		emailID, ok := args["email_id"].(string)
		if !ok || emailID == "" {
			return toolError("email_id is required"), nil
		}

		toFolder, ok := args["to_folder"].(string)
		if !ok || toFolder == "" {
			return toolError("to_folder is required"), nil
		}

		// Get from_folder (default to INBOX)
		fromFolder, _ := args["from_folder"].(string)
		if fromFolder == "" {
			fromFolder = "INBOX"
		}

		// Move email
		err := client.MoveEmail(ctx, fromFolder, toFolder, emailID)
		if err != nil {
			return toolError(fmt.Sprintf("failed to move email: %v", err)), nil
		}

		// Format response
		response := map[string]interface{}{
			"success":     true,
			"email_id":    emailID,
			"from_folder": fromFolder,
			"to_folder":   toFolder,
			"message":     fmt.Sprintf("Email moved from '%s' to '%s' successfully", fromFolder, toFolder),
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
