package imap

import (
	"io"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestSyncTransportMetadataPeekAndIDLE(t *testing.T) {
	backend := imapmemserver.New()
	user := imapmemserver.NewUser("test", "test-password")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	backend.AddUser(user)
	server := imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return backend.NewSession(), nil, nil
	}, InsecureAuth: true, Caps: imapv2.CapSet{imapv2.CapIMAP4rev1: {}, imapv2.CapIdle: {}}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	wake := make(chan struct{}, 8)
	wire, err := imapclient.DialInsecure(listener.Addr().String(), syncOptions(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wire.Close() })
	if err = wire.Login("test", "test-password").Wait(); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(client *imapclient.Client, id string) {
		t.Helper()
		raw := "From: Mira <mira@example.com>\r\nTo: me@example.com\r\nSubject: Review notes\r\nMessage-ID: <" + id + ">\r\nReferences: <root@example.com> <parent@example.com>\r\nList-Id: Notes <notes.example.com>\r\nList-Unsubscribe: <https://notes.example.com/unsubscribe>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nA real message preview, not the subject.\r\n"
		command := client.Append("INBOX", int64(len(raw)), nil)
		if _, err := io.WriteString(command, raw); err != nil {
			t.Fatal(err)
		}
		if err := command.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(wire, "first@example.com")
	client := &SyncClient{wire: wire}
	folders, err := client.Folders()
	if err != nil || len(folders) != 1 || folders[0].Role != "inbox" {
		t.Fatalf("folders=%+v err=%v", folders, err)
	}
	status, err := client.Status("INBOX")
	if err != nil || status.Total != 1 || status.Unread != 1 || status.UIDValidity == 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	uids, err := client.UIDs("INBOX")
	if err != nil || len(uids) != 1 {
		t.Fatal("UID search failed", err)
	}
	mail, err := client.Fetch("INBOX", uids, 0, false)
	if err != nil || len(mail) != 1 {
		t.Fatal("metadata fetch failed", err)
	}
	if mail[0].Snippet != "A real message preview, not the subject." || mail[0].ListID != "Notes <notes.example.com>" || len(mail[0].References) != 2 || mail[0].BodyPlain != "" || !mail[0].Unread {
		t.Fatalf("incorrect metadata: %+v", mail[0])
	}
	status, err = client.Status("INBOX")
	if err != nil || status.Unread != 1 {
		t.Fatal("metadata fetch marked mail read")
	}
	stop, err := client.StartIdle()
	if err != nil {
		t.Fatal(err)
	}
	other, err := imapclient.DialInsecure(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.Login("test", "test-password").Wait(); err != nil {
		t.Fatal(err)
	}
	for len(wake) > 0 {
		<-wake
	}
	appendMessage(other, "second@example.com")
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("IDLE did not report new mail within two seconds")
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	status, err = client.Status("INBOX")
	if err != nil || status.Total != 2 {
		t.Fatal("IDLE restart lost new mail")
	}
}

func TestPartialMIMESnippet(t *testing.T) {
	for _, test := range []struct{ header, body, want string }{
		{"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", "SGVsbG8gd29ybGQ=", "Hello world"},
		{"Content-Type: text/html; charset=utf-8\r\n\r\n", "<style>hide-me</style><p>Hello <b>world</b></p>", "Hello world"},
		{"Content-Type: multipart/alternative; boundary=boundary\r\n\r\n", "--boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPartial preview without a closing MIME boundary", "Partial preview without a closing MIME boundary"},
	} {
		got := syncSnippet([]byte(test.header), []byte(test.body))
		if !strings.Contains(got, test.want) || strings.Contains(got, "hide-me") {
			t.Errorf("snippet=%q want %q", got, test.want)
		}
	}
}

func TestDisplayAddressKeepsUTF8NamesParseable(t *testing.T) {
	got := displayAddress(`🔳 Turing "Post"`, "news@example.com")
	if strings.Contains(got, "=?") {
		t.Fatalf("name was MIME-encoded: %s", got)
	}
	parsed, err := mail.ParseAddress(got)
	if err != nil || parsed.Name != `🔳 Turing "Post"` || parsed.Address != "news@example.com" {
		t.Fatalf("round trip failed: %q %v", parsed, err)
	}
}
