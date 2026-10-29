package mailhttp

import "net/http"

func (h *handler) operations(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "operations_unavailable"})
		return
	}
	owner := h.owner(r.Context())
	if r.Method == "GET" {
		renames, err := h.state.RenameOperations(r.Context(), owner)
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, renames)
		return
	}
	var err error
	switch r.PathValue("action") {
	case "retry":
		err = h.state.RetryRename(r.Context(), owner, r.PathValue("id"))
	case "cancel":
		err = h.state.CancelRename(r.Context(), owner, r.PathValue("id"))
	default:
		respond(w, 400, map[string]string{"error": "invalid_operation_action"})
		return
	}
	if err != nil {
		respond(w, 409, map[string]string{"error": "rename_already_applied_or_mailbox_changed_retry_recovery"})
		return
	}
	w.WriteHeader(204)
}
