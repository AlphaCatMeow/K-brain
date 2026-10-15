package imaging

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Public image output retrieval never inherits provider credentials, cookies,
// proxy settings or the provider HTTP client. Every connection resolves and
// pins a validated public address, including connections following redirects.
func publicImageAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "168.63.129.16/32", "192.88.99.0/24", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	return true
}

func validImageOutputURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(u.String()) > 8192 {
		return false
	}
	if u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicImageAddress(ip)
	}
	return !strings.ContainsAny(host, "%\\\r\n")
}

func resolvePublicImageAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, &ImageError{Code: CodeUnavailable}
	}
	for _, ip := range ips {
		if !publicImageAddress(ip) {
			return nil, &ImageError{Code: CodeForbidden}
		}
	}
	return ips, nil
}

func imageOutputTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	return &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives: true, DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != strconv.Itoa(443) {
				return nil, &ImageError{Code: CodeForbidden}
			}
			ips, err := resolvePublicImageAddresses(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				if ctx.Err() != nil {
					break
				}
			}
			return nil, &ImageError{Code: CodeUnavailable}
		},
	}
}

func downloadImageOutput(ctx context.Context, source OutputSource) (ImageContent, error) {
	u, err := url.Parse(source.URL)
	if err != nil || !validImageOutputURL(u) {
		return ImageContent{}, &ImageError{Code: CodeForbidden}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	transport := imageOutputTransport()
	defer transport.CloseIdleConnections()
	return retrieveImageOutput(ctx, source, transport)
}

// The internal transport seam permits deterministic tests; production callers
// always supply imageOutputTransport, never a provider or browser client.
func retrieveImageOutput(ctx context.Context, source OutputSource, transport http.RoundTripper) (ImageContent, error) {
	u, err := url.Parse(source.URL)
	if err != nil || !validImageOutputURL(u) {
		return ImageContent{}, &ImageError{Code: CodeForbidden}
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !validImageOutputURL(req.URL) {
			return &ImageError{Code: CodeForbidden}
		}
		req.Header = make(http.Header)
		req.Header.Set("Accept", "image/png,image/jpeg,image/webp")
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ImageContent{}, &ImageError{Code: CodeForbidden}
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp")
	resp, err := client.Do(req)
	if err != nil {
		return ImageContent{}, &ImageError{Code: CodeUnavailable}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ImageContent{}, &ImageError{Code: CodeUnavailable}
	}
	if resp.ContentLength > OutputImageLimits.MaxBytes {
		return ImageContent{}, &ImageError{Code: CodeTooLarge}
	}
	// DecodeImageContent applies byte and pixel limits and validates actual bytes.
	mime := source.MIME
	if mime == "" {
		mime = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		return ImageContent{}, &ImageError{Code: CodeInvalidInput}
	}
	return DecodeImageContent(ctx, io.LimitReader(resp.Body, OutputImageLimits.MaxBytes+1), mime, OutputImageLimits)
}
