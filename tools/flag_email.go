package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FlagEmailHandler creates a handler for flagging emails
func FlagEmailHandler(imapClient EmailWriter) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := arguments(req)

		// Get required email_id
		emailID, ok := args["email_id"].(string)
		if !ok || emailID == "" {
			return toolError("email_id is required"), nil
		}

		// Get required flag type
		flagType, ok := args["flag"].(string)
		if !ok || flagType == "" {
			return toolError("flag is required"), nil
		}

		// Validate flag type
		validFlags := map[string]bool{
			"follow-up": true,
			"important": true,
			"deadline":  true,
			"none":      true,
		}
		if !validFlags[flagType] {
			return toolError("flag must be one of: follow-up, important, deadline, none"), nil
		}

		// Get folder (default to INBOX)
		folder, _ := args["folder"].(string)
		if folder == "" {
			folder = "INBOX"
		}

		// Get optional color
		color, _ := args["color"].(string)
		if color != "" {
			// Validate color
			validColors := map[string]bool{
				"red":    true,
				"orange": true,
				"yellow": true,
				"green":  true,
				"blue":   true,
				"purple": true,
			}
			if !validColors[color] {
				return toolError("color must be one of: red, orange, yellow, green, blue, purple"), nil
			}
		}

		// Flag the email
		err := imapClient.FlagEmail(ctx, folder, emailID, flagType, color)
		if err != nil {
			return toolError(fmt.Sprintf("failed to flag email: %v", err)), nil
		}

		// Format response
		response := map[string]interface{}{
			"success":  true,
			"email_id": emailID,
			"flag":     flagType,
		}

		if color != "" {
			response["color"] = color
		}

		var message string
		if flagType == "none" {
			message = "Email flags removed successfully"
		} else {
			if color != "" {
				message = fmt.Sprintf("Email flagged as %s (%s) successfully", flagType, color)
			} else {
				message = fmt.Sprintf("Email flagged as %s successfully", flagType)
			}
		}
		response["message"] = message

		jsonData, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to format response: %v", err)), nil
		}

		return toolText(string(jsonData)), nil
	}
}
