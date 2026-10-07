package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheKeyHashPreservesIdentity(t *testing.T) {
	prefix := strings.Repeat("x", 64)
	a, b := clampCacheKey(prefix+"first"), clampCacheKey(prefix+"second")
	if a == b || len(a) != 64 || a != clampCacheKey(prefix+"first") {
		t.Fatal("long cache keys collide or are unstable")
	}
	if clampCacheKey("session-1") != "session-1" || len(clampCacheKey(strings.Repeat("长", 40))) > 64 {
		t.Fatal("cache key byte bound violated")
	}
}

func TestCacheAffinityFormatsAndRequestOverride(t *testing.T) {
	for _, format := range []string{"none", "openai", "openai-nosession", "openrouter", ""} {
		t.Run(format, func(t *testing.T) {
			var body map[string]any
			var headers http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				_ = json.NewDecoder(r.Body).Decode(&body)
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			}))
			defer server.Close()
			client := New(server.URL, "secret")
			client.SetCacheKey("parent")
			client.SetCacheOptions(CacheOptions{SessionAffinity: true, AffinityFormat: format})
			_, _, err := client.Complete(t.Context(), Request{Model: "fixture", PromptCacheKey: "child", Messages: []Message{{Role: "user", Content: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			if body["prompt_cache_key"] != "child" {
				t.Fatal("request key lost")
			}
			for name, want := range map[string]bool{"Session_id": format == "openai", "X-Session-Id": format == "openrouter", "X-Client-Request-Id": format == "openai" || format == "openai-nosession", "X-Session-Affinity": format == "openai" || format == "openai-nosession"} {
				if (headers.Get(name) == "child") != want {
					t.Fatalf("format=%s header=%s value=%s", format, name, headers.Get(name))
				}
			}
		})
	}
}

func TestCacheHeadersPreserveExplicitRoutingAndDisabledPolicy(t *testing.T) {
	c := New("https://api.openai.com/v1", "secret")
	c.SetCacheKey("session")
	c.SetCacheOptions(CacheOptions{SessionAffinity: true})
	r, _ := http.NewRequest("POST", c.BaseURL, nil)
	r.Header.Set("x-client-request-id", "unique-request")
	c.applyCacheHeaders(r)
	if r.Header.Get("x-client-request-id") != "unique-request" || r.Header.Get("session_id") != "session" {
		t.Fatal("explicit routing header overwritten")
	}
	c.SetCacheOptions(CacheOptions{Retention: "none", AffinityFormat: "openai"})
	r, _ = http.NewRequest("POST", c.BaseURL, nil)
	c.applyCacheHeaders(r)
	if len(r.Header) != 0 {
		t.Fatal("disabled policy added affinity headers")
	}
	req := Request{PromptCacheKey: "stale", PromptCacheRetention: "24h"}
	c.applyCache(&req)
	if req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		t.Fatal("disabled policy retained cache hints")
	}
}

func TestAnthropicLongCacheAndThinkingBoundary(t *testing.T) {
	for _, retention := range []string{"short", "long", "none"} {
		payload, err := anthropicPayload(Request{Model: "fixture", PromptCacheRetention: retention,
			Messages: []Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "question"}, {Role: "assistant", Replay: &ProviderReplay{API: APIMessages, Blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private","signature":"s"}`)}}}},
			Tools:    []Tool{NewTool("read", "read", `{"type":"object"}`)}}, false)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(payload)
		want := 3
		if retention == "none" {
			want = 0
		}
		if strings.Count(string(body), `"cache_control"`) != want {
			t.Fatalf("boundary missing: %s", body)
		}
		if strings.Count(string(body), `"ttl":"1h"`) != map[bool]int{true: 3, false: 0}[retention == "long"] {
			t.Fatalf("wrong TTL: %s", body)
		}
	}
	blocks := []any{map[string]any{"type": "text", "text": "answer"}, map[string]any{"type": "thinking", "thinking": "private"}}
	if !markCacheBlock(blocks, cacheControl("short"), false) || blocks[1].(map[string]any)["cache_control"] != nil {
		t.Fatal("thinking boundary not skipped")
	}
}

func TestAnthropicCachePolicyOnWire(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	for _, supported := range []bool{false, true} {
		c := NewAnthropic(srv.URL, "fixture")
		c.SetCacheOptions(CacheOptions{Retention: "long", SupportsLong: supported})
		_, _, err := c.Complete(t.Context(), Request{Model: "fixture", Messages: []Message{{Role: "user", Content: "question"}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, `"ttl":"1h"`) != supported {
			t.Fatalf("long policy: %s", body)
		}
	}
}

func TestChatAnthropicCacheControlIsOptIn(t *testing.T) {
	for _, retention := range []string{"short", "long", "none"} {
		c := New("https://relay.test", "fixture")
		c.SetCacheOptions(CacheOptions{Retention: retention, ControlFormat: "anthropic", SupportsLong: true})
		req := Request{Messages: []Message{{Role: "system", Content: "stable"}, {Role: "user", Content: "question"}, {Role: "assistant", Content: ""}}, Tools: []Tool{NewTool("read", "read", `{"type":"object"}`)}}
		body, err := c.chatBody(req)
		if err != nil {
			t.Fatal(err)
		}
		want := 3
		if retention == "none" {
			want = 0
		}
		if strings.Count(string(body), `"cache_control"`) != want {
			t.Fatalf("chat markers: %s", body)
		}
		if strings.Count(string(body), `"ttl":"1h"`) != map[bool]int{true: 3, false: 0}[retention == "long"] {
			t.Fatalf("chat TTL: %s", body)
		}
		if req.Messages[0].Content != "stable" {
			t.Fatal("mutated source history")
		}
	}
}

func TestResponsesCacheCapabilitiesOverrideModelNames(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"status":"completed","output":[]}`)
	}))
	defer srv.Close()
	for _, enabled := range []bool{true, false} {
		c := NewResponses(srv.URL, "fixture")
		c.SetCacheKey("session")
		c.SetCacheOptions(CacheOptions{Retention: "long", SupportsLong: true, SupportsKey: &enabled, ResponsesCacheOptions: true})
		_, _, err := c.Complete(context.Background(), Request{Model: "grok-custom-name"})
		if err != nil {
			t.Fatal(err)
		}
		if (body["prompt_cache_key"] != nil) != enabled || body["prompt_cache_retention"] != nil {
			t.Fatalf("capabilities ignored: %v", body)
		}
		if body["prompt_cache_options"].(map[string]any)["ttl"] != "30m" {
			t.Fatal("missing explicit options")
		}
		c.SetCacheOptions(CacheOptions{Retention: "none", ResponsesCacheOptions: true})
		_, _, err = c.Complete(t.Context(), Request{Model: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if body["prompt_cache_key"] != nil || body["prompt_cache_options"].(map[string]any)["mode"] != "explicit" {
			t.Fatal("disabled options contract lost")
		}
	}
}

func TestStrictChatProxyReceivesNoUnsupportedCacheFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, field := range []string{"prompt_cache_key", "prompt_cache_retention", "cache_control", "prompt_cache_options"} {
			if strings.Contains(string(body), field) {
				t.Errorf("unexpected %s: %s", field, body)
			}
		}
		for _, header := range []string{"session_id", "x-session-id", "x-session-affinity", "x-client-request-id"} {
			if r.Header.Get(header) != "" {
				t.Errorf("unexpected affinity header %s", header)
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":97,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	no := false
	c := New(srv.URL, "fixture")
	c.SetCacheKey("session")
	c.SetCacheOptions(CacheOptions{Retention: "long", SupportsKey: &no, AffinityFormat: "none"})
	_, usage, err := c.Complete(t.Context(), Request{Model: "deepseek-chat", PromptCacheRetention: "24h", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil || usage.Cached() != 97 || usage.PromptTokens != 100 {
		t.Fatalf("usage lost: %+v %v", usage, err)
	}
}
