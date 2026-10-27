package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/imap"
)

// SearchEmailsHandler creates a handler for searching emails
func SearchEmailsHandler(client EmailReader) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get folder (default to INBOX)
		folder, _ := args["folder"].(string)
		if folder == "" {
			folder = "INBOX"
		}

		// Get search query (optional)
		query, _ := args["query"].(string)

		// Build filters
		filters := imap.EmailFilters{
			LastDays: 30, // Default to 30 days
			Limit:    50, // Default limit
		}

		// Parse last_days
		if lastDays, ok := args["last_days"].(float64); ok && lastDays > 0 {
			filters.LastDays = int(lastDays)
		}

		// Parse limit
		if limit, ok := args["limit"].(float64); ok && limit > 0 {
			filters.Limit = int(limit)
			if filters.Limit > 200 {
				filters.Limit = 200 // Max limit
			}
		}

		// Parse offset
		if offset, ok := args["offset"].(float64); ok && offset > 0 {
			filters.Offset = int(offset)
		}

		// Parse unread_only
		if unreadOnly, ok := args["unread_only"].(bool); ok {
			filters.UnreadOnly = unreadOnly
		}

		// Parse since (overrides last_days if provided)
		if sinceStr, ok := args["since"].(string); ok && sinceStr != "" {
			t, err := time.Parse(time.RFC3339, sinceStr)
			if err != nil {
				return toolError(fmt.Sprintf("invalid since format: %v (use ISO 8601 format like '2024-01-15T14:30:00Z')", err)), nil
			}
			filters.Since = &t
			filters.LastDays = 0 // Clear last_days when since is provided
		}

		// Parse before
		if beforeStr, ok := args["before"].(string); ok && beforeStr != "" {
			t, err := time.Parse(time.RFC3339, beforeStr)
			if err != nil {
				return toolError(fmt.Sprintf("invalid before format: %v (use ISO 8601 format like '2024-01-15T14:30:00Z')", err)), nil
			}
			filters.Before = &t
		}

		// Search emails
		emails, total, err := client.SearchEmails(ctx, folder, query, filters)
		if err != nil {
			return toolError(fmt.Sprintf("failed to search emails: %v", err)), nil
		}

		// Format response
		response := map[string]interface{}{
			"count":  len(emails),
			"total":  total,
			"emails": emails,
			"folder": folder,
		}

		if query != "" {
			response["query"] = query
		}

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
