package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestAnthropicModelDiscoveryAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, authorization, key, version string
		headers                           map[string]string
	}{
		{"compatible gateway", "Bearer fixture-key", "fixture-key", "2023-06-01", nil},
		{"custom authorization", "Bearer custom-token", "fixture-key", "2023-06-01", map[string]string{"authorization": "Bearer custom-token"}},
		{"disabled bearer", "", "fixture-key", "2023-06-01", map[string]string{"Authorization": ""}},
		{"custom native headers", "Bearer fixture-key", "custom-key", "2023-06-01", map[string]string{"x-api-key": "custom-key"}},
		{"custom version", "Bearer fixture-key", "fixture-key", "2024-01-01", map[string]string{"anthropic-version": "2024-01-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
					t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != tc.authorization || r.Header.Get("X-Api-Key") != tc.key || r.Header.Get("Anthropic-Version") != tc.version {
					http.Error(w, "missing compatible authentication", http.StatusUnauthorized)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-fixture","type":"model","display_name":"Claude fixture"}]}`)
			}))
			defer srv.Close()
			client := &Anthropic{OpenAI: NewWithOptions(srv.URL+"/v1", "fixture-key", false, "", tc.headers)}
			models, err := client.Models(context.Background())
			if err != nil || len(models) != 1 || models[0].ID != "claude-fixture" {
				t.Fatalf("models=%+v error=%v", models, err)
			}
		})
	}
}

func TestAnthropicDefaultVersionedEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, base, messages, models string
		full                         bool
	}{
		{"root", "", "/v1/messages", "/v1/models", false},
		{"root slash", "/", "/v1/messages", "/v1/models", false},
		{"versioned", "/v1", "/v1/messages", "/v1/models", false},
		{"versioned slash", "/v1/", "/v1/messages", "/v1/models", false},
		{"messages address", "/v1/messages", "/v1/messages", "/v1/models", false},
		{"models address", "/v1/models", "/v1/messages", "/v1/models", false},
		{"prefix", "/tenant", "/tenant/v1/messages", "/tenant/v1/models", false},
		{"versioned prefix", "/tenant/v1", "/tenant/v1/messages", "/tenant/v1/models", false},
		{"escaped prefix", "/tenant%2Fname", "/tenant%2Fname/v1/messages", "/tenant%2Fname/v1/models", false},
		{"custom version", "/tenant/v2", "/tenant/v2/messages", "/tenant/v2/models", false},
		{"full URL", "/custom/generate?region=cn", "/custom/generate?region=cn", "/custom/v1/models", true},
	} {
		for _, constructor := range []string{"factory", "direct"} {
			if constructor == "direct" && tc.full {
				continue
			}
			t.Run(tc.name+"/"+constructor, func(t *testing.T) {
				var paths []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.Method+" "+r.URL.RequestURI())
					if r.Header.Get("X-Api-Key") != "fixture-key" || r.Header.Get("Authorization") != "Bearer fixture-key" {
						http.Error(w, "missing credentials", 401)
						return
					}
					switch {
					case r.Method == http.MethodGet && r.URL.RequestURI() == tc.models:
						_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-fixture"}]}`)
					case r.Method == http.MethodPost && r.URL.RequestURI() == tc.messages:
						contentResponse(w, "anthropic", r.Header.Get("Accept") == "text/event-stream")
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				var client Client
				if constructor == "direct" {
					client = NewAnthropic(srv.URL+tc.base, "fixture-key")
				} else {
					var err error
					client, err = NewClient(ClientOptions{API: APIMessages, BaseURL: srv.URL + tc.base, APIKey: "fixture-key", IsFullURL: tc.full, MaxRetries: 1})
					if err != nil {
						t.Fatal(err)
					}
				}
				client = client.Clone()
				models, err := client.Models(t.Context())
				if err != nil || len(models) != 1 || models[0].ID != "claude-fixture" {
					t.Fatalf("models=%+v err=%v paths=%v", models, err, paths)
				}
				for _, stream := range []bool{false, true} {
					text, _, err := retryRequest(t.Context(), client, stream)
					if err != nil || text != "ok" {
						t.Fatalf("stream=%v text=%q err=%v paths=%v", stream, text, err, paths)
					}
				}
				want := []string{"GET " + tc.models, "POST " + tc.messages, "POST " + tc.messages}
				if !slices.Equal(paths, want) {
					t.Fatalf("paths=%v want=%v", paths, want)
				}
			})
		}
	}
}

func TestAnthropicModelDiscoveryNativeAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "fixture-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			http.Error(w, "missing native credentials", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-native"}]}`)
	}))
	defer srv.Close()
	models, err := NewAnthropic(srv.URL+"/v1", "fixture-key").Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "claude-native" {
		t.Fatalf("models=%+v error=%v", models, err)
	}
}
