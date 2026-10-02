package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesNativeSearchPayloadAndSources(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_item.done

data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"w1","call_id":"w1","action":{"query":"latest","sources":[{"url":"https://example.test/a","title":"Example"}]}}}

event: response.completed

data: {"type":"response.completed","response":{"status":"completed","output":[]}}

`))
	}))
	defer srv.Close()
	c := NewResponses(srv.URL, "key")
	msg, _, err := c.Stream(context.Background(), Request{Model: "m", NativeWebSearch: true, Messages: []Message{{Role: "user", Content: "search"}}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if payload["include"] == nil || len(payload["tools"].([]any)) != 1 || payload["tools"].([]any)[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if len(msg.HostedSearch) != 1 || msg.HostedSearch[0].Sources[0].URL != "https://example.test/a" {
		t.Fatalf("missing sources: %#v", msg.HostedSearch)
	}
}

func TestNativeSearchSourceValidation(t *testing.T) {
	got := searchSources([]any{map[string]any{"url": "file:///secret"}, map[string]any{"url": "https://ok.test"}}, "source")
	if len(got) != 1 || got[0].URL != "https://ok.test" {
		t.Fatalf("got %#v", got)
	}
}
