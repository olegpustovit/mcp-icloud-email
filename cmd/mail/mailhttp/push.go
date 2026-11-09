package mailhttp

import (
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
)

type pushRegistration struct {
	Subscription struct {
		webpush.Subscription
		ExpirationTime *uint64 `json:"expirationTime"`
	} `json:"subscription"`
	Name string `json:"name"`
	Zone string `json:"zone"`
}

func (h *handler) push(w http.ResponseWriter, r *http.Request) {
	if h.state == nil {
		respond(w, 503, map[string]string{"error": "push_unavailable"})
		return
	}
	owner := h.owner(r.Context())
	switch r.Method {
	case "GET":
		key, err := h.state.PushPublicKey(r.Context())
		if err != nil {
			failed(w, r, err)
			return
		}
		devices, err := h.state.PushDevices(r.Context(), owner)
		if err != nil {
			failed(w, r, err)
			return
		}
		respond(w, 200, map[string]any{"publicKey": key, "devices": devices})
	case "POST":
		var registration pushRegistration
		if !input(w, r, &registration) {
			return
		}
		cookie, err := r.Cookie("__Host-mail_session")
		if err != nil {
			respond(w, 401, map[string]string{"error": "login_required"})
			return
		}
		device, err := h.state.SavePushDevice(r.Context(), owner, auth.Hash(cookie.Value), mailsync.PushRegistration{Subscription: registration.Subscription.Subscription, Name: registration.Name, Zone: registration.Zone})
		if err != nil {
			respond(w, 400, map[string]string{"error": "device_could_not_be_registered"})
			return
		}
		respond(w, 201, device)
	case "DELETE":
		if err := h.state.RemovePushDevice(r.Context(), owner, r.PathValue("id")); err != nil {
			failed(w, r, err)
			return
		}
		w.WriteHeader(204)
	}
}
