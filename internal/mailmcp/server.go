package mailmcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/tools"
)

type field struct {
	name, kind, description string
	required                bool
	defaultValue            any
	minimum, maximum        *float64
	minLength               *int
	enum                    []string
}

type Dependencies struct {
	IMAP      tools.EmailService
	SMTP      tools.EmailSender
	FromEmail string
	Close     func()
}

type Resolver func(context.Context) (Dependencies, error)

func str(name, description string, required bool, defaultValue any) field {
	return field{name: name, kind: "string", description: description, required: required, defaultValue: defaultValue}
}

func boolean(name, description string, defaultValue any) field {
	return field{name: name, kind: "boolean", description: description, defaultValue: defaultValue}
}

func number(name, description string, defaultValue any, minimum, maximum *float64) field {
	return field{name: name, kind: "number", description: description, defaultValue: defaultValue, minimum: minimum, maximum: maximum}
}

func positiveNumber(name, description string, defaultValue any) field {
	min := float64(1)
	return number(name, description, defaultValue, &min, nil)
}

func nonnegativeNumber(name, description string, defaultValue any) field {
	min := float64(0)
	return number(name, description, defaultValue, &min, nil)
}

func requiredString(name, description string) field {
	min := 1
	f := str(name, description, true, nil)
	f.minLength = &min
	return f
}

func enumString(name, description string, values ...string) field {
	f := str(name, description, false, nil)
	f.enum = values
	return f
}

func addressList(name, description string, required bool) field {
	return field{name: name, kind: "address-list", description: description, required: required}
}

func schema(fields ...field) *jsonschema.Schema {
	properties := make(map[string]*jsonschema.Schema, len(fields))
	var required []string
	for _, f := range fields {
		p := &jsonschema.Schema{Type: f.kind, Description: f.description, MinLength: f.minLength, Minimum: f.minimum, Maximum: f.maximum}
		if f.kind == "address-list" {
			p.Type = ""
			p.OneOf = []*jsonschema.Schema{
				{Type: "string", MinLength: ptrInt(1)},
				{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			}
		}
		if f.defaultValue != nil {
			p.Default, _ = json.Marshal(f.defaultValue)
		}
		if len(f.enum) > 0 {
			for _, v := range f.enum {
				p.Enum = append(p.Enum, v)
			}
		}
		properties[f.name] = p
		if f.required {
			required = append(required, f.name)
		}
	}
	return &jsonschema.Schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}

func tool(name, description string, readOnly, destructive, idempotent bool, input *jsonschema.Schema) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Description: description, InputSchema: input,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: idempotent},
	}
}

// New builds the reusable MCP server definition used by stdio and authenticated HTTP requests.
func New(resolve Resolver, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "iCloud Email Server", Version: version}, nil)
	add := func(t *mcp.Tool, factory func(Dependencies) mcp.ToolHandler) {
		mcp.AddTool[map[string]any, any](s, t, func(ctx context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			deps, err := resolve(ctx)
			if err != nil {
				return nil, nil, err
			}
			if deps.Close != nil {
				defer deps.Close()
			}
			id := uuid.NewString()
			start := time.Now()
			logger := slog.With("request_id", id, "tool", req.Params.Name)
			logger.Debug("tool call started")
			result, err := invoke(ctx, req, factory(deps))
			switch {
			case err != nil:
				logger.Error("tool call failed", "duration_ms", time.Since(start).Milliseconds(), "error", err)
			case result != nil && result.IsError:
				logger.Warn("tool call returned error", "duration_ms", time.Since(start).Milliseconds())
			default:
				logger.Info("tool call completed", "duration_ms", time.Since(start).Milliseconds())
			}
			return result, nil, err
		})
	}

	add(tool("search_emails", "Search and list emails with optional filters. Use list_folders first to discover valid folder names. Returns each email's id (use with get_email), from, to, subject, date, and unread status.", true, false, true, schema(
		str("query", "Search term to find in subject and body text", false, nil),
		str("folder", "Mailbox folder to search in. Use list_folders to discover valid names.", false, "INBOX"),
		positiveNumber("last_days", "Only return emails from the last N days. Ignored if 'since' is provided.", float64(30)),
		number("limit", "Maximum number of emails to return. Most recent emails are returned first.", float64(50), ptrFloat(1), ptrFloat(200)),
		nonnegativeNumber("offset", "Number of most-recent matching emails to skip (for pagination). Use with limit to page through results.", float64(0)),
		boolean("unread_only", "Only return unread (unseen) emails.", false),
		str("since", "Start date filter in RFC 3339 format. Overrides last_days.", false, nil),
		str("before", "End date filter in RFC 3339 format.", false, nil),
	)), func(d Dependencies) mcp.ToolHandler { return tools.SearchEmailsHandler(d.IMAP) })
	add(tool("get_email", "Fetch full email content by ID. Use search_emails first to find email IDs. Returns full message content and attachment metadata.", true, false, true, schema(
		requiredString("email_id", "Email UID from search_emails results."), str("folder", "Mailbox folder containing the email.", false, "INBOX"),
	)), func(d Dependencies) mcp.ToolHandler { return tools.GetEmailHandler(d.IMAP) })
	add(tool("send_email", "Compose and send a new email via SMTP. Calling twice will send duplicate emails.", false, false, false, schema(
		addressList("to", "Recipient email address (string) or JSON array of addresses.", true), requiredString("subject", "Email subject line."), requiredString("body", "Email body content. Plain text by default; set html=true for HTML."),
		addressList("cc", "CC email address (string) or JSON array of addresses.", false), addressList("bcc", "BCC email address (string) or JSON array of addresses.", false), boolean("html", "Set true if body contains HTML. A plain text version is auto-generated.", false),
	)), func(d Dependencies) mcp.ToolHandler { return tools.SendEmailHandler(d.SMTP, d.FromEmail) })
	add(tool("reply_email", "Reply to an existing email. Automatically sets In-Reply-To/References headers and Re: subject prefix. Calling twice sends duplicate replies.", false, false, false, schema(
		requiredString("email_id", "Email UID of the message being replied to."), requiredString("body", "Reply body content. Plain text by default; set html=true for HTML."), str("folder", "Mailbox folder containing the original email.", false, "INBOX"), boolean("reply_all", "Reply to all original recipients (To + CC) instead of just the sender.", false), boolean("html", "Set true if body contains HTML.", false),
	)), func(d Dependencies) mcp.ToolHandler { return tools.ReplyEmailHandler(d.IMAP, d.SMTP) })
	add(tool("delete_email", "Delete an email. By default moves to trash. Set permanent=true for immediate removal.", false, true, true, schema(
		requiredString("email_id", "Email UID to delete."), str("folder", "Mailbox folder containing the email.", false, "INBOX"), boolean("permanent", "Permanently expunge the email instead of moving to trash. This cannot be undone.", false),
	)), func(d Dependencies) mcp.ToolHandler { return tools.DeleteEmailHandler(d.IMAP) })
	add(tool("move_email", "Move an email from one folder to another.", false, true, true, schema(
		requiredString("email_id", "Email UID to move."), str("from_folder", "Source mailbox folder.", false, "INBOX"), requiredString("to_folder", "Destination mailbox folder."),
	)), func(d Dependencies) mcp.ToolHandler { return tools.MoveEmailHandler(d.IMAP) })
	add(tool("list_folders", "List all available mailbox folders.", true, false, true, schema()), func(d Dependencies) mcp.ToolHandler { return tools.ListFoldersHandler(d.IMAP) })
	add(tool("create_folder", "Create a new mailbox folder. Optionally nest under a parent folder.", false, false, false, schema(
		requiredString("name", "Name of the new folder."), str("parent", "Parent folder path for nesting. Omit for top-level folder.", false, nil),
	)), func(d Dependencies) mcp.ToolHandler { return tools.CreateFolderHandler(d.IMAP) })
	add(tool("delete_folder", "Delete a mailbox folder. Refuses if the folder contains emails unless force=true.", false, true, false, schema(
		requiredString("name", "Folder name to delete."), boolean("force", "Delete even if the folder contains emails. All contained emails will be lost.", false),
	)), func(d Dependencies) mcp.ToolHandler { return tools.DeleteFolderHandler(d.IMAP) })
	add(tool("mark_read", "Mark an email as read (seen) or unread (unseen).", false, false, true, schema(
		requiredString("email_id", "Email UID to mark."), str("folder", "Mailbox folder containing the email.", false, "INBOX"), boolean("read", "true to mark as read, false to mark as unread.", true),
	)), func(d Dependencies) mcp.ToolHandler { return tools.MarkReadHandler(d.IMAP) })
	add(tool("count_emails", "Count emails matching filters without fetching content.", true, false, true, schema(
		str("folder", "Mailbox folder to count in.", false, "INBOX"), positiveNumber("last_days", "Only count emails from the last N days.", nil), boolean("unread_only", "Only count unread (unseen) emails.", false),
	)), func(d Dependencies) mcp.ToolHandler { return tools.CountEmailsHandler(d.IMAP) })
	add(tool("draft_email", "Save an email as a draft in the Drafts folder for later review and sending.", false, false, false, schema(
		addressList("to", "Recipient email address (string) or JSON array of addresses.", true), requiredString("subject", "Email subject line."), requiredString("body", "Email body content."), addressList("cc", "CC email address (string) or JSON array of addresses.", false), addressList("bcc", "BCC email address (string) or JSON array of addresses.", false), boolean("html", "Set true if body contains HTML.", false), str("reply_to_id", "Email UID of the original message if creating a reply draft.", false, nil), str("folder", "Folder containing the original email for reply drafts.", false, "INBOX"),
	)), func(d Dependencies) mcp.ToolHandler { return tools.DraftEmailHandler(d.IMAP, d.FromEmail) })
	add(tool("get_attachment", "Download an email attachment by filename. Returns base64-encoded content. Server filesystem paths are unavailable.", true, false, true, schema(
		requiredString("email_id", "Email UID containing the attachment."), requiredString("filename", "Exact filename of the attachment."), str("folder", "Mailbox folder containing the email.", false, "INBOX"), str("save_path", "Unsupported on this server; omit to receive base64-encoded content.", false, nil),
	)), func(d Dependencies) mcp.ToolHandler { return tools.GetAttachmentHandler(d.IMAP) })
	flag := enumString("flag", "Flag type to set. Use 'none' to remove all flags.", "follow-up", "important", "deadline", "none")
	flag.required = true
	add(tool("flag_email", "Set or remove flags on an email.", false, false, true, schema(
		requiredString("email_id", "Email UID to flag."), flag, str("folder", "Mailbox folder containing the email.", false, "INBOX"), enumString("color", "Optional flag color.", "red", "orange", "yellow", "green", "blue", "purple"),
	)), func(d Dependencies) mcp.ToolHandler { return tools.FlagEmailHandler(d.IMAP) })

	return s
}

func invoke(ctx context.Context, req *mcp.CallToolRequest, handler mcp.ToolHandler) (result *mcp.CallToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("tool handler panic", "tool", req.Params.Name, "panic", recovered)
			result = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "internal tool error"}}}
			err = nil
		}
	}()
	return handler(ctx, req)
}

func ptrFloat(v float64) *float64 { return &v }

func ptrInt(v int) *int { return &v }
