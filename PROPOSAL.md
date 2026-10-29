# PROPOSAL: TERMINAL_MAIL, a human web client for iCloud Mail

> `>> STATUS: DRAFT // AUTHOR: Oleg Pustovit // 2026-10-28`
> Design artifact (every screen, the design system, motion, flows, keyboard map, and a live keyboard prototype): **https://artifacts.microapps.space/a/mx4gywdnv23g** (source: `docs/design/web-client.html`)

## 1. Summary

`mail.microapps.space` is an MCP server for iCloud Mail today. Humans only get a settings page. This proposal turns the same Go binary into a full desktop-grade web mail client with offline PWA support. It is Gmail-class, keyboard-first and local-first, and it runs alongside the MCP interface on one shared sync engine.

Three bets:

1. **The server keeps one mirror per user.** The Go binary holds a persistent IMAP session per connected account (IDLE + CONDSTORE). It mirrors mailbox metadata into MongoDB and writes every change into a per-user, monotonically numbered **changelog**, like Linear's `lastSyncId`.
2. **The browser is a replica, not a viewer.** A Web Worker holds the mailbox in IndexedDB, bootstraps once, then tails the changelog over Server-Sent Events. Every read is local (0 network), and every write is an optimistic transaction confirmed later. That is how the client can beat Mail.app on perceived latency: Mail.app waits on IMAP round-trips, and we never do on the hot path.
3. **Agents and humans share one write path.** MCP tools stop opening throwaway IMAP connections and go through the same session manager and mutation log. When Claude archives a thread through MCP, the human sees it leave the inbox within about 100 ms, tagged `AGENT`. The reverse holds too: MCP reads hit the warm mirror.

It still ships as one binary: `go build ./cmd/mail` embeds the React app, the service worker and the manifest (`web/embed.go` already does this for `dist/`).

## 2. Goals and non-goals

**Goals**
- Desktop-grade three-pane client: conversations, compose, search, folders, flags, snooze, send-later, undo-send, aliases, signatures, rules, attachments, bulk actions.
- Full keyboard operation (Gmail-compatible map plus ⌘K command palette), with a visible focus model and no mouse required.
- Perceived latency budgets (see §9): keypress to paint under 16 ms, open a cached message under 50 ms, new mail on screen within 2 s of iCloud receiving it.
- PWA: installable, offline read/triage/compose with an outbox, push notifications, `mailto:` handler, app badge.
- Design system built on the reference image, plus documented sources: tiled windows with ▢▣✕ controls (working: collapse to a spine, maximize), Mac HIG racing stripes on the active title only, QuickDraw 8×8 fill patterns as tokens, Susan-Kare-style 9×9 bitmap icons, Silkscreen display + IBM Plex Mono chrome + Plex Sans reading, Doto dot-matrix numerals, barcode progress, crop marks, `>> … //` prompt lines, a graphite accent and stepped CRT motion. Each signature element has one job and place (see the artifact's signature map and reference board). Fonts are self-hosted under `web/public/fonts` (all OFL).
- The web UI complements agents and is a complete mail client on its own. Agents work through MCP and own their approval UX; the web UI shows their changes live, attributed and undoable, and never blocks them.

**Non-goals (v1)**
- Multiple iCloud accounts per user (the data model allows it; the UI ships with one).
- Calendar/Contacts (CalDAV/CardDAV). Contacts autocomplete comes from mail history only.
- Server-side iCloud rules, Hide My Email creation, Mail Drop, and VIP sync to Apple devices. These are not exposed over IMAP; §6 lists what we do instead.
- Native wrappers (Electron/Tauri). The PWA is the desktop app.

## 3. Current state (what we reuse)

| Piece | Today | Reuse |
| --- | --- | --- |
| `cmd/mail` | `net/http` mux, auth, OAuth, MCP at `/mcp`, React settings SPA embedded from `web/dist` | Same binary; add `/api/sync/*`, `/sw.js`, manifest |
| `internal/components/auth` | Users, sessions (`__Host-mail_session`, HttpOnly, Secure, Lax), AES-GCM iCloud creds, OAuth grants | Session auth for the web client as-is; OAuth grants list for the Agents page |
| `imap/client.go` | go-imap **v1**, one connection per MCP call, full-message fetch, search, flags incl. Apple flag colors, folders, drafts, SaveSent | Parsing (`parseEmailBody`, attachments, `sentFolder`), migrate transport to go-imap **v2** |
| `smtp/` | STARTTLS 587, saves copy to Sent | Unchanged; called from the outbox worker |
| `internal/components/mailcache` | 1-minute Mongo cache of full messages, invalidated on mutation | Superseded by the mirror (`mailsync`). Delete after migration |
| `tools/` | 14 MCP tools on `tools.EmailService` interface | Keep the interface; back it with `mailsync` instead of a fresh IMAP client |
| `web/` | React 19 + Vite 8, no other deps | Same stack; the mail client becomes the main route, settings move into it |

## 4. Architecture

```
 Browser (PWA)                                     Go binary (cmd/mail)                         iCloud
┌───────────────────────────────┐                ┌───────────────────────────────────────┐
│ UI thread (React 19)           │                │ authhttp  (sessions, OAuth)           │
│  • virtual list, reader iframe │                │                                       │
│  • reads only from store snap  │                │ /api/sync/bootstrap  ─┐               │
│        ▲ postMessage (diffs)   │                │ /api/sync/stream (SSE)├─ syncapi      │
│ SharedWorker "replica"         │  HTTP/2 + SSE  │ /api/sync/mutate      │               │
│  • IndexedDB (meta, bodies,    │◀──────────────▶│ /api/body /api/att /api/img (proxy)   │
│    tx queue, cursor)           │                │            │                          │
│  • in-memory index + FTS       │                │      mailsync (component)             │
│  • optimistic tx + rebase      │                │  ┌─ Session manager (per account) ─┐  │  IMAPS 993
│ Service worker                 │                │  │ conn A: IDLE on INBOX           │──┼──────────▶
│  • app shell precache          │                │  │ conn B: commands + poll others  │──┼──────────▶
│  • push, badge, outbox wake    │                │  └─────────────┬───────────────────┘  │
└───────────────────────────────┘                │   Mongo: messages, folders,           │  SMTP 587
                                                  │   changelog, tx, outbox, prefs        │──────────▶
 MCP clients (Claude etc.) ──── /mcp (OAuth) ───▶│ mailmcp → tools → mailsync            │
                                                  └───────────────────────────────────────┘
```

New code lives where the codebase already puts it (`build-go-stack` layout):
- `internal/components/mailsync/`: session manager, mirror, changelog, mutation applier, outbox and scheduler. Owns its Mongo collections.
- `cmd/mail/syncapi/`: HTTP transport (bootstrap, SSE, mutate, body, attachment, image proxy), composed in `cmd/mail/main.go` like `authhttp`.
- `web/src/`: client app, `web/src/replica/` (worker), `web/public/sw.js`, `manifest.webmanifest`.

## 5. Sync engine

### 5.1 Server mirror (IMAP → Mongo)

Per connected account the session manager keeps **two** IMAP connections. iCloud caps concurrent sessions per account (reports range from about 10 to 15), and the user's own Apple devices use some of them, so we never exceed two.

- **Conn A, `IDLE` on INBOX.** On `EXISTS`/`EXPUNGE`/`FETCH` it wakes the reconciler. IDLE is re-issued every 25 min (RFC 2177 says under 29). On drop, reconnect with jittered backoff, capped at 60 s.
- **Conn B, commands.** It runs the mutation applier, body fetches and the folder reconciler. While someone is connected (an SSE client or a recent MCP call), the reconciler polls non-INBOX folders every 30 s with `STATUS (UIDNEXT UIDVALIDITY HIGHESTMODSEQ MESSAGES UNSEEN)`. That one cheap round-trip for all folders decides which folders need work. With no viewers, folders are polled every 10 min, and the INBOX IDLE stays up so push notifications still work.

Reconcile per folder:
1. `UIDVALIDITY` changed: drop that folder's mirror and resync (rare).
2. **CONDSTORE path** (iCloud advertises it post-login): `UID FETCH 1:* (FLAGS) (CHANGEDSINCE <modseq>)` returns flag changes. New UIDs above the stored `UIDNEXT` get `UID FETCH n:* (UID FLAGS INTERNALDATE RFC822.SIZE ENVELOPE BODYSTRUCTURE BODY.PEEK[HEADER.FIELDS (References In-Reply-To List-Unsubscribe List-Id Auto-Submitted)] BODY.PEEK[TEXT]<0.512>)`. The 512-byte partial gives the snippet.
3. **Expunges without QRESYNC:** iCloud's QRESYNC is reported as inconsistent, so we do not rely on it. When `MESSAGES` (from STATUS) is lower than the mirror count for the folder, run `UID SEARCH ALL` (or `ESEARCH` if present) and diff the UID set. That costs about 4 bytes per message, e.g. about 200 KB for 50k messages, and happens only when the count shows a removal.
4. **Fallback (no CONDSTORE):** flag sync for the newest 2,000 UIDs every poll, and the full folder hourly.

Initial import: newest first, INBOX first, metadata only. The first 500 inbox rows reach the client within about 2 s. History backfills in 1,000-UID windows at low priority. Bodies are fetched on demand, then prefetched as in §5.4.

Threading: iCloud does not offer `THREAD`. The server computes `threadId` at ingest with the JWZ algorithm over `Message-ID`/`References`/`In-Reply-To`, falling back to a normalized subject within 30 days. Conversations span folders (Sent replies thread with Inbox), as in Gmail.

### 5.2 Changelog (the Linear part)

Every mirror write appends `{_id: syncId, owner, entity: "msg"|"folder"|"thread"|"pref"|"outbox", op: "put"|"del", key, patch, actor: "imap"|"web:<session>"|"mcp:<client_id>", txId?}`. `syncId` is a per-owner counter incremented atomically (`findOneAndUpdate $inc` on `counters`), so ordering is total per user. Changelog entries are TTL-pruned after 14 days. A client older than that re-bootstraps, which costs about 1 s.

### 5.3 Wire protocol

| Endpoint | Purpose |
| --- | --- |
| `GET /api/sync/bootstrap?since=0` | Folders, prefs, identities, and the newest 2,000 messages' metadata across Inbox/Flagged/Drafts/Snoozed, plus `syncId`. Streamed NDJSON, gzip, about 150 B per row compressed (2,000 rows ≈ 300 KB) |
| `GET /api/sync/stream?cursor=N` | **SSE.** Replays `syncId > N` and then tails live changes. Heartbeat every 15 s; `Last-Event-ID` resumes on reconnect. SSE avoids a WebSocket dependency, works through the existing reverse proxy, and HTTP/2 removes the per-domain connection limit |
| `POST /api/sync/mutate` | Batch of `{txId, op, args}`. Ops: `setFlags`, `move`, `archive`, `trash`, `deleteForever`, `markThread`, `snooze`, `unsnooze`, `send`, `scheduleSend`, `cancelSend`, `saveDraft`, `createFolder`, `renameFolder`, `deleteFolder`, `setPref`, `rule.*`. Returns per-tx `ok`/`rejected(reason)`; the authoritative change arrives over SSE carrying the same `txId` |
| `GET /api/body/:msgKey` | Sanitized HTML + text + attachment manifest. `Cache-Control: private, max-age=31536000, immutable`, because a UID plus UIDVALIDITY never changes content |
| `GET /api/att/:msgKey/:part` | Streams one MIME part (`BODY.PEEK[n]`). No full-message download (today's code fetches all of it) |
| `GET /api/img?u=…&sig=…` | Remote image proxy. HMAC-signed URLs, blocks private IP ranges (SSRF), strips cookies and referrer, max 10 MB, image MIME only. Hides the reader's IP from trackers |
| `GET /api/search?q=` | Server search over the Mongo mirror (text index on subject/from/to/snippet), with IMAP `SEARCH` fallback for bodies |

All of these routes sit behind the existing session cookie and `Origin` check. `/mcp` keeps bearer OAuth.

### 5.4 Client replica

- A **SharedWorker** (falling back to a dedicated Worker on Safari < 16) owns IndexedDB and the SSE connection. Multiple tabs share one stream and one cache.
- Stores: `meta` (msgKey → row), `threads`, `folders`, `bodies` (LRU, 200 MB soft cap, uses `navigator.storage.persist()`), `tx` (pending mutations), `cursor`.
- **Hydration:** on boot the worker reads `meta` into memory (50k rows ≈ 15 MB of JS objects, about 120 ms) and posts the first visible window to the UI before finishing. The UI paints the inbox from that window in the first frame.
- **Optimistic transactions:** UI action → worker applies the patch locally, emits a diff to the UI, puts it in the `tx` store, and POSTs a batch every 50 ms. SSE echoes carrying that `txId` clear it. On reject, the worker rolls back, shows a toast, and rebases later txs. Conflicts are last-writer-wins per field (flags, folder), with the server as arbiter. Mail has no rich-text co-editing, so no CRDT is needed.
- **Prefetch:** bodies for the top 30 inbox rows on idle, j/k neighbours (±3) on focus, and on `mouseenter` after 80 ms. With that, about 95% of opens should be IDB hits (<20 ms).
- **Local search:** an in-worker inverted index over subject/from/to/snippet gives instant results while typing. Server search merges in after 150 ms of idle.

### 5.5 UI performance

- List virtualization by hand (fixed 56 px rows, ~40 lines, no dependency). Rows are `transform`ed, not re-mounted.
- The reader renders HTML into a **sandboxed `<iframe srcdoc>`** (`sandbox="allow-popups allow-popups-to-escape-sandbox"`, no scripts) with its own strict CSP. Sanitization also runs server-side with an allowlist (bluemonday). Two layers.
- Keyboard handling is one global keymap dispatcher, so no per-component listeners. Actions are pure functions on the store, so `e` archive is a synchronous local patch and the row animates out in the same frame.
- Bundle budget: under 120 KB gz JS for the app shell, with React 19 and no UI kit. Fonts are self-hosted from `web/public/fonts` because the existing CSP blocks third-party origins.

## 6. iCloud feature matrix

| Feature | Via IMAP? | How we expose it |
| --- | --- | --- |
| Folders, nested, special-use (Inbox, Drafts, Sent Messages, Junk, Deleted Messages, Archive) | Yes (LIST/XLIST) | Sidebar tree; create/rename/delete/move (`CREATE`/`RENAME`/`DELETE`) |
| Read/unread, answered, forwarded | `\Seen` `\Answered` `$Forwarded` | Row states, `Shift+U`/`Shift+I` |
| Flag + Apple flag colors (red, orange, yellow, green, blue, purple, gray) | `\Flagged` + `$MailFlagBit0..2` (already implemented in `FlagEmail`) | `S` flag, `Shift+S` color picker. Colors render as **patterns** in monochrome mode and as true colors with SIGNAL mode on, so they sync 1:1 with Apple Mail |
| Junk / not junk | `$Junk` / `$NotJunk` + move to Junk | `!` marks junk; "Not junk" banner in Junk |
| Archive | MOVE to Archive | `E`, swipe on mobile |
| Trash / delete forever | MOVE to Deleted Messages / `UID EXPUNGE` | `#`; delete forever only inside Trash, behind a confirm |
| Drafts | APPEND to Drafts, replace on save | Autosave every 3 s, so drafts appear in Apple Mail too |
| Sent copy | APPEND to Sent (done) | Unchanged |
| Aliases (`@icloud.com` aliases, custom domain addresses, Hide My Email addresses) | Send-from works over SMTP; the list is **not** discoverable over IMAP | Identities page. Auto-suggest from addresses seen in `To:` of received mail; the user confirms and SMTP verifies on first send |
| Storage quota | `GETQUOTAROOT` if advertised post-login | Sidebar meter; hidden if not supported |
| Threads | No `THREAD` ext. | Server JWZ threading (§5.1) |
| Push | `XAPPLEPUSHSERVICE` is Apple-only; IDLE for us | Server IDLE → SSE (open tabs) + Web Push (closed app) |
| Unsubscribe | `List-Unsubscribe` / `-Post` headers | One-click unsubscribe button in the reader header (RFC 8058 POST) |
| VIP senders | Not in IMAP | App-level prefs; VIP smart folder |
| Snooze | Not in iCloud | App-level: move to an `Snoozed` IMAP folder (created on first use, so it is visible in Apple Mail), plus a scheduler that moves the message back and marks it unread |
| Send later / undo send | Not in iCloud | Server outbox with `sendAt`; undo window 5/10/20/30 s (pref). Survives closed laptop because the server sends |
| Rules / filters | iCloud rules not exposed | Server rules engine on new INBOX mail (from/to/subject/list-id → move/flag/mark read/forward). Runs in the IDLE reconciler |
| Remote content blocking (like Mail Privacy Protection) | n/a | Default block, proxy on load (§5.3) |
| Mail Drop, Hide My Email creation, iCloud rules import | No | Out of scope; link to icloud.com |

## 7. MCP alongside

- `mailmcp.Resolver` returns a `tools.EmailService` backed by `mailsync` instead of `imap.NewClient`. Reads (`search_emails`, `get_email`, `count_emails`, `list_folders`) hit the mirror, so they are fast and do not open new IMAP connections. Writes go through the same mutation applier with `actor: "mcp:<client_id>"`.
- **Agent activity**: the changelog already records actor and client, so `Settings → Agents` shows a live feed ("CLAUDE_DESKTOP archived 14 messages · 12:03 · [UNDO]"). Undo replays the inverse mutation.
- **No approval flow in the web UI.** Approvals belong to the agent and its client (e.g. Claude's tool-permission prompts). The web UI only shows the result: agent-sent mail appears in Sent tagged with the client, and every agent change is in the activity log with undo. Revoking a client stays on the Agent activity page.
- Tool contracts (names, schemas) do not change. The README tool docs stay valid.

## 8. Keyboard model

A single focus model with three focusable regions (sidebar, list, reader) plus overlays. Focus is always visible as the inverted row or bracketed panel title. Gmail-compatible defaults, rebindable in settings:

| Keys | Action | Keys | Action |
| --- | --- | --- | --- |
| `j` / `k` | next / prev conversation | `⌘K` / `Ctrl+K` | command palette |
| `o` / `Enter` | open | `/` | search |
| `u` / `Esc` | back to list / close overlay | `c` | compose (`Shift+C` new window) |
| `e` | archive | `r` / `a` / `f` | reply / reply all / forward |
| `#` | trash | `⌘Enter` | send |
| `s` | flag toggle; `Shift+S` color | `⌘Shift+Z` | undo last action (also `z`) |
| `Shift+I` / `Shift+U` | mark read / unread | `h` | snooze picker |
| `v` | move to folder (fuzzy) | `l` | label = move copy (folders) |
| `x` | select; `Shift+J/K` extend | `*a` / `*n` / `*u` | select all / none / unread |
| `g i` `g s` `g d` `g t` `g a` `g f` | go Inbox / Sent / Drafts / Trash / Archive / Flagged | `?` | shortcut sheet |
| `n` / `p` | next / prev message in thread | `;` | expand all in thread |
| `!` | junk | `⌘,` | settings |

Chords time out after 900 ms and show a `g _` HUD in the status bar. Every menu item displays its key, and the palette lists every action with its binding, so the keymap is self-teaching.

## 9. Performance budgets and how we verify them

| Metric | Budget | Mechanism | Measured by |
| --- | --- | --- | --- |
| Cold start (SW-cached shell) → inbox painted | < 300 ms | Precached shell, IDB window paint before full hydrate | `performance.mark` + Playwright trace in CI |
| First-ever load → inbox | < 2.5 s | Streamed bootstrap, newest 500 first | same |
| Keypress → frame | < 16 ms (p95) | Local store, no network on hot path | Event Timing API (INP) sampled to `/api/telemetry` (opt-in) |
| Open message (prefetched) | < 50 ms | IDB body, iframe srcdoc | same |
| Open message (cold) | < 400 ms | Partial `BODY.PEEK[1]` instead of full RFC822 | server histogram |
| New mail → visible | < 2 s | IDLE → changelog → SSE | synthetic: send to self, measure |
| Web action → IMAP applied | < 1 s p95 | 50 ms batching, dedicated conn B | server histogram |
| JS shell | < 120 KB gz | No UI deps | `vite build` size check in `make` |

"Faster than native" means faster perceived latency than Mail.app on the same account. Mail.app blocks on IMAP for search and for opening un-downloaded bodies; we answer both from the local replica first.

## 10. PWA

- `manifest.webmanifest`: `display: standalone`, `display_override: ["window-controls-overlay"]` so our window chrome *is* the title bar on desktop, theme `#0A0A0A`/`#F2F2EE`, maskable icons, `shortcuts` (Compose, Inbox, Search), `protocol_handlers: [{protocol: "mailto", url: "/compose?to=%s"}]`, `share_target` for attachments.
- `sw.js`: hand-written (~80 lines, no Workbox). Precache the hashed Vite assets list generated at build; network-first for `/api/*` except bodies (cache-first, immutable). `push` → notification with Archive/Reply actions. `navigator.setAppBadge(unread)`.
- **Web Push**: VAPID keys generated once and stored encrypted next to the iCloud creds. The server sends pushes from the IDLE reconciler when no SSE client is connected for that user. This needs a small Web Push encryption implementation (RFC 8291, ~150 lines on `crypto/ecdh` + `crypto/hkdf`) or `github.com/SherClockHolmes/webpush-go`; recommend the dependency.
- Offline: read, triage and compose all work; mutations queue in `tx`, sends queue in the outbox store, and the status bar shows `OFFLINE // 3 PENDING`.

## 11. Security

- Same-origin cookie session + strict `Origin` check (existing `protect`/authhttp patterns). Mutations require `Content-Type: application/json` and a matching `Origin`.
- CSP gains: `worker-src 'self'`, `frame-src 'self'`, `manifest-src 'self'`, `img-src 'self' data: blob:` (remote images only via `/api/img`). The email iframe has no `allow-scripts` and no `allow-same-origin`.
- HTML sanitization server-side (bluemonday UGC policy + style allowlist), with the iframe sandbox as a second layer. `cid:` images are rewritten to `/api/att`.
- Mirror data is user-scoped by `owner` on every query (same as `mailcache`). Bodies are **not** stored in Mongo; they stay on IMAP plus the browser cache. Only metadata, snippets and the 512-byte preview are mirrored, which narrows what DB access exposes compared with today's cache.
- The image proxy uses HMAC-signed URLs, denies private/link-local IPs after DNS resolution, enforces a size cap and timeout, and validates content types.
- Logout and password reset already revoke sessions. Additionally clear IDB via `Clear-Site-Data: "storage"` on logout.

## 12. Data model (Mongo, db `mail`)

```
sync_accounts   {_id, owner, account, folders:{name:{uidvalidity, uidnext, highestmodseq, count}}, status, lastError}
messages        {_id: owner|account|folder|uidvalidity|uid, owner, threadId, folder, uid, flags[], colors, date,
                 from, to[], cc[], subject, snippet, size, hasAtt, listId, unsub, msgId, refs[], syncId}
                 idx: (owner, folder, date desc) · (owner, threadId) · text(subject, from, to, snippet)
changelog       {_id: syncId, owner, entity, op, key, patch, actor, txId, at}  ttl 14d · idx (owner, _id)
counters        {_id: owner, syncId}
outbox          {_id, owner, raw(encrypted), sendAt, status, actor, txId}
schedules       {_id, owner, kind: "snooze", msgKey, at}
prefs           {_id: owner, keymap, theme, signal, undoSeconds, signatures[], identities[], vip[], rules[], push[]}
```

## 13. Delivery plan

| Phase | Scope | Exit criteria |
| --- | --- | --- |
| **P0 · go-imap v2 + session manager** (1 wk) | Port `imap/client.go` to go-imap v2, add `mailsync` session manager, move MCP onto it, delete `mailcache` | All `tools/` tests green; MCP latency p50 below today's |
| **P1 · Mirror + changelog + API** (1.5 wk) | Reconciler (IDLE, CONDSTORE, UID-diff expunge), threading, bootstrap/SSE/mutate/body/att | Integration test against a recorded IMAP transcript; synthetic new-mail < 2 s |
| **P2 · Client shell + design system** (2 wk) | Tokens, components, three-pane, virtual list, reader iframe, keyboard dispatcher, palette | Budgets in §9 pass in the Playwright perf test |
| **P3 · Compose & outbox** (1 wk) | Compose window, drafts autosave, identities, signatures, undo/send-later, attachments upload | Send, undo and schedule all survive tab close |
| **P4 · Triage features** (1 wk) | Snooze, flag colors, move/folders, bulk, search operators, rules, unsubscribe, image proxy | Feature matrix §6 rows ticked |
| **P5 · PWA + push + agents** (1 wk) | SW, manifest, push, badge, mailto handler, Agent activity page (live log, undo, revoke) | Lighthouse PWA installable; offline triage e2e |

Test strategy follows the repo: Go table tests with a fake IMAP backend (go-imap v2 ships `imapserver` + `imapmemserver`, so we get a **real in-process IMAP server** for integration tests), and `node --test` for the replica (tx rebase, changelog apply, keymap).

## 14. Risks

| Risk | Mitigation |
| --- | --- |
| iCloud connection cap / throttling | Max 2 conns per account; MCP shares them; backoff; status surfaced in UI (`IMAP: THROTTLED`) |
| QRESYNC inconsistent on iCloud | Not used; STATUS count + UID SEARCH diff for expunges |
| CONDSTORE absent on some shard | Bounded flag-poll fallback (§5.1) |
| Server holds always-on IMAP sessions → memory per user | About 200 KB per session; fine for the invite-only user base (registration is allowlisted) |
| Mongo text search quality | Good enough for metadata; IMAP `SEARCH BODY` fallback; SQLite FTS5 can be revisited if needed |
| go-imap v2 is still beta (`v2.0.0-beta.8`) | Pin the version; it's the maintained line and has IDLE/MOVE/UIDPLUS/SORT/QUOTA plus a test server |
| Sanitizer bypass | Two layers (bluemonday + script-less sandboxed iframe with CSP) |

## 15. Open questions

1. Snooze folder name: `Snoozed` (visible in Apple Mail, recommended) or hidden metadata only?
2. Telemetry for latency budgets: opt-in only (recommended), or off entirely?

## 16. Research notes and sources

- iCloud IMAP pre-login `CAPABILITY` (probed 2026-10-28): `XAPPLEPUSHSERVICE IMAP4 IMAP4rev1 SASL-IR AUTH=ATOKEN AUTH=PLAIN AUTH=ATOKEN2 AUTH=XOAUTH2`. Post-login reports show CONDSTORE + IDLE; QRESYNC is reported as inconsistent. **Verify post-login caps with a real account in P0.** [Apple dev forum thread](https://developer.apple.com/forums/thread/694251), [Wino Mail iCloud QRESYNC fix](https://homehud.duckdns.org/javier/Wino-Mail/src/commit/c1336428dc9546e44dc9c5588f5fe5368b8d82ff/Wino.Core/Synchronizers/ImapSync/QResyncSynchronizer.cs)
- Linear sync engine: IndexedDB per workspace, `lastSyncId` total order, delta packets of SyncActions, bootstrap then tail, queued transactions resent on reconnect, last-write-wins. [reverse-linear-sync-engine](https://github.com/Aslam97/reverse-linear-sync-engine), [Linear local-first write-up](https://braindetox.kr/en/posts/linear_local_first_speed_2026.html), [How's Linear so fast?](https://1023jack.com/general/how-s-linear-so-fast-a-technical-breakdown/)
- go-imap v2 `imapclient`: IDLE, MOVE (with COPY+EXPUNGE fallback), UIDPLUS, SORT, THREAD, QUOTA, METADATA, ModSeq. [pkg.go.dev](https://pkg.go.dev/github.com/emersion/go-imap/v2/imapclient)
- CONDSTORE-based delta sync in practice: [ImapGoose](https://whynothugo.nl/journal/2025/10/15/introducing-imapgoose/); RFCs 7162 (CONDSTORE/QRESYNC), 2177 (IDLE), 8058 (one-click unsubscribe), 8291 (Web Push encryption).
