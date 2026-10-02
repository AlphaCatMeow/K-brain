package backend

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestLegacyHistoryImportAllowsEmptyAndDetectsContentTampering(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	input := legacyHistoryImportRequest{SourceID: "empty-draft", ConversationID: "empty-draft", SourceFingerprint: "source-v1", Title: "Draft", Model: protocol.ModelRef{Provider: "p", Model: "m"}, SourceMetadata: json.RawMessage(`{"segments":[]}`)}
	var response map[string]any
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &response).StatusCode; got != http.StatusOK || response["status"] != "imported" {
		t.Fatalf("empty import = %d %#v", got, response)
	}
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &response).StatusCode; got != http.StatusOK || response["status"] != "already_imported" {
		t.Fatalf("empty retry = %d %#v", got, response)
	}
	input.Messages = []protocol.Message{{ID: "new", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "tampered"}}}}
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, nil).StatusCode; got != http.StatusConflict {
		t.Fatalf("content tamper = %d", got)
	}
}

func TestLegacyHistoryImportPreservesInputOrderingAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	_, server := newTestServer(t, store, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
	created := legacyHistoryImportRequest{SourceID: "ordered", ConversationID: "ordered", SourceFingerprint: "ordered-v1", Title: "Ordered", Model: protocol.ModelRef{Provider: "p", Model: "m"}, Messages: []protocol.Message{{ID: "z", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "z"}}}, {ID: "a", Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "a"}}}}}
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", created, nil).StatusCode; got != http.StatusOK {
		t.Fatalf("ordered import = %d", got)
	}
	server.Close()
	_ = store.Close()
	reopened, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, restarted := newTestServer(t, reopened, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
	var retry map[string]any
	if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", created, &retry).StatusCode; got != http.StatusOK || retry["status"] != "already_imported" {
		t.Fatalf("restart retry = %d %+v", got, retry)
	}
	created.SourceMetadata = json.RawMessage(`{"tampered":true}`)
	var conflict map[string]any
	if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", created, &conflict).StatusCode; got != http.StatusConflict {
		t.Fatalf("metadata tampering = %d", got)
	}
	messages := reopened.RawMessages("ordered")
	if len(messages) != 2 || messages[0].ID != "z" || messages[1].ID != "a" {
		t.Fatalf("ordering after restart = %+v", messages)
	}
}

func TestLegacyHistoryImportActiveContextBoundary(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	input := legacyHistoryImportRequest{SourceID: "segmented", ConversationID: "segmented", SourceFingerprint: "v1", Model: protocol.ModelRef{Model: "m"}, ActiveContext: &legacyActiveContext{Cutoff: 1, Summary: "latest cumulative summary"}, Messages: []protocol.Message{
		{ID: "sealed", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "old segment"}}},
		{ID: "active", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "new segment"}}},
	}}
	var response map[string]any
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &response).StatusCode; got != http.StatusOK {
		t.Fatalf("import = %d %+v", got, response)
	}
	raw := store.RawMessages("segmented")
	if len(raw) != 2 || raw[0].ID != "sealed" || raw[1].ID != "active" {
		t.Fatalf("raw history = %+v", raw)
	}
	_, active, err := store.Load("segmented")
	if err != nil || len(active) != 2 || active[0].Role != "system" || active[1].ID != "active" {
		t.Fatalf("context = %+v %v", active, err)
	}
	input.ActiveContext.Cutoff = 0
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &response).StatusCode; got != http.StatusConflict {
		t.Fatalf("boundary tamper = %d", got)
	}
}
