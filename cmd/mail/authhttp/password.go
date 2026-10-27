package authhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
)

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Next     string `json:"next,omitempty"`
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func passwordValid(p string) bool    { return len(p) >= 12 && len(p) <= 128 }
func safeNext(next string) string {
	if strings.HasPrefix(next, "/oauth/authorize?") {
		return next
	}
	return "/"
}
func (h *Handler) newSession(w http.ResponseWriter, r *http.Request, email string) error {
	session := auth.Random()
	if e := h.s.Put(r.Context(), auth.Record{ID: auth.Hash(session), Kind: "session", Email: email, Expires: time.Now().Add(7 * 24 * time.Hour)}); e != nil {
		return e
	}
	h.cookie(w, "__Host-mail_session", session, 7*86400)
	return nil
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	var in loginInput
	if decode(w, r, &in) != nil {
		fail(w, 400, "invalid_json")
		return
	}
	in.Email = normalizeEmail(in.Email)
	if !passwordValid(in.Password) || !auth.Allowed(in.Email) {
		fail(w, 401, "invalid_credentials")
		return
	}
	u, e := h.s.User(r.Context(), in.Email)
	if e != nil || !auth.CheckPassword(u.Password, in.Password) {
		fail(w, 401, "invalid_credentials")
		return
	}
	if e = h.newSession(w, r, in.Email); e != nil {
		fail(w, 500, "request_failed")
		return
	}
	jsonResponse(w, 200, map[string]string{"email": in.Email, "next": safeNext(in.Next)})
}
func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	var in loginInput
	if decode(w, r, &in) != nil {
		fail(w, 400, "invalid_json")
		return
	}
	in.Email = normalizeEmail(in.Email)
	if !auth.Allowed(in.Email) {
		fail(w, 400, "email_not_allowed")
		return
	}
	if !passwordValid(in.Password) {
		fail(w, 400, "password_must_be_12_to_128_characters")
		return
	}
	_, e := h.s.User(r.Context(), in.Email)
	if e == nil {
		jsonResponse(w, 202, map[string]string{"message": "Check your email to verify your account."})
		return
	}
	if !errors.Is(e, auth.ErrUnauthorized) {
		fail(w, 500, "request_failed")
		return
	}
	hash, e := auth.HashPassword(in.Password)
	if e != nil {
		fail(w, 500, "request_failed")
		return
	}
	token := auth.Random()
	if e = h.s.Put(r.Context(), auth.Record{ID: auth.Hash(token), Kind: "verify", Email: in.Email, Data: hash, Redirect: safeNext(in.Next), Expires: time.Now().Add(time.Hour)}); e != nil {
		fail(w, 500, "request_failed")
		return
	}
	if e = h.sendEmail(r.Context(), in.Email, "Verify your iCloud Mail account", "Verify your email address", h.s.Config.BaseURL+"/auth/verify?token="+url.QueryEscape(token)); e != nil {
		_ = h.s.Delete(r.Context(), token)
		fail(w, 502, "email_unavailable")
		return
	}
	jsonResponse(w, 202, map[string]string{"message": "Check your email to verify your account."})
}
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	record, e := h.s.Get(r.Context(), r.URL.Query().Get("token"), "verify", true)
	if e != nil || !auth.Allowed(record.Email) {
		http.Redirect(w, r, "/?error=verification", http.StatusSeeOther)
		return
	}
	if e = h.s.CreateUser(r.Context(), record.Email, record.Data); e != nil {
		http.Redirect(w, r, "/?error=verification", http.StatusSeeOther)
		return
	}
	if e = h.newSession(w, r, record.Email); e != nil {
		http.Redirect(w, r, "/?error=verification", http.StatusSeeOther)
		return
	}
	next := safeNext(record.Redirect)
	if next == "/" {
		next = "/?verified=1"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (h *Handler) forgot(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "invalid_json")
		return
	}
	in.Email = normalizeEmail(in.Email)
	if auth.Allowed(in.Email) {
		_, e := h.s.User(r.Context(), in.Email)
		if e == nil {
			token := auth.Random()
			if e = h.s.Put(r.Context(), auth.Record{ID: auth.Hash(token), Kind: "reset", Email: in.Email, Expires: time.Now().Add(30 * time.Minute)}); e != nil {
				fail(w, 500, "request_failed")
				return
			}
			if e = h.sendEmail(r.Context(), in.Email, "Reset your iCloud Mail password", "Choose a new password", h.s.Config.BaseURL+"/?reset_token="+url.QueryEscape(token)); e != nil {
				_ = h.s.Delete(r.Context(), token)
				fail(w, 502, "email_unavailable")
				return
			}
		}
	}
	jsonResponse(w, 202, map[string]string{"message": "If an account exists, a password reset link is on its way."})
}
func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	if !h.origin(r) {
		fail(w, 403, "invalid_origin")
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "invalid_json")
		return
	}
	if !passwordValid(in.Password) {
		fail(w, 400, "password_must_be_12_to_128_characters")
		return
	}
	record, e := h.s.Get(r.Context(), in.Token, "reset", true)
	if e != nil || !auth.Allowed(record.Email) {
		fail(w, 400, "invalid_or_expired_reset_token")
		return
	}
	hash, e := auth.HashPassword(in.Password)
	if e != nil {
		fail(w, 500, "request_failed")
		return
	}
	if e = h.s.ResetPassword(r.Context(), record.Email, hash); e != nil {
		fail(w, 500, "request_failed")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) sendEmail(ctx context.Context, to, subject, label, link string) error {
	body, e := json.Marshal(struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}{h.s.Config.EmailFrom, []string{to}, subject, "<p>" + html.EscapeString(label) + "</p><p><a href=\"" + html.EscapeString(link) + "\">Continue to iCloud Mail</a></p><p>If you did not request this, ignore this email.</p>"})
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, e := http.NewRequestWithContext(ctx, "POST", "https://api.resend.com/emails", bytes.NewReader(body))
	if e != nil {
		return e
	}
	request.Header.Set("Authorization", "Bearer "+h.s.Config.ResendAPIKey)
	request.Header.Set("Content-Type", "application/json")
	resp, e := h.emailClient.Do(request)
	if e != nil {
		return e
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("email delivery failed")
	}
	return nil
}
