package imapclient_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestSelectEncodesQResyncCheckpoint(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()
	defer serverConn.Close()
	go client.Select("INBOX", &imap.SelectOptions{ReadOnly: true, QResync: &imap.SelectOptionsQResync{UIDValidity: 7, ModSeq: 42}})
	line, err := bufio.NewReader(serverConn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "EXAMINE INBOX (QRESYNC (7 42))") {
		t.Fatalf("SELECT command = %q", line)
	}
}
