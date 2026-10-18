package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
