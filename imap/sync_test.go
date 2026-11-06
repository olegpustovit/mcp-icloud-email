package imap

import (
	"bufio"
	"io"
	"net"
	"net/mail"
	"strings"
	"sync/atomic"
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
	}, InsecureAuth: true, Caps: imapv2.CapSet{imapv2.CapIMAP4rev1: {}, imapv2.CapIdle: {}, imapv2.CapListStatus: {}}})
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
	folderStatuses, err := client.FolderStatuses()
	if err != nil || len(folderStatuses) != 1 || folderStatuses[0].Total != 1 || folderStatuses[0].UIDNext != status.UIDNext {
		t.Fatalf("LIST-STATUS=%+v err=%v", folderStatuses, err)
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

func TestSyncCommandDeadlineClosesWire(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := &SyncClient{wire: imapclient.New(clientConn, nil)}
	_, err := syncCommand(client, "blocked test", 20*time.Millisecond, func() (struct{}, error) {
		<-client.Closed()
		return struct{}{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want timeout", err)
	}
}

func TestVanishedUnilateralResponseWakesSync(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	vanished := make(chan struct {
		set     imapv2.UIDSet
		earlier bool
	}, 2)
	wire := imapclient.New(clientConn, &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Vanished: func(set imapv2.UIDSet, earlier bool) {
			vanished <- struct {
				set     imapv2.UIDSet
				earlier bool
			}{set, earlier}
		},
	}})
	t.Cleanup(func() { _ = wire.Close(); _ = serverConn.Close() })
	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		if _, err := io.WriteString(serverConn, "* OK ready\r\n"); err != nil {
			serverErr <- err
			return
		}
		command, _ := reader.ReadString('\n')
		if fields := strings.Fields(command); len(fields) > 0 {
			_, err := io.WriteString(serverConn, "* CAPABILITY IMAP4rev1\r\n"+fields[0]+" OK capability complete\r\n* VANISHED (EARLIER) 5:7\r\n* VANISHED 8:9\r\n")
			if err != nil {
				serverErr <- err
			}
		} else {
			serverErr <- io.ErrUnexpectedEOF
		}
	}()
	select {
	case result := <-vanished:
		if !result.earlier || !result.set.Contains(5) || !result.set.Contains(7) {
			t.Fatalf("unexpected VANISHED response: %+v", result)
		}
	case <-time.After(time.Second):
		select {
		case err := <-serverErr:
			t.Fatalf("server exchange failed: %v", err)
		case <-wire.Closed():
			t.Fatal("client closed before VANISHED callback")
		default:
			t.Fatal("VANISHED response was not handled")
		}
	}
	select {
	case result := <-vanished:
		if result.earlier || result.set.String() != "8:9" {
			t.Fatalf("unexpected current VANISHED response: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("current VANISHED response was not handled")
	}
}

func TestVanishedDuringStatusAndSelect(t *testing.T) {
	for _, command := range []string{"STATUS", "SELECT"} {
		t.Run(command, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer serverConn.Close()
			vanished := make(chan struct {
				set     imapv2.UIDSet
				earlier bool
			}, 1)
			wire := imapclient.New(clientConn, &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
				Vanished: func(set imapv2.UIDSet, earlier bool) {
					vanished <- struct {
						set     imapv2.UIDSet
						earlier bool
					}{set, earlier}
				},
			}})
			t.Cleanup(func() { _ = wire.Close() })
			serverErr := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(serverConn)
				if _, err := io.WriteString(serverConn, "* OK ready\r\n"); err != nil {
					serverErr <- err
					return
				}
				line, err := reader.ReadString('\n')
				if err != nil {
					serverErr <- err
					return
				}
				fields := strings.Fields(line)
				if len(fields) < 3 {
					serverErr <- io.ErrUnexpectedEOF
					return
				}
				tag := fields[0]
				if command == "STATUS" {
					_, err = io.WriteString(serverConn, "* VANISHED (EARLIER) 5:7\r\n* STATUS INBOX (MESSAGES 3 UIDNEXT 4)\r\n"+tag+" OK status complete\r\n")
				} else {
					_, err = io.WriteString(serverConn, "* FLAGS (\\Seen)\r\n* 3 EXISTS\r\n* OK [UIDVALIDITY 1] valid\r\n* OK [UIDNEXT 8] next\r\n* VANISHED (EARLIER) 5:7\r\n"+tag+" OK [READ-ONLY] selected\r\n")
				}
				serverErr <- err
			}()

			if command == "STATUS" {
				_, err := wire.Status("INBOX", &imapv2.StatusOptions{NumMessages: true}).Wait()
				if err != nil {
					t.Fatal(err)
				}
			} else if _, err := wire.Select("INBOX", &imapv2.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-vanished:
				if !result.earlier || !result.set.Contains(5) || !result.set.Contains(7) {
					t.Fatalf("unexpected VANISHED response: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("unsolicited VANISHED was not handled")
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSyncOptionsWakeOnVanished(t *testing.T) {
	wake := make(chan struct{}, 1)
	handler := syncOptions(func() { wake <- struct{}{} }).UnilateralDataHandler
	handler.Vanished(imapv2.UIDSet{}, false)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("VANISHED did not wake folder reconciliation")
	}
}

func TestSyncOptionsQueueVanishedForSelectedFolder(t *testing.T) {
	events := make(chan SyncEvent, 1)
	selected := &atomic.Value{}
	selected.Store("Archive")
	options := syncOptionsWithEvents(nil, events, selected)
	var uids imapv2.UIDSet
	uids.AddRange(5, 7)
	options.UnilateralDataHandler.Vanished(uids, true)
	select {
	case event := <-events:
		if event.Kind != "vanished" || event.Folder != "Archive" || !event.Earlier || !event.HasUID(6) || event.HasUID(8) {
			t.Fatalf("incorrect VANISHED event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("VANISHED event was not queued")
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

func TestSyncHeadersMarkMixedMessagesAsHavingAttachments(t *testing.T) {
	for raw, want := range map[string]bool{"Content-Type: multipart/mixed; boundary=x\r\n\r\n": true, "Content-Type: multipart/alternative; boundary=x\r\n\r\n": false, "Content-Type: text/plain\r\n\r\n": false} {
		var email Email
		readSyncHeaders(&email, []byte(raw))
		if email.HasAttachments != want {
			t.Errorf("%q: HasAttachments=%v", raw, email.HasAttachments)
		}
	}
}
