package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mutationError(ctx context.Context, action string, err error) *mcp.CallToolResult {
	if ctx.Err() != nil {
		return toolError(fmt.Sprintf("%s outcome is unknown because the operation timed out or was canceled; check the mailbox before retrying", action))
	}
	return toolError(fmt.Sprintf("failed to %s: %v", action, err))
}

func arguments(req *mcp.CallToolRequest) map[string]interface{} {
	args := map[string]interface{}{}
	if req != nil && req.Params != nil {
		_ = json.Unmarshal(req.Params.Arguments, &args)
	}
	return args
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func toolText(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}
