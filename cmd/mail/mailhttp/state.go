package mailhttp

import (
	"encoding/json"
	"errors"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
	"net/http"
	"strconv"
)

func (h *handler) activity(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "mail_state_unavailable"})
		return
	}
	changes, err := h.state.Activity(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"))
	if err != nil {
		failed(w, r, err)
		return
	}
	respond(w, 200, changes)
}
func (h *handler) undoActivity(w http.ResponseWriter, r *http.Request) {
	sequence, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || sequence <= 0 || h.state == nil {
		respond(w, 400, map[string]string{"error": "invalid_activity"})
		return
	}
	if err = h.state.UndoActivity(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), sequence); err != nil {
		respond(w, 409, map[string]string{"error": "message_changed_or_activity_cannot_be_reversed"})
		return
	}
	w.WriteHeader(204)
}

func (h *handler) preferences(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "mail_state_unavailable"})
		return
	}
	owner := h.owner(r.Context())
	if r.Method == "GET" {
		prefs, err := h.state.Preferences(r.Context(), owner)
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, prefs)
		return
	}
	var err error
	if r.Method == "PATCH" {
		var patch json.RawMessage
		if !input(w, r, &patch) {
			return
		}
		err = h.state.PatchPreferences(r.Context(), owner, patch)
	} else {
		var prefs mailsync.Preferences
		if !input(w, r, &prefs) {
			return
		}
		err = h.state.SavePreferences(r.Context(), owner, prefs)
	}
	if err != nil {
		respond(w, 400, map[string]string{"error": "preferences_could_not_be_saved"})
		return
	}
	w.WriteHeader(204)
}
func (h *handler) schedule(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "outbox_unavailable"})
		return
	}
	var message mailsync.Send
	if !input(w, r, &message) {
		return
	}
	if message.ReplyID != "" && !validMessage(message.ReplyFolder, message.ReplyID) {
		respond(w, 400, map[string]string{"error": "invalid_reply"})
		return
	}
	job, err := h.state.Schedule(r.Context(), h.owner(r.Context()), message)
	if err != nil {
		if errors.Is(err, mailsync.ErrRenamePending) {
			respond(w, 503, map[string]string{"error": "folder_rename_recovery_pending"})
			return
		}
		respond(w, 400, map[string]string{"error": "message_could_not_be_queued_check_recipients_and_time"})
		return
	}
	respond(w, 202, job)
}
func (h *handler) outbox(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "outbox_unavailable"})
		return
	}
	jobs, err := h.state.Outbox(r.Context(), h.owner(r.Context()))
	if err != nil {
		failed(w, r, err)
		return
	}
	respond(w, 200, jobs)
}
func (h *handler) cancelSend(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "outbox_unavailable"})
		return
	}
	message, err := h.state.OutboxMessage(r.Context(), h.owner(r.Context()), r.PathValue("id"))
	if err != nil {
		respond(w, 404, map[string]string{"error": "outbox_message_unavailable"})
		return
	}
	if err := h.state.CancelSend(r.Context(), h.owner(r.Context()), r.PathValue("id")); err != nil {
		respond(w, 409, map[string]string{"error": "send_already_started_check_sent"})
		return
	}
	respond(w, 200, message)
}

func (h *handler) restoreSend(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "outbox_unavailable"})
		return
	}
	message, err := h.state.RestoreUncertain(r.Context(), h.owner(r.Context()), r.PathValue("id"))
	if err != nil {
		respond(w, 409, map[string]string{"error": "unconfirmed_draft_unavailable"})
		return
	}
	respond(w, 200, message)
}
