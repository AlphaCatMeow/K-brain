package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserTokenPreflightAndUnauthorizedResponse(t *testing.T) {
	server := &Server{token: "backend-test-token"}
	origin := "http://localhost:1420"
	preflight := httptest.NewRequest(http.MethodOptions, "/v1/sessions", nil)
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPut)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, preflight)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("preflight = %d, headers = %v", response.Code, response.Header())
	}
	methods := response.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, http.MethodPut) {
		t.Fatalf("preflight methods = %q", methods)
	}
	for _, token := range []string{"", "wrong", "backend-test-token"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		request.Header.Set("Origin", origin)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		expected := http.StatusUnauthorized
		if token == "backend-test-token" {
			expected = http.StatusOK
		}
		if response.Code != expected || response.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("token %q response = %d, headers = %v", token, response.Code, response.Header())
		}
	}
}

func TestSettingsPUTRealPreflight(t *testing.T) {
	server := httptest.NewServer(&Server{token: "backend-token"})
	defer server.Close()
	req, err := http.NewRequest(http.MethodOptions, server.URL+"/v1/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:1420")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,cache-control")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:1420" || !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	for _, header := range []string{"authorization", "content-type", "cache-control"} {
		if !strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")), header) {
			t.Fatalf("missing allowed header %s", header)
		}
	}
}
