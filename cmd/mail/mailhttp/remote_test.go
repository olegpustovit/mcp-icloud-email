package mailhttp

import (
	"bytes"
	"encoding/base64"
	"github.com/microcosm-cc/bluemonday"
	"github.com/olegpustovit/mcp-icloud-email/internal/publichttp"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestEmbeddedCIDImagesStayLocalAndRejectActiveContent(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aDXkAAAAASUVORK5CYII=")
	policy := bluemonday.UGCPolicy()
	policy.AllowURLSchemes("cid")
	source := policy.Sanitize(`<img src="cid:logo%40mail" onerror="bad()"><img src="cid:active"><img src="cid:absent">`)
	content, remote, _ := messageContent(source, false, map[string][]byte{"logo@mail": data, "active": []byte(`<svg onload="bad()"/>`)})
	if len(remote) != 0 || !strings.Contains(content, "data:image/png;base64,") || strings.Contains(content, "onerror") || strings.Contains(content, "svg") || strings.Contains(content, "cid:") {
		t.Fatal("CID content was lost or active content survived", content, remote)
	}
}

func TestProxyRejectsInternalNetworksAndUnsafeURLs(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.2.3.4", "0.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "198.19.0.1", "224.0.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2001:db8::1", "2002:7f00:1::", "3fff::1"} {
		if publichttp.PublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publichttp.PublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("blocked %s", ip)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "data:image/png,secret", "ftp://example.com/a", "https://user:secret@example.com/a", "https://example.com:8080/a", "http://127.0.0.1/a", "https://[::ffff:127.0.0.1]/a", "https://example.com/#fragment"} {
		if _, err := remoteURL(raw); err == nil {
			t.Errorf("allowed %s", raw)
		}
	}
	client := imageProxyClient()
	if _, err := client.Get("http://localhost"); err == nil {
		t.Fatal("DNS resolution to loopback was accepted")
	}
	for _, raw := range []string{"http://169.254.169.254/", "https://example.com:99/", "http://user:password@example.com/"} {
		req, _ := http.NewRequest("GET", raw, nil)
		if err := client.CheckRedirect(req, []*http.Request{{}}); err == nil {
			t.Errorf("unsafe redirect %s", raw)
		}
	}
}

type remoteRoundTrip func(*http.Request) (*http.Response, error)

func (f remoteRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProxyImagesAreBoundedAndNeverForwardCredentials(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aDXkAAAAASUVORK5CYII=")
	for _, tc := range []struct {
		name   string
		data   []byte
		length int64
		status int
	}{
		{"png", png, int64(len(png)), 200}, {"html", []byte("<html><script>bad()</script>"), 27, 415}, {"oversized header", png, 11 << 20, 502}, {"oversized stream", bytes.Repeat([]byte{'x'}, (10<<20)+1), -1, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{remoteClient: &http.Client{Transport: remoteRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" || r.Header.Get("X-Mail-Owner") != "" {
					t.Fatal("credentials leaked to sender")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tc.data)), ContentLength: tc.length}, nil
			})}}
			req := httptest.NewRequest("POST", "/api/mail/remote-image", strings.NewReader(`{"url":"https://images.example.com/image"}`))
			req.Header.Set("Cookie", "session=secret")
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("X-Mail-Owner", "alice")
			w := httptest.NewRecorder()
			h.remoteImage(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Code == 200 && (w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), png)) {
				t.Fatal("image changed")
			}
		})
	}
}
func TestMessageContentBlocksRemotePixelsAndStripsOnlyTrackingParameters(t *testing.T) {
	source := `<p>Hello</p><img src="https://images.example/banner.png?utm_source=mail&amp;token=keep"><img src="https://tracker.example/pixel" width="1" height="1"><a href="https://example.com/path?utm_campaign=mail&amp;fbclid=id&amp;q=keep">Visit</a><img srcset="https://other.example/a 2x" src="https://other.example/a"><img src="https://127.0.0.1/secret">`
	content, images, stripped := messageContent(source, true, nil)
	if stripped != 1 || len(images) != 2 || images[0].URL != "https://images.example/banner.png?token=keep" {
		t.Fatalf("wrong content accounting: %d %+v", stripped, images)
	}
	if strings.Contains(content, "src=\"https:") || strings.Contains(content, "srcset") || strings.Contains(content, "fbclid") || strings.Contains(content, "utm_") || !strings.Contains(content, `href="https://example.com/path?q=keep"`) || !strings.Contains(content, `data-remote-id="0"`) {
		t.Fatal(content)
	}
	_, images, stripped = messageContent(source, false, nil)
	if stripped != 0 || len(images) != 3 || !strings.Contains(images[0].URL, "utm_source") {
		t.Fatal("disabled tracking control was ignored")
	}
}

func TestWebPCanvasBounds(t *testing.T) {
	cases := []struct {
		tag    string
		header []byte
		w, h   int
	}{
		{"VP8X", []byte{0, 0, 0, 0, 99, 0, 0, 49, 0, 0}, 100, 50},
		{"VP8L", []byte{0x2f, 0x63, 0x40, 0x0c, 0}, 100, 50},
		{"VP8 ", []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 100, 0, 50, 0}, 100, 50},
	}
	for _, tc := range cases {
		data := append([]byte("RIFF\x00\x00\x00\x00WEBP"+tc.tag+"\x00\x00\x00\x00"), tc.header...)
		w, h := webPDimensions(data)
		if w != tc.w || h != tc.h {
			t.Fatalf("%s: %dx%d", tc.tag, w, h)
		}
	}
	for _, data := range [][]byte{nil, []byte("RIFF"), bytes.Repeat([]byte{0xff}, 30)} {
		if w, h := webPDimensions(data); w != 0 || h != 0 {
			t.Fatal("accepted invalid header")
		}
	}
}
