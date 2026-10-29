package authhttp

import "net/http"

func (h *Handler) clients(w http.ResponseWriter, r *http.Request) {
	owner, err := h.Session(r)
	if err != nil {
		fail(w, 401, "login_required")
		return
	}
	if r.Method == http.MethodDelete {
		if !h.origin(r) {
			fail(w, 403, "invalid_origin")
			return
		}
		if err = h.s.RevokeClient(r.Context(), owner, r.PathValue("id")); err != nil {
			fail(w, 500, "revocation_failed")
			return
		}
		w.WriteHeader(204)
		return
	}
	clients, err := h.s.Clients(r.Context(), owner)
	if err != nil {
		fail(w, 500, "clients_unavailable")
		return
	}
	jsonResponse(w, 200, clients)
}
func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	owner, err := h.Session(r)
	if err != nil {
		fail(w, 401, "login_required")
		return
	}
	cookie, _ := r.Cookie("__Host-mail_session")
	if r.Method == http.MethodDelete {
		if !h.origin(r) {
			fail(w, 403, "invalid_origin")
			return
		}
		if err = h.s.RevokeOtherSessions(r.Context(), owner, cookie.Value); err != nil {
			fail(w, 500, "revocation_failed")
			return
		}
		w.WriteHeader(204)
		return
	}
	sessions, err := h.s.Sessions(r.Context(), owner, cookie.Value)
	if err != nil {
		fail(w, 500, "sessions_unavailable")
		return
	}
	jsonResponse(w, 200, sessions)
}
