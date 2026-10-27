package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/imap"
)

// CountEmailsHandler creates a handler for counting emails
func CountEmailsHandler(client EmailReader) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get folder (default to INBOX)
		folder, _ := args["folder"].(string)
		if folder == "" {
			folder = "INBOX"
		}

		// Build filters
		filters := imap.EmailFilters{}

		// Parse last_days
		if lastDays, ok := args["last_days"].(float64); ok && lastDays > 0 {
			filters.LastDays = int(lastDays)
		}

		// Parse unread_only
		if unreadOnly, ok := args["unread_only"].(bool); ok {
			filters.UnreadOnly = unreadOnly
		}

		// Count emails
		count, err := client.CountEmails(ctx, folder, filters)
		if err != nil {
			return toolError(fmt.Sprintf("failed to count emails: %v", err)), nil
		}

		// Format response
		response := map[string]interface{}{
			"count":  count,
			"folder": folder,
		}

		if filters.LastDays > 0 {
			response["last_days"] = filters.LastDays
		}
		if filters.UnreadOnly {
			response["unread_only"] = true
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
