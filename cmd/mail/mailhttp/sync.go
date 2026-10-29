package mailhttp

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *handler) bootstrap(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "sync_unavailable"})
		return
	}
	owner, account := h.owner(r.Context()), r.Header.Get("X-Mail-Account")
	if _, err := h.state.Start(r.Context(), owner); err != nil {
		failed(w, r, err)
		return
	}
	cursor, folders, mail, status, err := h.state.Bootstrap(r.Context(), owner, account)
	if err != nil {
		failed(w, r, err)
		return
	}
	prefs, err := h.state.Preferences(r.Context(), owner)
	if err != nil {
		failed(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	var output io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		compressed := gzip.NewWriter(w)
		defer func() { _ = compressed.Close() }()
		output = compressed
	}
	encoder := json.NewEncoder(output)
	if err = encoder.Encode(map[string]any{"type": "bootstrap", "syncId": cursor, "status": status, "prefs": prefs}); err != nil {
		return
	}
	for _, folder := range folders {
		if encoder.Encode(map[string]any{"type": "folder", "folder": folder}) != nil {
			return
		}
	}
	for _, message := range mail {
		if encoder.Encode(map[string]any{"type": "msg", "message": message}) != nil {
			return
		}
	}
	_ = encoder.Encode(map[string]any{"type": "ready", "syncId": cursor})
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "sync_unavailable"})
		return
	}
	var cursor struct {
		Date time.Time `json:"date"`
		Key  string    `json:"key"`
	}
	if value := r.URL.Query().Get("before"); value != "" {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(raw) > 1024 || json.Unmarshal(raw, &cursor) != nil || cursor.Date.IsZero() || cursor.Key == "" {
			respond(w, 400, map[string]string{"error": "invalid_history_cursor"})
			return
		}
	}
	mail, err := h.state.History(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), cursor.Date, cursor.Key)
	if err != nil {
		failed(w, r, err)
		return
	}
	next := ""
	if len(mail) > 0 {
		last := mail[len(mail)-1]
		cursor.Date = last.Date
		cursor.Key = last.Key
		raw, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	respond(w, 200, map[string]any{"messages": mail, "next": next, "complete": len(mail) < 2000})
}
func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	if h.state != nil {
		done := h.state.Watch(h.owner(r.Context()))
		defer done()
	}
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "sync_unavailable"})
		return
	}
	owner, account := h.owner(r.Context()), r.Header.Get("X-Mail-Account")
	raw := r.URL.Query().Get("cursor")
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		raw = resume
	}
	if raw == "" {
		raw = "0"
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		respond(w, 400, map[string]string{"error": "invalid_sync_cursor"})
		return
	}
	if _, err = h.state.Start(r.Context(), owner); err != nil {
		failed(w, r, err)
		return
	}
	missing, err := h.state.HistoryMissing(r.Context(), owner, account, cursor)
	if err != nil {
		failed(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	flush := http.NewResponseController(w)
	if missing {
		_, _ = io.WriteString(w, "event: reset\ndata: {}\n\n")
		_ = flush.Flush()
		return
	}
	if _, err = io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	if flush.Flush() != nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		changed := h.state.Changed(owner)
		changes, err := h.state.Changes(r.Context(), owner, account, cursor)
		if err != nil {
			return
		}
		for _, change := range changes {
			data, err := json.Marshal(change)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "id: %d\nevent: change\ndata: %s\n\n", change.Sequence, data); err != nil {
				return
			}
			cursor = change.Sequence
		}
		if len(changes) > 0 {
			if flush.Flush() != nil {
				return
			}
			if len(changes) == 1000 {
				continue
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-h.state.Done():
			return
		case <-changed:
		case <-ticker.C:
			if h.session != nil {
				email, err := h.session(r)
				if err != nil || email != owner {
					_, _ = io.WriteString(w, "event: auth_required\ndata: {}\n\n")
					_ = flush.Flush()
					return
				}
			}
			current, err := h.state.Account(r.Context(), owner)
			if err != nil || current != account {
				_, _ = io.WriteString(w, "event: account_changed\ndata: {}\n\n")
				_ = flush.Flush()
				return
			}
			if _, err = io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if flush.Flush() != nil {
				return
			}
		}
	}
}
func (h *handler) thread(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "sync_unavailable"})
		return
	}
	thread := r.PathValue("id")
	if thread == "" || len(thread) > 100 {
		respond(w, 400, map[string]string{"error": "invalid_thread"})
		return
	}
	mail, err := h.state.Thread(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), thread)
	if err != nil {
		failed(w, r, err)
		return
	}
	respond(w, 200, map[string]any{"messages": mail})
}
