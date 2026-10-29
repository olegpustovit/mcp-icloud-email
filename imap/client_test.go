package imap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func TestRelatedImagePartsRetainCIDBytes(t *testing.T) {
	for _, disposition := range []string{"inline", "attachment; filename=logo.png"} {
		raw := "MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=related\r\n\r\n--related\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:logo@mail\">\r\n--related\r\nContent-Type: image/png\r\nContent-ID: <logo@mail>\r\nContent-Disposition: " + disposition + "\r\nContent-Transfer-Encoding: base64\r\n\r\naW1hZ2UtYnl0ZXM=\r\n--related--\r\n"
		var email Email
		if err := (&Client{}).parseEmailBody(&email, strings.NewReader(raw)); err != nil || string(email.InlineImages["logo@mail"]) != "image-bytes" || !strings.Contains(email.BodyHTML, "cid:logo@mail") {
			t.Fatalf("related part lost: %+v %v", email, err)
		}
	}
}

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

func TestFlagColorsRoundTrip(t *testing.T) {
	for _, color := range flagColors {
		keywords, err := flagKeywords("follow-up", color)
		if err != nil {
			t.Fatal(err)
		}
		flags := make([]string, len(keywords))
		for i, keyword := range keywords {
			flags[i] = keyword.(string)
		}
		if got := flagColor(flags); got != color {
			t.Fatalf("%s decoded as %s", color, got)
		}
	}
	if got := flagColor([]string{"$MailFlagBit2"}); got != "" {
		t.Fatalf("unflagged color: %s", got)
	}
	if _, err := flagKeywords("follow-up", "unknown"); err == nil {
		t.Fatal("accepted unknown color")
	}
}

func TestDraftHeaderInjectionRejected(t *testing.T) {
	for _, tc := range []struct {
		from, subject string
		to            []string
		opts          DraftOptions
	}{
		{"me@example.com", "subject\r\nBcc: victim@example.com", nil, DraftOptions{}},
		{"me@example.com", "subject", []string{"ok@example.com\r\nX: bad"}, DraftOptions{}},
		{"me@example.com", "subject", nil, DraftOptions{BCC: []string{"bad address"}}},
	} {
		if err := validateDraftHeaders(tc.from, tc.to, tc.subject, tc.opts); err == nil {
			t.Fatal("accepted unsafe draft headers")
		}
	}
	if err := validateDraftHeaders("Me <me@example.com>", []string{"you@example.com"}, "Привіт", DraftOptions{}); err != nil {
		t.Fatal(err)
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
