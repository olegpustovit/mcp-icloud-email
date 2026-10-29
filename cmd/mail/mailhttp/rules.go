package mailhttp

import (
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
	"net/http"
)

func (h *handler) rules(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "rules_unavailable"})
		return
	}
	owner := h.owner(r.Context())
	switch {
	case r.Method == "GET":
		status, err := h.state.RulesStatus(r.Context(), owner)
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, status)
	case r.PathValue("id") != "":
		if err := h.state.RetryRuleJob(r.Context(), owner, r.PathValue("id")); err != nil {
			failed(w, r, err)
			return
		}
		w.WriteHeader(204)
	case r.URL.Path == "/api/mail/rules/preview":
		var body struct {
			Rule mailsync.Rule `json:"rule"`
		}
		if !input(w, r, &body) {
			return
		}
		preview, err := h.state.PreviewRule(r.Context(), owner, body.Rule)
		if err != nil {
			respond(w, 400, map[string]string{"error": "invalid_rule"})
			return
		}
		respond(w, 200, preview)
	default:
		var body struct {
			Digest string `json:"digest"`
		}
		if !input(w, r, &body) {
			return
		}
		job, err := h.state.ApplyRules(r.Context(), owner, body.Digest)
		if err != nil {
			respond(w, 409, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 202, job)
	}
}
