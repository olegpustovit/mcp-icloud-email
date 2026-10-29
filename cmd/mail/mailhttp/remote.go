package mailhttp

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/internal/publichttp"
	"golang.org/x/net/html"
)

func remoteURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.User != nil || u.Hostname() == "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Port() != "" && (u.Scheme != "https" || u.Port() != "443") && (u.Scheme != "http" || u.Port() != "80")) {
		return nil, errors.New("invalid remote URL")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publichttp.PublicAddress(ip) {
		return nil, errors.New("private image address")
	}
	return u, nil
}

// Every connection resolves and pins a public address; redirects cannot bypass the dial check.
func imageProxyClient() *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	transport.DialContext = publichttp.DialContext
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		if len(via) > 3 {
			return errors.New("too many image redirects")
		}
		_, err := remoteURL(req.URL.String())
		return err
	}}
}
func (h *handler) remoteImage(w http.ResponseWriter, r *http.Request) {
	var inputValue struct {
		URL string `json:"url"`
	}
	if !input(w, r, &inputValue) {
		return
	}
	u, err := remoteURL(inputValue.URL)
	if err != nil {
		respond(w, 400, map[string]string{"error": "image_url_not_allowed"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", u.String(), nil)
	if err != nil {
		respond(w, 400, map[string]string{"error": "invalid_image_url"})
		return
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp")
	req.Header.Set("User-Agent", "TerminalMailImageProxy/1")
	client := h.remoteClient
	if client == nil {
		client = imageProxyClient()
	}
	response, err := client.Do(req)
	if err != nil {
		respond(w, 502, map[string]string{"error": "remote_image_unavailable"})
		return
	}
	defer func() { _ = response.Body.Close() }()
	const limit = 10 << 20
	if response.StatusCode != 200 || response.ContentLength > limit {
		respond(w, 502, map[string]string{"error": "remote_image_unavailable"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(data) > limit {
		respond(w, 502, map[string]string{"error": "remote_image_too_large"})
		return
	}
	kind := http.DetectContentType(data)
	switch kind {
	case "image/png", "image/jpeg", "image/gif":
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || int64(config.Width)*int64(config.Height) > 40_000_000 {
			respond(w, 502, map[string]string{"error": "remote_image_invalid"})
			return
		}
	case "image/webp":
		width, height := webPDimensions(data)
		if width <= 0 || height <= 0 || int64(width)*int64(height) > 40_000_000 {
			respond(w, 502, map[string]string{"error": "remote_image_invalid"})
			return
		}
	default:
		respond(w, 415, map[string]string{"error": "remote_content_is_not_an_image"})
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

type remoteImage struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func stripParameters(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	changed := false
	for key := range q {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_eid" || lower == "mkt_tok" {
			q.Del(key)
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}
func messageContent(source string, strip bool, inline map[string][]byte) (string, []remoteImage, int) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", nil, 0
	}
	images := []remoteImage{}
	stripped := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			walk(child)
			child = next
		}
		if node.Type != html.ElementNode {
			return
		}
		if node.Data == "a" && strip {
			for i, a := range node.Attr {
				if a.Key == "href" {
					node.Attr[i].Val = stripParameters(a.Val)
				}
			}
		}
		if node.Data != "img" {
			return
		}
		src := ""
		tiny := false
		for _, a := range node.Attr {
			if a.Key == "src" {
				src = a.Val
			}
			if (a.Key == "width" || a.Key == "height") && (a.Val == "1" || a.Val == "2" || a.Val == "0") {
				tiny = true
			}
		}
		if strip && tiny {
			if node.Parent != nil {
				node.Parent.RemoveChild(node)
			}
			stripped++
			return
		}
		attrs := node.Attr[:0]
		for _, a := range node.Attr {
			if a.Key != "src" && a.Key != "srcset" && a.Key != "data-remote-id" {
				attrs = append(attrs, a)
			}
		}
		node.Attr = attrs
		if strings.HasPrefix(strings.ToLower(src), "cid:") {
			id, _ := url.PathUnescape(src[4:])
			data := inline[strings.Trim(id, "<>")]
			kind := http.DetectContentType(data)
			if len(data) <= 10<<20 && kind == "image/webp" {
				width, height := webPDimensions(data)
				if width > 0 && height > 0 && int64(width)*int64(height) <= 40_000_000 {
					node.Attr = append(node.Attr, html.Attribute{Key: "src", Val: "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)})
				}
				return
			}
			if len(data) <= 10<<20 && (kind == "image/png" || kind == "image/jpeg" || kind == "image/gif") {
				config, _, err := image.DecodeConfig(bytes.NewReader(data))
				if err == nil && int64(config.Width)*int64(config.Height) <= 40_000_000 {
					node.Attr = append(node.Attr, html.Attribute{Key: "src", Val: "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)})
				}
			}
			return
		}
		if strings.HasPrefix(src, "data:image/png;") || strings.HasPrefix(src, "data:image/jpeg;") || strings.HasPrefix(src, "data:image/gif;") || strings.HasPrefix(src, "data:image/webp;") {
			node.Attr = append(node.Attr, html.Attribute{Key: "src", Val: src})
			return
		}
		if _, err := remoteURL(src); err != nil {
			return
		}
		if strip {
			src = stripParameters(src)
		}
		id := fmt.Sprint(len(images))
		images = append(images, remoteImage{ID: id, URL: src})
		node.Attr = append(node.Attr, html.Attribute{Key: "data-remote-id", Val: id})
	}
	walk(doc)
	var out bytes.Buffer
	var body *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if body != nil {
		for n := body.FirstChild; n != nil; n = n.NextSibling {
			_ = html.Render(&out, n)
		}
	}
	return out.String(), images, stripped
}

// WebP's three container variants put canvas dimensions before the compressed pixels.
func webPDimensions(data []byte) (int, int) {
	if len(data) < 25 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(data[12:16]) {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0
		}
		width := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
		height := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
		return width + 1, height + 1
	case "VP8L":
		if data[20] != 0x2f {
			return 0, 0
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1
	case "VP8 ":
		if len(data) < 30 || !bytes.Equal(data[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
	}
	return 0, 0
}
