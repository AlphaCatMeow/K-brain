package imaging

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestImageOutputRejectsUnsafeDestinations(t *testing.T) {
	for _, raw := range []string{"http://example.com/a.png", "https://127.0.0.1/a.png", "https://[::1]/a.png", "https://169.254.169.254/latest", "https://10.0.0.1/a.png", "https://100.64.0.1/a.png", "https://user:password@example.com/a.png", "https://example.com:8443/a.png", "https://localhost/a.png", "https://service.internal/a.png", "https://example.com/a.png#fragment"} {
		t.Run(raw, func(t *testing.T) {
			u, err := url.Parse(raw)
			if err == nil && validImageOutputURL(u) {
				t.Fatal("unsafe output permitted")
			}
			if _, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: raw}); err == nil {
				t.Fatal("unsafe retrieval succeeded")
			}
		})
	}
}
func TestImageOutputPublicAddressPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "192.168.1.1", "172.16.0.1", "100.100.100.200", "168.63.129.16", "192.88.99.1", "198.18.0.1", "192.0.2.1", "2001:db8::1", "2002:a00:1::", "fc00::1", "fe80::1"} {
		if publicImageAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("nonpublic allowed: %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicImageAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("public denied: %s", raw)
		}
	}
}
func TestImageOutputTransportDoesNotInheritProxyOrCredentials(t *testing.T) {
	transport := imageOutputTransport()
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.DialContext == nil || !transport.DisableCompression || !transport.DisableKeepAlives {
		t.Fatal("unsafe transport defaults")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("loopback dial allowed")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "example.com:80"); err == nil {
		t.Fatal("non-TLS port allowed")
	}
}

type imageOutputTestTransport func(*http.Request) (*http.Response, error)

func (f imageOutputTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestImageOutputRetrievalValidatesBytesAndRedirects(t *testing.T) {
	raw := imageTestBytes(t, "png", 3, 2)
	for _, mode := range []string{"success", "private-redirect", "redirect-loop", "oversize", "invalid-bytes"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			tr := imageOutputTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
					t.Fatal("credentials or referrer transmitted")
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: r}
				switch mode {
				case "private-redirect":
					resp.StatusCode = 302
					resp.Header.Set("Location", "https://127.0.0.1/private")
				case "redirect-loop":
					resp.StatusCode = 302
					resp.Header.Set("Location", "https://example.com/next")
				case "oversize":
					resp.ContentLength = OutputImageLimits.MaxBytes + 1
				case "invalid-bytes":
					resp.Body = io.NopCloser(strings.NewReader("not an image"))
					resp.ContentLength = 12
				}
				return resp, nil
			})
			result, err := retrieveImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://example.com/result.png"}, tr)
			if mode == "success" {
				if err != nil || result.Width != 3 || result.Height != 2 || !bytes.Equal(result.Data, raw) {
					t.Fatalf("valid image not decoded: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or invalid result accepted")
			}
			if mode == "private-redirect" && calls != 1 {
				t.Fatalf("private redirect reached transport: %d", calls)
			}
			if mode == "redirect-loop" && calls != 4 {
				t.Fatalf("redirect budget wrong: %d", calls)
			}
		})
	}
}
