package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DeleteEmailHandler creates a handler for deleting emails
func DeleteEmailHandler(client EmailWriter) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get required email_id
		emailID, ok := args["email_id"].(string)
		if !ok || emailID == "" {
			return toolError("email_id is required"), nil
		}

		// Get folder (default to INBOX)
		folder, _ := args["folder"].(string)
		if folder == "" {
			folder = "INBOX"
		}

		// Get permanent flag (default to false)
		permanent := false
		if perm, ok := args["permanent"].(bool); ok {
			permanent = perm
		}

		// Delete email
		err := client.DeleteEmail(ctx, folder, emailID, permanent)
		if err != nil {
			return toolError(fmt.Sprintf("failed to delete email: %v", err)), nil
		}

		// Format response
		deleteType := "moved to trash"
		if permanent {
			deleteType = "permanently deleted"
		}

		response := map[string]interface{}{
			"success":  true,
			"email_id": emailID,
			"message":  fmt.Sprintf("Email %s successfully", deleteType),
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
