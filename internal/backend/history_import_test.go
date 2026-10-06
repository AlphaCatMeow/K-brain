package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestLegacyHistoryImportRealHTTPIsIdempotentAndPreservesCanonicalHistory(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{response: "unused"}
	_, server := newTestServer(t, store, t.TempDir(), client)
	created, updated := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC()
	input := legacyHistoryImportRequest{
		SourceID: "legacy-conversation-1", SourceFingerprint: "fixture-fingerprint", ConversationID: "legacy-conversation-1",
		Title: "Imported title", CWD: t.TempDir(), Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"},
		CreatedAt: created, UpdatedAt: updated, Pinned: true, Shared: true, ShareToken: "fixture-share", ShareRedactTool: true,
		SourceMetadata: json.RawMessage(`{"checkpoint":"unresolved","segment_ids":["seg-0","seg-1"]}`),
		Messages: []protocol.Message{
			{ID: "user-1", Role: protocol.RoleUser, CreatedAt: &created, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "hello"}}},
			{ID: "assistant-1", Role: protocol.RoleAssistant, Provider: "fixture", Model: "fixture-model", CreatedAt: &updated, ToolCalls: []protocol.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "answer"}}},
			{ID: "tool-1", Role: protocol.RoleTool, ToolCallID: "call-1", Name: "lookup", CreatedAt: &updated, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "result"}}},
		},
	}
	var first map[string]any
	response := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &first)
	if response.StatusCode != http.StatusOK || first["status"] != "imported" {
		t.Fatalf("first import = %d %#v", response.StatusCode, first)
	}
	var second map[string]any
	response = postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &second)
	if response.StatusCode != http.StatusOK || second["status"] != "already_imported" {
		t.Fatalf("retry = %d %#v", response.StatusCode, second)
	}
	input.Title = "changed source"
	input.SourceFingerprint = "changed-fingerprint"
	response = postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d", response.StatusCode)
	}
	meta, messages, err := store.Load("legacy-conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != input.ConversationID || meta.Title != "Imported title" || !meta.Pinned || !meta.Shared || len(messages) != 3 {
		t.Fatalf("metadata/messages not preserved: %+v %d", meta, len(messages))
	}
	if messages[0].ID != "user-1" || messages[1].ID != "assistant-1" || messages[2].ID != "tool-1" || messages[1].ToolCalls[0].ID != "call-1" || messages[2].ToolCallID != "call-1" {
		t.Fatalf("message/tool identities = %+v", messages)
	}
	if meta.ImportMetadata == "" {
		t.Fatal("source metadata was not retained")
	}
	if _, err := os.Stat(filepath.Join(store.SessionsDir(), input.ConversationID, "session.jsonl")); err != nil {
		t.Fatal(err)
	}
	status := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+input.ConversationID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("delete status: %d", status)
	}
	response = postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, nil)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("deleted import status: %d", response.StatusCode)
	}
}
