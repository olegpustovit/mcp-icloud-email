package mailmessage

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
)

const MaxAttachments = 10 << 20

type Attachment struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mimeType"`
	Content  []byte `json:"content"`
}

func ValidateAttachments(files []Attachment) error {
	if len(files) > 30 {
		return errors.New("at most 30 attachments are allowed")
	}
	var size int
	for _, file := range files {
		size += len(file.Content)
		if file.Filename == "" || len(file.Filename) > 255 || strings.ContainsAny(file.Filename, "\r\n\x00/\\") || size > MaxAttachments {
			return errors.New("attachments must total at most 10 MB and have safe filenames")
		}
		if _, _, err := mime.ParseMediaType(file.MIMEType); err != nil {
			return errors.New("invalid attachment type")
		}
	}
	return nil
}

// Encode is shared by SMTP and Drafts so autosave preserves every MIME part.
func Encode(header mail.Header, body string, rich bool, files []Attachment) ([]byte, error) {
	if err := ValidateAttachments(files); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer, err := mail.CreateWriter(&buffer, header)
	if err != nil {
		return nil, err
	}
	inline, err := writer.CreateInline()
	if err != nil {
		return nil, err
	}
	parts := []struct{ kind, text string }{{"text/plain", body}}
	if rich {
		parts = []struct{ kind, text string }{{"text/plain", html.UnescapeString(bluemonday.StrictPolicy().Sanitize(strings.NewReplacer("</p>", "\n\n", "<br>", "\n", "<br/>", "\n").Replace(body)))}, {"text/html", body}}
	}
	for _, part := range parts {
		var h mail.InlineHeader
		h.SetContentType(part.kind, map[string]string{"charset": "utf-8"})
		out, err := inline.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(out, part.text); err != nil {
			return nil, err
		}
		if err = out.Close(); err != nil {
			return nil, err
		}
	}
	if err = inline.Close(); err != nil {
		return nil, err
	}
	for _, file := range files {
		var h mail.AttachmentHeader
		h.SetFilename(file.Filename)
		h.SetContentType(file.MIMEType, nil)
		out, err := writer.CreateAttachment(h)
		if err != nil {
			return nil, err
		}
		if _, err = out.Write(file.Content); err != nil {
			return nil, fmt.Errorf("write attachment: %w", err)
		}
		if err = out.Close(); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
