// Package netproxy selects the outbound transport for provider HTTP traffic.
//
// Providers whose useSystemProxy toggle is on use the proxied transport, which
// resolves the proxy from the configuration LiveAgent hands over; all other
// providers use the direct transport (Proxy == nil, environment ignored).
// Loopback hosts are never proxied. Proxy URLs and credentials are never
// logged or included in errors.
package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http/httpproxy"
)

// EnvVar carries the LiveAgent app proxy into the backend process. Values:
// a proxy URL (http, https, socks5, socks5h), "direct" for app proxy disabled,
// or "invalid" for an enabled but unusable app proxy. When unset, the standard
// HTTPS_PROXY/HTTP_PROXY/NO_PROXY variables are used instead.
const EnvVar = "LIVEAGENT_KBRAIN_PROXY"

const (
	valueDirect  = "direct"
	valueInvalid = "invalid"
	noProxy      = "localhost,127.0.0.1,::1"
)

// ErrInvalidProxy fails proxied requests while the app proxy is enabled but
// misconfigured, instead of silently falling back to a direct connection.
var ErrInvalidProxy = errors.New("app proxy is enabled but its configuration is invalid")

type state struct {
	proxy func(*url.URL) (*url.URL, error)
}

var (
	current    atomic.Pointer[state]
	initOnce   sync.Once
	transports struct {
		once            sync.Once
		proxied, direct *http.Transport
	}
)

func load() *state {
	initOnce.Do(func() {
		if current.Load() == nil {
			v, ok := os.LookupEnv(EnvVar)
			if !ok {
				cfg := httpproxy.FromEnvironment()
				current.Store(&state{proxy: wrap(cfg.ProxyFunc())})
				return
			}
			st, _ := parse(v)
			current.Store(st)
		}
	})
	return current.Load()
}

// Set replaces the proxy used by proxied providers. It applies to subsequent
// requests without rebuilding clients. An unparseable value is stored as
// invalid (proxied requests fail) and reported to the caller.
func Set(value string) error {
	st, err := parse(value)
	// Settle the one-time environment load first so it cannot overwrite this.
	initOnce.Do(func() {})
	current.Store(st)
	return err
}

func parse(value string) (*state, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", valueDirect:
		return &state{proxy: func(*url.URL) (*url.URL, error) { return nil, nil }}, nil
	case valueInvalid:
		return &state{proxy: func(*url.URL) (*url.URL, error) { return nil, ErrInvalidProxy }}, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.Port() == "" {
		return invalid(), ErrInvalidProxy
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "socks5h":
		// net/http's SOCKS5 dialer already resolves the target remotely.
		u.Scheme = "socks5"
	default:
		return invalid(), ErrInvalidProxy
	}
	cfg := httpproxy.Config{HTTPProxy: u.String(), HTTPSProxy: u.String(), NoProxy: noProxy}
	return &state{proxy: wrap(cfg.ProxyFunc())}, nil
}

func invalid() *state {
	return &state{proxy: func(*url.URL) (*url.URL, error) { return nil, ErrInvalidProxy }}
}

func wrap(fn func(*url.URL) (*url.URL, error)) func(*url.URL) (*url.URL, error) {
	return func(u *url.URL) (*url.URL, error) {
		if isLoopback(u.Hostname()) {
			return nil, nil
		}
		return fn(u)
	}
}

func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func proxyForRequest(req *http.Request) (*url.URL, error) {
	if isLoopback(req.URL.Hostname()) {
		return nil, nil
	}
	return load().proxy(req.URL)
}

func build() {
	transports.once.Do(func() {
		base := http.DefaultTransport.(*http.Transport)
		transports.proxied = base.Clone()
		transports.proxied.Proxy = proxyForRequest
		transports.direct = base.Clone()
		transports.direct.Proxy = nil
	})
}

// Transport returns the cached transport for the provider toggle.
func Transport(useProxy bool) http.RoundTripper {
	build()
	if useProxy {
		return transports.proxied
	}
	return transports.direct
}

type ctxKey struct{}

// WithProvider marks ctx with the provider's proxy toggle for clients that
// serve several providers through one ContextTransport.
func WithProvider(ctx context.Context, useProxy bool) context.Context {
	return context.WithValue(ctx, ctxKey{}, useProxy)
}

// ContextTransport routes each request by the toggle stored with
// WithProvider; requests without a mark go direct.
func ContextTransport() http.RoundTripper { return contextTransport{} }

type contextTransport struct{}

func (contextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	useProxy, _ := req.Context().Value(ctxKey{}).(bool)
	return Transport(useProxy).RoundTrip(req)
}
