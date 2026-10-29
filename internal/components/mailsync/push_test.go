package mailsync

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func pushFixture(t *testing.T) PushRegistration {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return PushRegistration{Name: "Test browser", Zone: "Europe/Zagreb", Subscription: webpush.Subscription{Endpoint: "https://push.example.test/opaque-secret", Keys: webpush.Keys{Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}}}
}
func TestNotificationPreferencesAndValidation(t *testing.T) {
	p := defaultPreferences()
	p.Notifications = "vip"
	p.VIP = []string{"vip@example.com"}
	p.QuietHours = true
	m := Metadata{Email: imap.Email{Unread: true, From: "VIP <vip@example.com>"}}
	for _, tc := range []struct {
		clock string
		want  bool
	}{{"2026-10-28T21:59:00Z", true}, {"2026-10-28T22:00:00Z", false}, {"2026-10-29T07:29:00Z", false}, {"2026-10-29T07:30:00Z", true}} {
		now, _ := time.Parse(time.RFC3339, tc.clock)
		if got := wantsNotification(p, "me@icloud.com", m, now); got != tc.want {
			t.Fatalf("%s: got %v", tc.clock, got)
		}
	}
	p.QuietHours = false
	m.From = "other@example.com"
	m.To = []string{"Me <me@icloud.com>"}
	if !wantsNotification(p, "me@icloud.com", m, time.Now()) {
		t.Fatal("direct mail missing")
	}
	m.ListID = "newsletter"
	if wantsNotification(p, "me@icloud.com", m, time.Now()) {
		t.Fatal("newsletter treated as direct")
	}
	m.ListID = ""
	m.Unread = false
	if wantsNotification(p, "me@icloud.com", m, time.Now()) {
		t.Fatal("read mail notified")
	}
	registration := pushFixture(t)
	if err := validatePush(registration); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://example.com/a", "https://user:pass@example.com/a", "https://example.com:444/a", "https://example.com/a#fragment", "file:///a"} {
		bad := registration
		bad.Subscription.Endpoint = endpoint
		if validatePush(bad) == nil {
			t.Fatal("accepted endpoint", endpoint)
		}
	}
	bad := registration
	bad.Subscription.Keys.Auth = "AA"
	if validatePush(bad) == nil {
		t.Fatal("accepted invalid auth key")
	}
	bad = registration
	bad.Zone = "missing/timezone"
	if validatePush(bad) == nil {
		t.Fatal("accepted invalid timezone")
	}
	for _, clock := range []string{"-1:30", "22x00", "7:30", "24:00"} {
		p = defaultPreferences()
		p.QuietStart = clock
		if p.validate() == nil {
			t.Fatal("accepted clock", clock)
		}
	}
}

type pushTransport func(*http.Request) (*http.Response, error)

func (f pushTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPushPersistenceRetriesRevocationAndAccountIsolation(t *testing.T) {
	s := testMirror(t)
	s.cancel()
	s.wg.Wait()
	ctx := context.Background()
	validSession := true
	valid := func(context.Context, string, string) (bool, error) { return validSession, nil }
	s.ConfigurePush(valid, "https://mail.example.com")
	registration := pushFixture(t)
	d, err := s.SavePushDevice(ctx, "owner", "session", registration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Payload), "opaque-secret") {
		t.Fatal("subscription stored unencrypted")
	}
	encoded, _ := json.Marshal(d)
	if strings.Contains(string(encoded), "opaque-secret") || strings.Contains(string(encoded), "session") {
		t.Fatal("private device fields exposed", string(encoded))
	}
	public, err := s.PushPublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.PushPublicKey(ctx)
	if public != again {
		t.Fatal("VAPID keys changed")
	}
	p := defaultPreferences()
	p.Notifications = "all"
	if err = s.SavePreferences(ctx, "owner", p); err != nil {
		t.Fatal(err)
	}
	m := Metadata{Key: messageKey("owner", d.Account, "INBOX", 7, "42"), Owner: "owner", Account: d.Account, Email: imap.Email{ID: "42", UIDValidity: 7, Folder: "INBOX", From: "from@example.com", Subject: "private subject", Unread: true}}
	if err = s.commit(ctx, "owner", d.Account, "imap", "", []Change{{Entity: "msg", Op: "put", Key: m.Key, Message: &m, Arrival: true}}); err != nil {
		t.Fatal(err)
	}
	code, calls := 500, 0
	client := &http.Client{Transport: pushTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), m.Subject) || r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid ") {
			t.Fatal("unencrypted or unauthenticated push")
		}
		if r.Header.Get("Cookie") != "" {
			t.Fatal("browser cookie leaked")
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	run := func() {
		t.Helper()
		if err := s.pushDevice(ctx, d, valid, "https://mail.example.com", client); err != nil {
			t.Fatal(err)
		}
		if err := s.db.Collection("mail_push_devices").FindOne(ctx, bson.M{"_id": d.ID}).Decode(&d); err != nil {
			t.Fatal(err)
		}
	}
	original := d.Cursor
	run()
	if calls != 1 || d.Cursor != original || d.Error == "" {
		t.Fatal("failed push consumed arrival")
	}
	code = 201
	run()
	if calls != 2 || d.Cursor <= original || d.Error != "" {
		t.Fatal("retry did not acknowledge delivery")
	}
	run()
	if calls != 2 {
		t.Fatal("acknowledged arrival delivered twice")
	}
	if err = s.RemovePushDevice(ctx, "other", d.ID); err != nil {
		t.Fatal(err)
	}
	devices, _ := s.PushDevices(ctx, "owner")
	if len(devices) != 1 {
		t.Fatal("foreign owner deleted device")
	}
	validSession = false
	if err = s.pushDevice(ctx, d, valid, "https://mail.example.com", client); err != nil {
		t.Fatal(err)
	}
	devices, _ = s.PushDevices(ctx, "owner")
	if len(devices) != 0 {
		t.Fatal("revoked session retained notifications")
	}
	validSession = true
	d, err = s.SavePushDevice(ctx, "owner", "new-session", registration)
	if err != nil {
		t.Fatal(err)
	}
	s.credentials = func(context.Context, string) (string, string, error) {
		return "replacement@icloud.com", "password", nil
	}
	if err = s.pushDevice(ctx, d, valid, "https://mail.example.com", client); err != nil {
		t.Fatal(err)
	}
	var count int64
	count, err = s.db.Collection("mail_push_devices").CountDocuments(ctx, bson.M{"_id": d.ID})
	if err != nil || count != 0 {
		t.Fatal("old account retained notifications", err)
	}
}
func TestArrivalExcludesInitialImportAndMailboxReset(t *testing.T) {
	s := testMirror(t)
	ctx := context.Background()
	account := "mirror@icloud.com"
	wire := &syncFake{mail: map[uint32]imap.Email{1: {ID: "1", Unread: true}}, validity: 7, next: 2, modseq: 1}
	m := &monitor{ctx: ctx, owner: "owner", account: account, folders: map[string]Folder{}}
	if err := s.reconcileFolder(m, wire, "INBOX", false, "imap", ""); err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		n, err := s.db.Collection("mail_changelog").CountDocuments(ctx, bson.M{"arrival": true})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("initial mail became arrivals")
	}
	wire.mail[2] = imap.Email{ID: "2", Unread: true}
	wire.next = 3
	wire.modseq++
	if err := s.reconcileFolder(m, wire, "INBOX", false, "imap", ""); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("new arrival not marked")
	}
	wire.validity = 8
	wire.next = 4
	wire.mail = map[uint32]imap.Email{3: {ID: "3", Unread: true}}
	wire.modseq++
	if err := s.reconcileFolder(m, wire, "INBOX", false, "imap", ""); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("reset history became arrivals")
	}
}
