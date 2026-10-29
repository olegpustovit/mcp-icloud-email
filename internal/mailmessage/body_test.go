package mailmessage

import (
	"bytes"
	"io"
	"testing"

	"github.com/emersion/go-message/mail"
)

func TestRichMessageAndBinaryAttachmentRoundTrip(t *testing.T) {
	var h mail.Header
	h.SetSubject("Привіт")
	file := Attachment{Filename: "résumé.bin", MIMEType: "application/octet-stream", Content: []byte{0, 1, 255, 13, 10, 0}}
	raw, err := Encode(h, "<p>Hello &amp; <b>world</b></p>", true, []Attachment{file})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string][]byte{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(part.Body)
		if err != nil {
			t.Fatal(err)
		}
		switch header := part.Header.(type) {
		case *mail.InlineHeader:
			kind, _, _ := header.ContentType()
			seen[kind] = content
		case *mail.AttachmentHeader:
			name, _ := header.Filename()
			seen[name] = content
		}
	}
	if !bytes.Contains(seen["text/plain"], []byte("Hello & world")) || !bytes.Contains(seen["text/html"], []byte("<b>world</b>")) || !bytes.Equal(seen[file.Filename], file.Content) {
		t.Fatalf("MIME parts lost: %v", seen)
	}
}

func TestAttachmentBounds(t *testing.T) {
	for _, file := range []Attachment{{Filename: "../escape", MIMEType: "text/plain"}, {Filename: "safe", MIMEType: "bad type"}, {Filename: "safe", MIMEType: "text/plain", Content: make([]byte, MaxAttachments+1)}} {
		if ValidateAttachments([]Attachment{file}) == nil {
			t.Fatal("unsafe attachment accepted")
		}
	}
}
