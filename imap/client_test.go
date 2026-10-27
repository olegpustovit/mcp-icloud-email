package imap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func TestParseUID(t *testing.T) {
	for _, id := range []string{"1", "4294967295"} {
		if _, err := parseUID(id); err != nil {
			t.Errorf("parseUID(%q): %v", id, err)
		}
	}
	for _, id := range []string{"", "0", "1junk", "-1", "4294967296"} {
		if _, err := parseUID(id); err == nil {
			t.Errorf("parseUID(%q) unexpectedly succeeded", id)
		}
	}
}

func TestReadBoundedBody(t *testing.T) {
	if _, err := readBoundedBody(strings.NewReader("small message")); err != nil {
		t.Fatalf("small message: %v", err)
	}
	if _, err := readBoundedBody(strings.NewReader(strings.Repeat("x", maxMessageSize+1))); err == nil {
		t.Fatal("expected oversized message error")
	}
}

func TestBodyReadUsesPeekAndByteLimit(t *testing.T) {
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, maxMessageSize + 1}}
	if got, want := string(section.FetchItem()), "BODY.PEEK[]<0.20971521>"; got != want {
		t.Fatalf("fetch item = %q, want %q", got, want)
	}
}

func TestParseEmailBodyRawMIMEFixtures(t *testing.T) {
	c := &Client{}
	plainRaw := []byte("From: sender@example.com\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello plain\r\n")
	plain := &Email{Subject: "plain subject"}
	if err := c.parseEmailBody(plain, bytes.NewReader(plainRaw)); err != nil {
		t.Fatalf("parse plain MIME: %v", err)
	}
	if plain.BodyPlain != "hello plain\r\n" {
		t.Fatalf("plain body = %q", plain.BodyPlain)
	}

	const boundary = "mix-boundary"
	multipartRaw := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n" +
		"--" + boundary + "\r\nContent-Type: multipart/alternative; boundary=alt-boundary\r\n\r\n" +
		"--alt-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain alternative\r\n" +
		"--alt-boundary\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>html alternative</b>\r\n" +
		"--alt-boundary--\r\n" +
		"--" + boundary + "\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"sample.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\nAQIDBA==\r\n" +
		"--" + boundary + "--\r\n")
	parsed := &Email{Subject: "multipart subject"}
	if err := c.parseEmailBody(parsed, bytes.NewReader(multipartRaw)); err != nil {
		t.Fatalf("parse multipart MIME: %v", err)
	}
	if parsed.BodyPlain != "plain alternative" || parsed.BodyHTML != "<b>html alternative</b>" {
		t.Fatalf("multipart bodies = plain %q, html %q", parsed.BodyPlain, parsed.BodyHTML)
	}
	if len(parsed.Attachments) != 1 || parsed.Attachments[0].Filename != "sample.bin" || parsed.Attachments[0].Size != 4 {
		t.Fatalf("attachments = %#v", parsed.Attachments)
	}
	attachment, err := findAttachment(multipartRaw, "sample.bin")
	if err != nil {
		t.Fatalf("find attachment: %v", err)
	}
	if string(attachment.Content) != "\x01\x02\x03\x04" || attachment.Size != 4 {
		t.Fatalf("attachment content=%v size=%d", attachment.Content, attachment.Size)
	}
}

func TestParseEmailBodyPropagatesMalformedMIME(t *testing.T) {
	if err := (&Client{}).parseEmailBody(&Email{}, strings.NewReader("Content-Type: multipart/mixed; boundary=missing\r\n\r\n")); err == nil {
		t.Fatal("expected malformed MIME error")
	}
}
