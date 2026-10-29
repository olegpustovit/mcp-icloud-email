package mailhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmessage"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
)

// New serves the cookie-authenticated mail API using the same services as MCP.
func New(resolve mailmcp.Resolver, origin string, state *mailsync.Service, owner func(context.Context) string, session func(*http.Request) (string, error)) http.Handler {
	h := &handler{resolve: resolve, origin: origin, state: state, owner: owner, session: session, html: bluemonday.UGCPolicy()}
	h.html.RequireNoReferrerOnLinks(true)
	h.html.AllowURLSchemes("cid")
	h.html.AddTargetBlankToFullyQualifiedLinks(true)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mail/folders", h.folders)
	mux.HandleFunc("GET /api/mail/sync/bootstrap", h.bootstrap)
	mux.HandleFunc("GET /api/mail/sync/stream", h.stream)
	mux.HandleFunc("GET /api/mail/sync/history", h.history)
	mux.HandleFunc("GET /api/sync/bootstrap", h.bootstrap)
	mux.HandleFunc("GET /api/sync/stream", h.stream)
	mux.HandleFunc("GET /api/mail/thread/{id}", h.thread)
	mux.HandleFunc("GET /api/mail/messages", h.messages)
	mux.HandleFunc("GET /api/mail/search", h.search)
	mux.HandleFunc("GET /api/mail/message", h.message)
	mux.HandleFunc("GET /api/mail/attachment", h.attachment)
	mux.HandleFunc("POST /api/mail/remote-image", h.remoteImage)
	mux.HandleFunc("POST /api/mail/mutate", h.mutate)
	mux.HandleFunc("POST /api/mail/send", h.send)
	mux.HandleFunc("POST /api/mail/draft", h.draft)
	mux.HandleFunc("GET /api/mail/preferences", h.preferences)
	mux.HandleFunc("GET /api/mail/activity", h.activity)
	mux.HandleFunc("POST /api/mail/activity/{id}/undo", h.undoActivity)
	mux.HandleFunc("PUT /api/mail/preferences", h.preferences)
	mux.HandleFunc("PATCH /api/mail/preferences", h.preferences)
	mux.HandleFunc("GET /api/mail/push", h.push)
	mux.HandleFunc("POST /api/mail/push", h.push)
	mux.HandleFunc("DELETE /api/mail/push/{id}", h.push)
	mux.HandleFunc("GET /api/mail/rules", h.rules)
	mux.HandleFunc("POST /api/mail/rules/preview", h.rules)
	mux.HandleFunc("POST /api/mail/rules/apply", h.rules)
	mux.HandleFunc("POST /api/mail/rules/jobs/{id}/retry", h.rules)
	mux.HandleFunc("POST /api/mail/outbox", h.schedule)
	mux.HandleFunc("GET /api/mail/outbox", h.outbox)
	mux.HandleFunc("DELETE /api/mail/outbox/{id}", h.cancelSend)
	mux.HandleFunc("POST /api/mail/outbox/{id}/restore", h.restoreSend)
	mux.HandleFunc("GET /api/mail/operations", h.operations)
	mux.HandleFunc("POST /api/mail/operations/{id}/{action}", h.operations)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if owner != nil && r.Header.Get("X-Mail-Owner") != owner(r.Context()) {
			respond(w, 403, map[string]string{"error": "account_changed_reload_before_retrying"})
			return
		}
		if state != nil {
			address, err := state.Account(r.Context(), owner(r.Context()))
			if err != nil || r.Header.Get("X-Mail-Account") != address {
				w.Header().Set("X-Mail-Account-Changed", "1")
				respond(w, 409, map[string]string{"error": "icloud_account_changed_reload_before_retrying"})
				return
			}
		}
		if r.Method != http.MethodGet && (r.Header.Get("Origin") != origin || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json") {
			respond(w, 403, map[string]string{"error": "invalid_origin_or_content_type"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type handler struct {
	state        *mailsync.Service
	owner        func(context.Context) string
	session      func(*http.Request) (string, error)
	resolve      mailmcp.Resolver
	origin       string
	html         *bluemonday.Policy
	remoteClient *http.Client
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *handler) open(w http.ResponseWriter, r *http.Request) (mailmcp.Dependencies, bool) {
	d, err := h.resolve(r.Context())
	if err != nil {
		slog.WarnContext(r.Context(), "open web mailbox", "error", err)
		respond(w, 502, map[string]string{"error": "mailbox_unavailable_check_connection"})
		return d, false
	}
	if r.Header.Get("X-Mail-Account") != d.FromEmail {
		closeMail(d)
		w.Header().Set("X-Mail-Account-Changed", "1")
		respond(w, 409, map[string]string{"error": "icloud_account_changed_reload_before_retrying"})
		return d, false
	}
	return d, true
}

func closeMail(d mailmcp.Dependencies) {
	if d.Close != nil {
		d.Close()
	}
}
func failed(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "web mail operation", "path", r.URL.Path, "error", err)
	respond(w, 502, map[string]string{"error": "mail_operation_failed_try_refreshing"})
}

func (h *handler) folders(w http.ResponseWriter, r *http.Request) {
	if h.state != nil {
		owner, account := h.owner(r.Context()), r.Header.Get("X-Mail-Account")
		if err := h.state.WaitReady(r.Context(), owner); err != nil {
			failed(w, r, err)
			return
		}
		folders, err := h.state.Folders(r.Context(), owner, account)
		if err != nil {
			failed(w, r, err)
			return
		}
		names := make([]string, len(folders))
		for i, f := range folders {
			names[i] = f.Name
		}
		status, err := h.state.Status(r.Context(), owner, account)
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, map[string]any{"folders": names, "details": folders, "sync": status})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	folders, err := d.IMAP.ListFolders(r.Context())
	if err != nil {
		failed(w, r, err)
		return
	}
	respond(w, 200, map[string]any{"folders": folders})
}

func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	offset, err := strconv.Atoi(q.Get("offset"))
	if q.Get("offset") == "" {
		offset, err = 0, nil
	}
	if !validFolder(folder) || err != nil || offset < 0 || len(q.Get("q")) > 1000 {
		respond(w, 400, map[string]string{"error": "invalid_query"})
		return
	}
	if h.state != nil {
		query := q.Get("q")
		if q.Get("unread") == "1" {
			query += " is:unread"
		}
		mail, total, err := h.state.MirrorSearch(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), folder, query, 100, offset)
		if err != nil {
			failed(w, r, err)
			return
		}
		folders, err := h.state.Folders(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"))
		if err != nil {
			failed(w, r, err)
			return
		}
		unread := int64(0)
		partial := false
		for _, f := range folders {
			if f.Name == folder {
				unread = int64(f.Unread)
				partial = !f.Complete
				if query == "" {
					total = int64(f.Total)
				}
			}
		}
		respond(w, 200, map[string]any{"emails": mail, "total": total, "unread": unread, "partial": partial, "folder": folder, "offset": offset})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	emails, total, err := d.IMAP.SearchEmails(r.Context(), folder, q.Get("q"), imap.EmailFilters{Limit: 100, Offset: offset, UnreadOnly: q.Get("unread") == "1"})
	if err != nil {
		failed(w, r, err)
		return
	}
	respond(w, 200, map[string]any{"emails": emails, "total": total, "folder": folder, "offset": offset})
}

func validFolder(folder string) bool {
	return folder != "" && len(folder) <= 1000 && !strings.ContainsAny(folder, "\x00\r\n")
}
func validMessage(folder, id string) bool {
	n, err := strconv.ParseUint(id, 10, 32)
	return validFolder(folder) && err == nil && n > 0
}

func checkValidity(w http.ResponseWriter, r *http.Request, d mailmcp.Dependencies, folder string, expected uint32) bool {
	if expected == 0 {
		respond(w, 400, map[string]string{"error": "mailbox_identity_required_refresh_mail"})
		return false
	}
	reader, ok := d.IMAP.(interface {
		UIDValidity(context.Context, string) (uint32, error)
	})
	if !ok {
		respond(w, 503, map[string]string{"error": "mailbox_identity_unavailable"})
		return false
	}
	actual, err := reader.UIDValidity(r.Context(), folder)
	if err != nil {
		failed(w, r, err)
		return false
	}
	if actual != expected {
		respond(w, 409, map[string]string{"error": "mailbox_changed_refresh_before_retrying"})
		return false
	}
	return true
}

func (h *handler) message(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !validMessage(q.Get("folder"), q.Get("id")) {
		respond(w, 400, map[string]string{"error": "invalid_message"})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	validity, _ := strconv.ParseUint(q.Get("validity"), 10, 32)
	if !checkValidity(w, r, d, q.Get("folder"), uint32(validity)) {
		return
	}
	email, err := d.IMAP.GetEmail(r.Context(), q.Get("folder"), q.Get("id"))
	if err != nil {
		failed(w, r, err)
		return
	}
	copy := *email
	strip := true
	if h.state != nil {
		prefs, prefErr := h.state.Preferences(r.Context(), h.owner(r.Context()))
		if prefErr != nil {
			failed(w, r, prefErr)
			return
		}
		strip = prefs.StripTracking
	}
	content, images, stripped := messageContent(h.html.Sanitize(copy.BodyHTML), strip, copy.InlineImages)
	copy.BodyHTML = content
	copy.InlineImages = nil
	respond(w, 200, struct {
		*imap.Email
		RemoteImages     []remoteImage `json:"remoteImages"`
		TrackersStripped int           `json:"trackersStripped"`
	}{&copy, images, stripped})
}

func (h *handler) attachment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !validMessage(q.Get("folder"), q.Get("id")) || q.Get("filename") == "" {
		respond(w, 400, map[string]string{"error": "invalid_attachment"})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	validity, _ := strconv.ParseUint(q.Get("validity"), 10, 32)
	if !checkValidity(w, r, d, q.Get("folder"), uint32(validity)) {
		return
	}
	a, err := d.IMAP.GetAttachment(r.Context(), q.Get("folder"), q.Get("id"), q.Get("filename"))
	if err != nil {
		failed(w, r, err)
		return
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if q.Get("preview") == "1" {
		detected := strings.Split(http.DetectContentType(a.Content), ";")[0]
		switch detected {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain":
			contentType, disposition = detected, "inline"
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(a.Content)))
	_, _ = w.Write(a.Content) //nolint:gosec // Sniffed raster/PDF/text previews are inline; other bytes are downloads.
}

func input(w http.ResponseWriter, r *http.Request, v any) bool {
	limit := int64(1 << 20)
	if r.URL.Path == "/api/mail/send" || r.URL.Path == "/api/mail/draft" || r.URL.Path == "/api/mail/outbox" {
		limit = 20 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return false
	}
	return true
}

type mutation struct {
	Op                  string     `json:"op"`
	Folder              string     `json:"folder"`
	ID                  string     `json:"id"`
	Target              string     `json:"target"`
	Read                bool       `json:"read"`
	Color               string     `json:"color"`
	Permanent           bool       `json:"permanent"`
	TransactionID       string     `json:"transactionId"`
	Validity            uint32     `json:"validity"`
	Until               *time.Time `json:"until"`
	OriginalTransaction string     `json:"originalTransaction"`
}

func (h *handler) mutate(w http.ResponseWriter, r *http.Request) {
	var m mutation
	if !input(w, r, &m) {
		return
	}
	if len(m.TransactionID) > 100 {
		respond(w, 400, map[string]string{"error": "invalid_transaction"})
		return
	}
	r = r.WithContext(mailsync.Transaction(r.Context(), m.TransactionID))
	folderOp := m.Op == "createFolder" || m.Op == "deleteFolder" || m.Op == "renameFolder" || m.Op == "undoMove"
	if !validFolder(m.Folder) || (!folderOp && !validMessage(m.Folder, m.ID)) || (m.Op == "move" || m.Op == "renameFolder") && !validFolder(m.Target) || m.Op == "undoMove" && (m.OriginalTransaction == "" || len(m.OriginalTransaction) > 100) {
		respond(w, 400, map[string]string{"error": "invalid_mutation"})
		return
	}
	if !slices.Contains([]string{"read", "flag", "move", "delete", "createFolder", "deleteFolder", "renameFolder", "snooze", "unsnooze", "undoMove"}, m.Op) {
		respond(w, 400, map[string]string{"error": "unknown_operation"})
		return
	}
	if m.Op == "flag" && m.Color != "" && !slices.Contains([]string{"red", "orange", "yellow", "green", "blue", "purple", "gray"}, m.Color) {
		respond(w, 400, map[string]string{"error": "invalid_color"})
		return
	}
	if h.state != nil && m.TransactionID != "" {
		cursor, err := h.state.TransactionCommitted(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), m.TransactionID)
		if err != nil {
			failed(w, r, err)
			return
		}
		if cursor > 0 {
			respond(w, 200, map[string]any{"syncId": cursor, "txId": m.TransactionID})
			return
		}
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	if !folderOp && !checkValidity(w, r, d, m.Folder, m.Validity) {
		return
	}
	var err error
	switch m.Op {
	case "read":
		err = d.IMAP.MarkRead(r.Context(), m.Folder, m.ID, m.Read)
	case "flag":
		kind := "follow-up"
		if m.Color == "" {
			kind = "none"
		}
		err = d.IMAP.FlagEmail(r.Context(), m.Folder, m.ID, kind, m.Color)
	case "move":
		if box, ok := d.IMAP.(interface {
			MoveWithValidity(context.Context, imap.Location, string) error
		}); ok {
			err = box.MoveWithValidity(r.Context(), imap.Location{Folder: m.Folder, ID: m.ID, Validity: m.Validity}, m.Target)
		} else {
			err = d.IMAP.MoveEmail(r.Context(), m.Folder, m.Target, m.ID)
		}
	case "snooze", "unsnooze", "undoMove", "renameFolder":
		box, ok := d.IMAP.(*mailsync.Mailbox)
		if !ok {
			respond(w, 503, map[string]string{"error": "mail_state_unavailable"})
			return
		}
		location := imap.Location{Folder: m.Folder, ID: m.ID, Validity: m.Validity}
		switch m.Op {
		case "snooze":
			if m.Until != nil && (!m.Until.After(time.Now()) || m.Until.After(time.Now().AddDate(1, 0, 0))) {
				respond(w, 400, map[string]string{"error": "choose_a_future_time_within_one_year"})
				return
			}
			err = box.Snooze(r.Context(), location, m.Until)
		case "unsnooze":
			err = box.Unsnooze(r.Context(), location)
		case "undoMove":
			err = box.UndoMove(r.Context(), m.OriginalTransaction)
		case "renameFolder":
			err = box.RenameFolder(r.Context(), m.Folder, m.Target)
		}
	case "delete":
		err = d.IMAP.DeleteEmail(r.Context(), m.Folder, m.ID, m.Permanent)
	case "createFolder":
		err = d.IMAP.CreateFolder(r.Context(), m.Folder, "")
	case "deleteFolder":
		_, _, err = d.IMAP.DeleteFolder(r.Context(), m.Folder, false)
	}
	if err != nil {
		if errors.Is(err, imap.ErrLocationChanged) {
			respond(w, 409, map[string]string{"error": "message_location_changed_refresh_before_retrying"})
			return
		}
		if errors.Is(err, imap.ErrMarkerUnsupported) {
			respond(w, 400, map[string]string{"error": "mailbox_does_not_support_recoverable_moves"})
			return
		}
		failed(w, r, err)
		return
	}
	if h.state != nil && m.TransactionID != "" {
		cursor, err := h.state.Cursor(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"))
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, map[string]any{"syncId": cursor, "txId": m.TransactionID})
		return
	}
	w.WriteHeader(204)
}

type composeInput struct {
	To            []string                 `json:"to"`
	CC            []string                 `json:"cc"`
	BCC           []string                 `json:"bcc"`
	Subject       string                   `json:"subject"`
	Body          string                   `json:"body"`
	ReplyFolder   string                   `json:"replyFolder"`
	ReplyID       string                   `json:"replyId"`
	ReplyAll      bool                     `json:"replyAll"`
	DraftID       string                   `json:"draftId"`
	DraftFolder   string                   `json:"draftFolder"`
	DraftValidity uint32                   `json:"draftValidity"`
	ReplyValidity uint32                   `json:"replyValidity"`
	From          string                   `json:"from"`
	HTML          bool                     `json:"html"`
	Attachments   []mailmessage.Attachment `json:"attachments"`
}

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	var m composeInput
	if !input(w, r, &m) {
		return
	}
	if len(m.Subject) > 1000 || len(m.To)+len(m.CC)+len(m.BCC) > 100 || m.ReplyID != "" && !validMessage(m.ReplyFolder, m.ReplyID) {
		respond(w, 400, map[string]string{"error": "invalid_message"})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	opts := smtp.SendOptions{CC: m.CC, BCC: m.BCC, From: m.From, HTML: m.HTML, Attachments: m.Attachments}
	var err error
	if m.ReplyID != "" {
		if !checkValidity(w, r, d, m.ReplyFolder, m.ReplyValidity) {
			return
		}
		original, e := d.IMAP.GetEmail(r.Context(), m.ReplyFolder, m.ReplyID)
		if e != nil {
			failed(w, r, e)
			return
		}
		err = d.SMTP.ReplyToEmail(r.Context(), original, m.Body, m.ReplyAll, opts)
	} else {
		from := m.From
		if from == "" {
			from = d.FromEmail
		}
		err = d.SMTP.SendEmail(r.Context(), from, m.To, m.Subject, m.Body, opts)
	}
	var sentCopy *smtp.SentCopyError
	if errors.As(err, &sentCopy) {
		respond(w, 200, map[string]string{"warning": "sent_but_sent_copy_failed_do_not_resend"})
		return
	}
	if err != nil {
		failed(w, r, fmt.Errorf("SMTP send (check Sent before retrying): %w", err))
		return
	}
	w.WriteHeader(204)
}
func (h *handler) draft(w http.ResponseWriter, r *http.Request) {
	var m composeInput
	if !input(w, r, &m) {
		return
	}
	if len(m.Subject) > 1000 || len(m.To)+len(m.CC)+len(m.BCC) > 100 {
		respond(w, 400, map[string]string{"error": "invalid_message"})
		return
	}
	if m.DraftID != "" && !validMessage(m.DraftFolder, m.DraftID) || m.ReplyID != "" && !validMessage(m.ReplyFolder, m.ReplyID) {
		respond(w, 400, map[string]string{"error": "invalid_source_message"})
		return
	}
	if err := mailmessage.ValidateAttachments(m.Attachments); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	d, ok := h.open(w, r)
	if !ok {
		return
	}
	defer closeMail(d)
	if m.DraftID != "" && !checkValidity(w, r, d, m.DraftFolder, m.DraftValidity) {
		return
	}
	if m.ReplyID != "" && !checkValidity(w, r, d, m.ReplyFolder, m.ReplyValidity) {
		return
	}
	from := d.FromEmail
	if h.state != nil {
		var err error
		from, err = h.state.SenderIdentity(r.Context(), h.owner(r.Context()), m.From)
		if err != nil {
			respond(w, 400, map[string]string{"error": "invalid_sender_identity"})
			return
		}
	}
	id, err := d.IMAP.SaveDraft(r.Context(), from, m.To, m.Subject, m.Body, imap.DraftOptions{CC: m.CC, BCC: m.BCC, HTML: m.HTML, Attachments: m.Attachments, ReplyToID: m.ReplyID, Folder: m.ReplyFolder, ReplyValidity: m.ReplyValidity, ReplaceID: m.DraftID, ReplaceValidity: m.DraftValidity})
	var saved *imap.DraftSavedError
	warning := ""
	if errors.As(err, &saved) {
		id = saved.ID
		warning = "draft_saved_previous_version_could_not_be_removed"
		err = nil
	}
	if err != nil {
		failed(w, r, err)
		return
	}
	validity := uint32(0)
	if validFolder(m.DraftFolder) {
		if reader, ok := d.IMAP.(interface {
			UIDValidity(context.Context, string) (uint32, error)
		}); ok {
			validity, _ = reader.UIDValidity(r.Context(), m.DraftFolder)
		}
	}
	respond(w, 200, map[string]any{"id": id, "folder": m.DraftFolder, "validity": validity, "warning": warning})
}
