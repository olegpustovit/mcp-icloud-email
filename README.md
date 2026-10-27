# iCloud Mail MCP

Multiuser iCloud Mail for MCP clients, with a React settings dashboard, email/password login, and MongoDB persistence. Fork of [roygabriel/mcp-icloud-email](https://github.com/roygabriel/mcp-icloud-email).

## Hosted use

Open https://mail.microapps.space, register, and verify your email. Registration is restricted to `*@olegpustovit.com` and `ursus95@gmail.com`. In settings, connect your iCloud address and an Apple **app-specific password** from https://account.apple.com. The server verifies the iCloud connection before saving it.

Add `https://mail.microapps.space/mcp` as a remote MCP server in your client and select OAuth authentication. Approve the client access request after signing in. Discovery, dynamic client registration, authorization code with PKCE, refresh tokens, and revocation are available. Client support depends on the client's remote MCP and OAuth capabilities; no iCloud password goes into client configuration.

Each client grant permits reading, sending, and organizing your connected mailbox, including destructive operations. Only approve clients you trust. Disconnecting iCloud removes stored mailbox credentials; signing out ends the browser session. Use password reset to revoke all existing browser and client access tokens.

## Run the hosted server

Requires Go 1.26.10+, Node 22+, MongoDB, and a Resend sending key with a verified sender domain.

```sh
cp .env.example .env
# Set MAIL_* values; export them into the process environment.
make server
./mail-server
```

`cmd/mail` reads environment variables directly. The root stdio command also supports `.env` through its existing configuration loader.

| Variable | Purpose |
| --- | --- |
| `MAIL_BASE_URL` | Public HTTPS origin, without trailing slash |
| `MAIL_ADDR` | Listen address; defaults to `127.0.0.1:4290` |
| `MAIL_MONGODB_URI` | MongoDB URI; application database is `mail` |
| `MAIL_ENCRYPTION_KEY` | Base64 encoding of 32 random bytes; generate once with `openssl rand -base64 32` |
| `MAIL_RESEND_API_KEY` | Resend key for email verification and password reset |
| `MAIL_EMAIL_FROM` | Verified sending address, optionally with a display name |

Keep the encryption key backed up separately with your database backup. Replacing or losing it makes stored iCloud credentials unreadable. Secrets belong in private service environment files, never in Git.

The production service runs on `oracle-vm` using [smdctl.yml](smdctl.yml), behind the existing HTTPS reverse proxy. Its private environment file is `~/.config/smdctl/env/mail.env` and its binary is `/home/snowbear/ci/mail/mail-server`.

```sh
smdctl run -f smdctl.yml
smdctl status mail
curl https://mail.microapps.space/healthz
```

Docker builds the React assets and hosted Go binary. Publish port 4290 and supply the same `MAIL_*` variables. Health is available at `/healthz`; MongoDB is checked at startup.

## Email cache

Fetched full messages are cached in MongoDB for one minute, scoped by user, iCloud account, folder, and message UID. Mailbox mutations invalidate that user/account cache; replacing or disconnecting an account clears the user cache. Cached bodies are stored in MongoDB, so database access also grants access to cached message content. The TTL index cleans expired documents asynchronously; reads enforce expiration immediately. Messages over 8 MiB are served without caching; email and attachment reads reject MIME messages over 20 MiB. External mailbox changes can remain stale for up to one minute.

## Local stdio use

The original single-user stdio interface remains available:

```sh
export ICLOUD_EMAIL=you@icloud.com
export ICLOUD_PASSWORD=xxxx-xxxx-xxxx-xxxx
go run .
```

Use an app-specific password, never your main Apple account password. IMAP uses TLS on port 993; SMTP uses STARTTLS on port 587.

Sent emails and replies are saved to the account’s Sent mailbox after SMTP accepts them, preserving the message and threading headers. Tool results include `saved_to_sent`. If filing the copy fails, the result still reports a successful send with a warning; do not resend the email. Sent copies confirm SMTP acceptance, not recipient delivery.

## Development and checks

```sh
cd web && npm ci && npm test && npm run build
# From the repository root:
go test ./...
go vet ./...
# Include Mongo integration tests against a disposable test server:
MAIL_TEST_MONGODB_URI=mongodb://127.0.0.1:27018 go test ./...
```

The official MCP Go SDK owns protocol negotiation, Streamable HTTP and stdio framing, schema validation, and tool dispatch. Tool discovery does not connect to iCloud. Authenticated tool calls resolve the requesting user's credentials and close their connection afterward.

Passwords use salted scrypt hashes. iCloud credentials use AES-256-GCM authenticated encryption bound to the user. Browser sessions and MCP tokens are opaque, stored hashed, checked for expiration, and scoped to the requesting user. Browser writes require a matching Origin. MCP requests require bearer authentication and reject foreign origins. Email links are single-use and expire. Public authentication routes have bounded rate limits.

## Available Tools

The server exposes 14 MCP tools. Each tool includes schema constraints and annotations indicating whether it is read-only, destructive, or idempotent.

### search_emails

Search and list emails with optional filters. Returns email headers (not full bodies) for efficiency.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `query` | string | | Search term for subject/body |
| `folder` | string | `INBOX` | Mailbox folder to search |
| `last_days` | integer | `30` | Only show emails from last N days |
| `limit` | integer | `50` | Max emails to return (max 200) |
| `offset` | integer | `0` | Skip first N results (for pagination) |
| `unread_only` | boolean | `false` | Only return unread emails |
| `since` | string | | Start date (ISO 8601) |
| `before` | string | | End date (ISO 8601) |

Response includes `count` (returned), `total` (matching before offset/limit), and an array of email summaries.

### get_email

Retrieve full email content including body text, HTML, headers, and attachment list.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `folder` | string | `INBOX` | Mailbox folder |

### send_email

Compose and send a new email.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `to` | string/array | *(required)* | Recipient address(es) |
| `subject` | string | *(required)* | Subject line |
| `body` | string | *(required)* | Email body |
| `cc` | string/array | | CC address(es) |
| `bcc` | string/array | | BCC address(es) |
| `html` | boolean | `false` | Whether body is HTML |

### reply_email

Reply to an existing email. Automatically sets In-Reply-To and References headers.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID to reply to |
| `body` | string | *(required)* | Reply body |
| `folder` | string | `INBOX` | Folder containing original email |
| `reply_all` | boolean | `false` | Reply to all recipients |
| `html` | boolean | `false` | Whether body is HTML |

### draft_email

Save an email as a draft. Supports reply drafts with automatic header threading.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `to` | string/array | *(required)* | Recipient address(es) |
| `subject` | string | *(required)* | Subject line |
| `body` | string | *(required)* | Email body |
| `cc` | string/array | | CC address(es) |
| `bcc` | string/array | | BCC address(es) |
| `html` | boolean | `false` | Whether body is HTML |
| `reply_to_id` | string | | Original email ID for reply drafts |
| `folder` | string | `INBOX` | Folder of original email (for replies) |

### delete_email

Delete an email by moving it to trash, or permanently delete it.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `folder` | string | `INBOX` | Mailbox folder |
| `permanent` | boolean | `false` | Permanently delete instead of trashing |

### move_email

Move an email from one folder to another.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `from_folder` | string | `INBOX` | Source folder |
| `to_folder` | string | *(required)* | Destination folder |

### mark_read

Change the read/unread status of an email.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `folder` | string | `INBOX` | Mailbox folder |
| `read` | boolean | `true` | `true` to mark read, `false` for unread |

### flag_email

Flag an email for follow-up with optional color.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `flag` | string | *(required)* | `follow-up`, `important`, `deadline`, or `none` |
| `folder` | string | `INBOX` | Mailbox folder |
| `color` | string | | `red`, `orange`, `yellow`, `green`, `blue`, `purple` |

Set `flag` to `none` to remove all flags.

### count_emails

Count emails matching filters without downloading message content.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `folder` | string | `INBOX` | Mailbox folder |
| `last_days` | integer | | Only count from last N days |
| `unread_only` | boolean | `false` | Only count unread |

### list_folders

List all available mailbox folders. Takes no parameters.

### create_folder

Create a new mailbox folder.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | string | *(required)* | Folder name |
| `parent` | string | | Parent folder for nesting (e.g. `Work/Projects`) |

### delete_folder

Delete a mailbox folder. Non-empty folders require explicit confirmation.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | string | *(required)* | Folder name |
| `force` | boolean | `false` | Delete even if folder contains emails |

System folders (INBOX, Sent, Trash) cannot be deleted.

### get_attachment

Download an email attachment by filename.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `email_id` | string | *(required)* | Email UID |
| `filename` | string | *(required)* | Attachment filename |
| `folder` | string | `INBOX` | Mailbox folder |
| `save_path` | string | | Unavailable on the hosted server; omit to receive base64 |

---


## License

MIT; see [LICENSE](LICENSE).
