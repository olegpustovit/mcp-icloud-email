package auth

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	for _, v := range []struct {
		email string
		want  bool
	}{{"me@olegpustovit.com", true}, {"URSUS95@gmail.com", true}, {"@olegpustovit.com", false}, {"x@evil.olegpustovit.com", false}, {"x@olegpustovit.com.evil", false}, {"x@y@olegpustovit.com", false}, {"other@gmail.com", false}} {
		if Allowed(v.email) != v.want {
			t.Errorf("Allowed(%q)", v.email)
		}
	}
}
func TestMongoCredentialIsolationAndTokens(t *testing.T) {
	uri := os.Getenv("MAIL_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set MAIL_TEST_MONGODB_URI for Mongo integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, e := New(ctx, Config{BaseURL: "https://mail.example.com", MongoURI: uri, ResendAPIKey: "test", EmailFrom: "test@example.com", EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx)
	s.db = s.client.Database("mail_test_" + Random())
	defer s.db.Drop(ctx)
	if e = s.SaveAccount(ctx, "one@olegpustovit.com", "one@icloud.com", "password-secret"); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveAccount(ctx, "two@olegpustovit.com", "two@icloud.com", "other-secret"); e != nil {
		t.Fatal(e)
	}
	email, p, e := s.Credentials(ctx, "one@olegpustovit.com")
	if e != nil || email != "one@icloud.com" || p != "password-secret" {
		t.Fatalf("credentials mismatch %s %v", email, e)
	}
	a, e := s.Account(ctx, "one@olegpustovit.com")
	if e != nil {
		t.Fatal(e)
	}
	if string(a.Secret) == p {
		t.Fatal("plaintext stored")
	}
	if _, _, e = s.Credentials(ctx, "missing@olegpustovit.com"); e == nil {
		t.Fatal("missing account accepted")
	}
	a.Secret[len(a.Secret)-1] ^= 1
	if _, e = s.db.Collection("accounts").ReplaceOne(ctx, map[string]string{"_id": a.Email}, a); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Credentials(ctx, a.Email); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	token := Random()
	if e = s.Put(ctx, Record{ID: Hash(token), Kind: "code", Expires: time.Now().Add(time.Minute)}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, token, "code", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, token, "code", true); e != ErrUnauthorized {
		t.Fatal("replayed code accepted")
	}
	expired := Random()
	if e = s.Put(ctx, Record{ID: Hash(expired), Kind: "access", Expires: time.Now().Add(-time.Second)}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, expired, "access", false); e != ErrUnauthorized {
		t.Fatal("expired token accepted before TTL cleanup")
	}
}
func TestPasswordHash(t *testing.T) {
	hash, e := HashPassword("correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	if hash == "correct horse battery staple" || !CheckPassword(hash, "correct horse battery staple") || CheckPassword(hash, "wrong") {
		t.Fatal("password verification failed")
	}
	for _, bad := range []string{"", "a.b", "..."} {
		if CheckPassword(bad, "password") {
			t.Fatal("invalid hash accepted")
		}
	}
}
