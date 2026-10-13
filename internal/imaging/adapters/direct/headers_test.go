package direct

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestContractCustomHeadersInherited(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			raw := syntheticPNG(t, 16, 30, false)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Tenant-ID") != "synthetic-tenant" || r.Header.Get("OpenAI-Organization") != "synthetic-org" || r.Header.Get("OpenAI-Project") != "synthetic-project" {
					t.Error("custom headers missing or mutated")
				}
				if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
					t.Error("content headers overwritten")
				}
				if id == GoogleID {
					if r.Header.Get("X-Goog-Api-Key") != "synthetic-key" || r.Header.Get("Authorization") != "" {
						t.Error("Google auth overwritten")
					}
				} else if r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("bearer overwritten")
				}
				io.WriteString(w, responseJSON(id, raw))
			}))
			defer srv.Close()
			o := options(srv.Client(), srv.URL)
			o.CustomHeaders = map[string]string{"x-tenant-id": "synthetic-tenant", "OpenAI-Organization": "synthetic-org", "OpenAI-Project": "synthetic-project"}
			a := adapter(t, id, o)
			o.CustomHeaders["x-tenant-id"] = "later mutation"
			o.CustomHeaders["Authorization"] = "SECRET_SENTINEL"
			for i := 0; i < 2; i++ {
				if _, err := a.Submit(context.Background(), request(id)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestContractCustomHeadersRejected(t *testing.T) {
	names := []string{"Authorization", "authorization", "Proxy-Authorization", "Host", "Content-Length", "Content-Type", "Accept", "Cookie", "Connection", "Transfer-Encoding", "Idempotency-Key", "X-Idempotency-Key", "x-goog-api-key", "X-Api-Key", "Api-Key", "X-Auth-Token", "X-Authorization", "X-Forwarded-Host", "X-Forwarded-For", "X-Original-URL", "X-HTTP-Method-Override", "X-Proxy-Key", "X-Content-Length", "X-Secret", "Bad Header", "X-Bad\r\nHost"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			o := options(&http.Client{}, "https://example.invalid")
			o.CustomHeaders = map[string]string{name: "SECRET_SENTINEL"}
			_, err := NewOpenAI(o)
			assertError(t, imaging.RemoteResult{}, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		})
	}
	for _, headers := range []map[string]string{
		{"X-Tenant": "line\r\nAuthorization: SECRET_SENTINEL"},
		{"X-Tenant": "nul\x00SECRET_SENTINEL"},
		{"X-Tenant": "\tSECRET_SENTINEL"},
		{"X-Tenant": strings.Repeat("x", 4097)},
		{"X-Tenant": "one", "x-tenant": "two"},
	} {
		o := options(&http.Client{}, "https://example.invalid")
		o.CustomHeaders = headers
		_, err := NewGoogle(o)
		assertError(t, imaging.RemoteResult{}, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
}
