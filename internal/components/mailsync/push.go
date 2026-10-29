package mailsync

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/olegpustovit/mcp-icloud-email/internal/publichttp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type PushDevice struct {
	ID          string    `bson:"_id" json:"id"`
	Owner       string    `bson:"owner" json:"-"`
	Account     string    `bson:"account" json:"-"`
	Session     string    `bson:"session" json:"-"`
	Name        string    `bson:"name" json:"name"`
	Zone        string    `bson:"zone" json:"zone"`
	Payload     []byte    `bson:"payload" json:"-"`
	Cursor      int64     `bson:"cursor" json:"-"`
	NextAttempt time.Time `bson:"next_attempt" json:"-"`
	Error       string    `bson:"error" json:"error,omitempty"`
}
type PushRegistration struct {
	Subscription webpush.Subscription `json:"subscription"`
	Name         string               `json:"name"`
	Zone         string               `json:"zone"`
}
type pushKeys struct {
	ID      string `bson:"_id"`
	Public  string `bson:"public"`
	Private []byte `bson:"private"`
}

func (s *Service) ConfigurePush(validSession func(context.Context, string, string) (bool, error), subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushSession = validSession
	s.pushSubject = subject
	if s.pushHTTP == nil {
		s.pushHTTP = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: publichttp.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
}
func (s *Service) pushKeys(ctx context.Context) (pushKeys, error) {
	var keys pushKeys
	err := s.db.Collection("mail_push_keys").FindOne(ctx, bson.M{"_id": "vapid"}).Decode(&keys)
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return keys, err
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return keys, err
	}
	keys = pushKeys{ID: "vapid", Public: public, Private: s.seal([]byte(private))}
	_, err = s.db.Collection("mail_push_keys").InsertOne(ctx, keys)
	if mongo.IsDuplicateKeyError(err) {
		err = s.db.Collection("mail_push_keys").FindOne(ctx, bson.M{"_id": "vapid"}).Decode(&keys)
	}
	return keys, err
}
func (s *Service) PushPublicKey(ctx context.Context) (string, error) {
	keys, err := s.pushKeys(ctx)
	return keys.Public, err
}
func validatePush(p PushRegistration) error {
	u, err := url.Parse(p.Subscription.Endpoint)
	if err != nil || len(p.Subscription.Endpoint) > 8192 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return errors.New("invalid push endpoint")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publichttp.PublicAddress(ip) {
		return errors.New("private push endpoint")
	}
	auth, err := base64.RawURLEncoding.DecodeString(p.Subscription.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("invalid push authentication")
	}
	key, err := base64.RawURLEncoding.DecodeString(p.Subscription.Keys.P256dh)
	if err != nil {
		return errors.New("invalid push key")
	}
	if _, err = ecdh.P256().NewPublicKey(key); err != nil {
		return errors.New("invalid push key")
	}
	if len(p.Name) == 0 || len(p.Name) > 100 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return errors.New("invalid device name")
	}
	if len(p.Zone) > 100 {
		return errors.New("invalid time zone")
	}
	_, err = time.LoadLocation(p.Zone)
	return err
}
func (s *Service) SavePushDevice(ctx context.Context, owner, session string, p PushRegistration) (PushDevice, error) {
	var d PushDevice
	if err := validatePush(p); err != nil {
		return d, err
	}
	s.mu.Lock()
	valid := s.pushSession
	s.mu.Unlock()
	if valid == nil {
		return d, errors.New("push unavailable")
	}
	ok, err := valid(ctx, owner, session)
	if err != nil {
		return d, err
	}
	if !ok {
		return d, errors.New("session expired")
	}
	state := s.owner(owner)
	state.write.Lock()
	defer state.write.Unlock()
	account, err := s.Account(ctx, owner)
	if err != nil {
		return d, err
	}
	id := identity(p.Subscription.Endpoint)
	count, err := s.db.Collection("mail_push_devices").CountDocuments(ctx, bson.M{"owner": owner, "_id": bson.M{"$ne": id}})
	if err != nil {
		return d, err
	}
	if count >= 20 {
		return d, errors.New("too many devices")
	}
	cursor, err := s.Cursor(ctx, owner, account)
	if err != nil {
		return d, err
	}
	var existing PushDevice
	err = s.db.Collection("mail_push_devices").FindOne(ctx, bson.M{"_id": id, "owner": owner, "account": account}).Decode(&existing)
	if err == nil {
		cursor = existing.Cursor
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return d, err
	}
	raw, err := json.Marshal(p.Subscription)
	if err != nil {
		return d, err
	}
	d = PushDevice{ID: id, Owner: owner, Account: account, Session: session, Name: p.Name, Zone: p.Zone, Cursor: cursor, Payload: s.seal(raw), NextAttempt: time.Now().UTC()}
	// Possession of the subscription's endpoint and keys rebinds this browser after an account change.
	_, err = s.db.Collection("mail_push_devices").ReplaceOne(ctx, bson.M{"_id": id}, d, options.Replace().SetUpsert(true))
	return d, err
}
func (s *Service) PushDevices(ctx context.Context, owner string) ([]PushDevice, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Collection("mail_push_devices").Find(ctx, bson.M{"owner": owner, "account": account})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	devices := []PushDevice{}
	err = rows.All(ctx, &devices)
	return devices, err
}
func (s *Service) RemovePushDevice(ctx context.Context, owner, id string) error {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return err
	}
	_, err = s.db.Collection("mail_push_devices").DeleteOne(ctx, bson.M{"_id": id, "owner": owner, "account": account})
	return err
}
func quietNow(p Preferences, now time.Time) bool {
	if !p.QuietHours {
		return false
	}
	clock := now.Format("15:04")
	if p.QuietStart < p.QuietEnd {
		return clock >= p.QuietStart && clock < p.QuietEnd
	}
	return clock >= p.QuietStart || clock < p.QuietEnd
}
func wantsNotification(p Preferences, account string, m Metadata, now time.Time) bool {
	if !m.Unread || p.Notifications == "none" || quietNow(p, now) {
		return false
	}
	if p.Notifications == "all" {
		return true
	}
	contains := func(values []string, value string) bool {
		for _, candidate := range values {
			if strings.EqualFold(mailAddress(candidate), mailAddress(value)) {
				return true
			}
		}
		return false
	}
	if contains(p.VIP, m.From) {
		return true
	}
	if m.ListID != "" {
		return false
	}
	for _, to := range m.To {
		if strings.EqualFold(mailAddress(to), account) {
			return true
		}
		for _, identity := range p.Identities {
			if strings.EqualFold(mailAddress(to), identity.Address) {
				return true
			}
		}
	}
	return false
}
func mailAddress(value string) string {
	if address, err := mail.ParseAddress(value); err == nil {
		return address.Address
	}
	return strings.TrimSpace(value)
}
func cutText(value string, limit int) string {
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return value
}
func (s *Service) pushDevice(ctx context.Context, d PushDevice, valid func(context.Context, string, string) (bool, error), subject string, client *http.Client) error {
	ok, err := valid(ctx, d.Owner, d.Session)
	if err != nil {
		return err
	}
	account, err := s.Account(ctx, d.Owner)
	if err != nil {
		return err
	}
	if !ok || account != d.Account {
		_, err = s.db.Collection("mail_push_devices").DeleteOne(ctx, bson.M{"_id": d.ID, "owner": d.Owner, "session": d.Session})
		return err
	}
	p, err := s.Preferences(ctx, d.Owner)
	if err != nil {
		return err
	}
	zone, err := time.LoadLocation(d.Zone)
	if err != nil {
		return err
	}
	high, err := s.Cursor(ctx, d.Owner, d.Account)
	if err != nil {
		return err
	}
	rows, err := s.db.Collection("mail_changelog").Find(ctx, bson.M{"owner": d.Owner, "account": d.Account, "committed": true, "arrival": true, "seq": bson.M{"$gt": d.Cursor, "$lte": high}}, options.Find().SetSort(bson.D{{Key: "seq", Value: 1}}).SetLimit(30))
	if err != nil {
		return err
	}
	var changes []Change
	err = rows.All(ctx, &changes)
	_ = rows.Close(ctx)
	if err != nil {
		return err
	}
	next := time.Now().Add(2 * time.Second)
	failure := ""
	for _, change := range changes {
		now := time.Now()
		if change.Message != nil && now.Sub(change.At) < 5*time.Minute {
			var current Metadata
			err = s.db.Collection("mail_messages").FindOne(ctx, bson.M{"_id": change.Key, "owner": d.Owner, "account": d.Account}).Decode(&current)
			if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
				return err
			}
			if err == nil && wantsNotification(p, account, current, now.In(zone)) {
				keys, err := s.pushKeys(ctx)
				if err != nil {
					return err
				}
				private, err := s.open(keys.Private)
				if err != nil {
					return err
				}
				raw, err := s.open(d.Payload)
				if err != nil {
					return err
				}
				var sub webpush.Subscription
				if err = json.Unmarshal(raw, &sub); err != nil {
					return err
				}
				folders, err := s.Folders(ctx, d.Owner, d.Account)
				if err != nil {
					return err
				}
				var unread uint32
				for _, folder := range folders {
					if folder.Role == "inbox" || strings.EqualFold(folder.Name, "INBOX") {
						unread += folder.Unread
					}
				}
				payload, _ := json.Marshal(map[string]any{"title": cutText(current.From, 120), "body": cutText(current.Subject, 300), "tag": change.Key, "unread": unread, "data": map[string]any{"folder": current.Folder, "id": current.ID, "validity": current.UIDValidity, "account": d.Account, "owner": d.Owner}})
				response, sendErr := webpush.SendNotificationWithContext(ctx, payload, &sub, &webpush.Options{HTTPClient: client, Subscriber: subject, VAPIDPublicKey: keys.Public, VAPIDPrivateKey: string(private), TTL: 300, Topic: change.Key[:32], Urgency: webpush.UrgencyNormal})
				code := 0
				if response != nil {
					code = response.StatusCode
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
					_ = response.Body.Close()
				}
				if code == 404 || code == 410 {
					_, err = s.db.Collection("mail_push_devices").DeleteOne(ctx, bson.M{"_id": d.ID, "owner": d.Owner, "session": d.Session})
					return err
				}
				if sendErr != nil || code < 200 || code >= 300 {
					failure = "Push delivery failed; retrying."
					next = now.Add(time.Minute)
					break
				}
			}
		}
		d.Cursor = change.Sequence
	}
	if failure == "" && len(changes) < 30 {
		d.Cursor = high
	}
	_, err = s.db.Collection("mail_push_devices").UpdateOne(ctx, bson.M{"_id": d.ID, "owner": d.Owner, "session": d.Session}, bson.M{"$set": bson.M{"cursor": d.Cursor, "next_attempt": next, "error": failure}})
	return err
}
func (s *Service) runPush() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			valid, subject, client := s.pushSession, s.pushSubject, s.pushHTTP
			s.mu.Unlock()
			if valid == nil {
				continue
			}
			rows, err := s.db.Collection("mail_push_devices").Find(s.ctx, bson.M{"next_attempt": bson.M{"$lte": time.Now()}}, options.Find().SetSort(bson.D{{Key: "next_attempt", Value: 1}}).SetLimit(20))
			if err != nil {
				continue
			}
			var devices []PushDevice
			err = rows.All(s.ctx, &devices)
			_ = rows.Close(s.ctx)
			if err != nil {
				continue
			}
			for _, device := range devices {
				if err = s.pushDevice(s.ctx, device, valid, subject, client); err != nil {
					_, _ = s.db.Collection("mail_push_devices").UpdateOne(s.ctx, bson.M{"_id": device.ID, "session": device.Session}, bson.M{"$set": bson.M{"next_attempt": time.Now().Add(time.Minute), "error": "Push unavailable; retrying."}})
				}
			}
		}
	}
}
