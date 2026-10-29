package mailhttp

import (
	"errors"
	"net/http"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
)

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if _, err := imap.ParseQuery(query); err != nil || query == "" {
		respond(w, 400, map[string]string{"error": "invalid_search_query"})
		return
	}
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "search_unavailable"})
		return
	}
	result, err := h.state.RemoteSearch(r.Context(), h.owner(r.Context()), r.Header.Get("X-Mail-Account"), query, r.URL.Query().Get("cursor"))
	if err != nil {
		if errors.Is(err, mailsync.ErrInvalidSearchCursor) {
			respond(w, 400, map[string]string{"error": "invalid_search_cursor"})
			return
		}
		failed(w, r, err)
		return
	}
	respond(w, 200, result)
}
