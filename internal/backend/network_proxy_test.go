package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

func TestNetworkProxyEndpoint(t *testing.T) {
	defer netproxy.Set("direct")
	s := &Server{}
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		s.handleNetworkProxy(r, httptest.NewRequest(method, "/v1/network/proxy", strings.NewReader(body)))
		return r
	}
	if r := call(http.MethodPut, `{"proxy":"http://user:s3cret@127.0.0.1:7890"}`); r.Code != http.StatusOK || strings.Contains(r.Body.String(), "s3cret") {
		t.Fatalf("valid: %d %s", r.Code, r.Body.String())
	}
	if r := call(http.MethodPut, `{"proxy":"direct"}`); r.Code != http.StatusOK {
		t.Fatalf("direct: %d %s", r.Code, r.Body.String())
	}
	for _, body := range []string{`{"proxy":"ftp://user:s3cret@127.0.0.1:21"}`, `{"proxy":"http://user:s3cret@host"}`, `{}`, `not json`} {
		if r := call(http.MethodPut, body); r.Code != http.StatusBadRequest || strings.Contains(r.Body.String(), "s3cret") {
			t.Fatalf("invalid %s: %d %s", body, r.Code, r.Body.String())
		}
	}
	if r := call(http.MethodGet, ""); r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r.Code)
	}
}

func TestNetworkProxyEndpointRequiresAuthentication(t *testing.T) {
	defer netproxy.Set("direct")
	s := &Server{token: "proxy-test-token"}
	server := httptest.NewServer(s)
	defer server.Close()
	for _, token := range []string{"", "wrong-token", "proxy-test-token"} {
		req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/network/proxy", strings.NewReader(`{"proxy":"direct"}`))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := http.StatusUnauthorized
		if token == "proxy-test-token" {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Fatalf("status = %d, want %d", resp.StatusCode, want)
		}
	}
}
