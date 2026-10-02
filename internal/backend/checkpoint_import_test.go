package backend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func legacyCheckpointFixture(t *testing.T) legacyHistoryImportRequest {
	t.Helper()
	blob, data, note := "0123456789abcdef@v1", base64.StdEncoding.EncodeToString([]byte{0, 255, 1}), "ledger note"
	mode := uint32(0100644)
	root := t.TempDir()
	return legacyHistoryImportRequest{SourceID: "native", ConversationID: "native", SourceFingerprint: "fixture-v2", Model: protocol.ModelRef{Model: "m"}, Messages: []protocol.Message{
		{Role: protocol.RoleSystem, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "system"}}},
		{ID: "u1", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "first"}}},
		{ID: "u2", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "second"}}},
	}, Checkpoint: &legacyCheckpointImport{Status: "available", NativePath: "~/.liveagent/checkpoints/native", IndexPath: "~/.liveagent/checkpoints/native/index.jsonl", IndexJSONL: "original ledger bytes\n", Records: []legacyCheckpointImportRecord{
		{Schema: 2, TurnSeq: 7, TurnID: "u1", Kind: "turn", CapturedAt: 10},
		{Schema: 2, TurnSeq: 8, TurnID: "u2", Kind: "file", Root: root, RelPath: "binary", ExistedBefore: true, Blob: &blob, BlobBase64: &data, Size: 3, MtimeMs: 11, CapturedAt: 12, Mode: &mode, Note: &note},
		{Schema: 2, TurnSeq: 8, TurnID: "u2", Kind: "file", Root: root, RelPath: "created", CapturedAt: 13},
	}}}
}

func TestLegacyCheckpointImportHTTPPreservesLedgerMapsTurnsAndRetries(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events := filepath.Join(root, "events")
	_, server := newTestServer(t, store, events, &scriptedClient{response: "unused"})
	in := legacyCheckpointFixture(t)
	for i := 0; i < 2; i++ {
		var result map[string]any
		response := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", in, &result)
		if response.StatusCode != 200 || result["checkpoint"] != "available" {
			t.Fatalf("import %d: %d %+v", i, response.StatusCode, result)
		}
		if i == 1 && result["status"] != "already_imported" {
			t.Fatalf("retry: %+v", result)
		}
	}
	response, err := http.Get(server.URL + "/v1/sessions/native/checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var turns []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&turns); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0]["turn_seq"] != float64(1) || turns[1]["turn_seq"] != float64(2) {
		t.Fatalf("mapped turns: %+v", turns)
	}
	var preview checkpointDiff
	response = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/native/checkpoints/1/preview", checkpointRootsRequest{AuthorizedRoots: []string{in.Checkpoint.Records[1].Root}}, &preview)
	if response.StatusCode != 200 || preview.RestoreFiles != 1 || len(preview.Entries) != 2 {
		t.Fatalf("cumulative preview: %d %+v", response.StatusCode, preview)
	}
	metadataBytes, err := os.ReadFile(importedCheckpointMetadataPath(events, in.ConversationID, in.SourceFingerprint))
	if err != nil {
		t.Fatal(err)
	}
	var metadata legacyCheckpointMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.IndexJSONL != in.Checkpoint.IndexJSONL || *metadata.Records[1].Mode != 0100644 || metadata.Records[1].MtimeMs != 11 || *metadata.Records[1].Note != "ledger note" || *metadata.Records[1].BlobBase64 != "AP8B" {
		t.Fatalf("ledger lost: %+v", metadata)
	}
	refs := store.Snapshots("native")
	if len(refs) != 2 {
		t.Fatalf("snapshot mapping: %+v", refs)
	}
	expected := make([]checkpointExpected, 0, len(preview.Entries))
	for _, entry := range preview.Entries {
		expected = append(expected, checkpointExpected{Key: entry.Key, CurrentHash: entry.CurrentHash})
	}
	var rewound checkpointRewindResult
	response = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/native/checkpoints/1/rewind", checkpointRewindRequest{AuthorizedRoots: []string{in.Checkpoint.Records[1].Root}, Expected: expected}, &rewound)
	if response.StatusCode != 200 || rewound.RestoredFiles != 1 {
		t.Fatalf("imported checkpoint rewind: %d %+v", response.StatusCode, rewound)
	}
	restored, err := os.ReadFile(filepath.Join(in.Checkpoint.Records[1].Root, "binary"))
	if err != nil || string(restored) != string([]byte{0, 255, 1}) {
		t.Fatalf("binary preimage not restored: %v %v", restored, err)
	}
	if len(store.RawMessages("native")) != 3 {
		t.Fatal("file rewind changed imported conversation history")
	}
	server.Close()
	store.Close()
	reopened, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, restarted := newTestServer(t, reopened, events, &scriptedClient{response: "unused"})
	var retry map[string]any
	if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", in, &retry).StatusCode; got != 200 {
		t.Fatalf("restart: %d %+v", got, retry)
	}
	if len(reopened.Snapshots("native")) != 0 {
		t.Fatal("retry resurrected consumed snapshots")
	}
	in.Checkpoint.Records[1].MtimeMs++
	if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", in, nil).StatusCode; got != 409 {
		t.Fatalf("ledger tamper status: %d", got)
	}
}

func TestLegacyCheckpointImportHTTPValidationPrecedesHistory(t *testing.T) {
	for name, mutate := range map[string]func(*legacyCheckpointImportRecord){
		"schema":         func(r *legacyCheckpointImportRecord) { r.Schema = 1 },
		"traversal":      func(r *legacyCheckpointImportRecord) { r.RelPath = "../escape" },
		"blob traversal": func(r *legacyCheckpointImportRecord) { s := "../escape"; r.Blob = &s },
		"blob size":      func(r *legacyCheckpointImportRecord) { r.Size++ },
		"blob encoding":  func(r *legacyCheckpointImportRecord) { s := "!"; r.BlobBase64 = &s },
		"identity":       func(r *legacyCheckpointImportRecord) { r.TurnID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
			in := legacyCheckpointFixture(t)
			mutate(&in.Checkpoint.Records[1])
			var result map[string]any
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", in, &result).StatusCode; got != 400 {
				t.Fatalf("invalid import: %d %+v", got, result)
			}
			if _, _, err := store.Load("native"); err == nil {
				t.Fatal("invalid checkpoint created history")
			}
		})
	}
}

func TestLegacyCheckpointImportHTTPPruningPartialAndPersistenceFailure(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events := t.TempDir()
	_, server := newTestServer(t, store, events, &scriptedClient{response: "unused"})
	in := legacyCheckpointFixture(t)
	in.Checkpoint.Records = append(in.Checkpoint.Records, legacyCheckpointImportRecord{Schema: 2, Kind: "rewind", TurnSeq: 8}, legacyCheckpointImportRecord{Schema: 2, Kind: "error", TurnSeq: 9, TurnID: "unmapped", CapturedAt: 20})
	blocker := filepath.Join(events, "checkpoints")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", in, &result).StatusCode; got != 500 {
		t.Fatalf("persistence failure: %d %+v", got, result)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", in, &result).StatusCode; got != 200 || result["checkpoint"] != "partial" || result["status"] != "already_imported" {
		t.Fatalf("retry: %d %+v", got, result)
	}
	files, _ := filepath.Glob(filepath.Join(events, "checkpoints", "native", "cp-*.json"))
	if len(files) != 1 {
		t.Fatalf("pruned/unmapped turns installed: %v", files)
	}
	if _, err := os.Stat(importedCheckpointPath(events, "native", in.SourceFingerprint, 8)); !os.IsNotExist(err) {
		t.Fatal("rewound turn imported")
	}
	t.Log(fmt.Sprintf("partial reason: %v", result["checkpoint_reason"]))
}

func TestLegacyCheckpointImportHTTPEmptyBlobAndMissingBlob(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
			in := legacyCheckpointFixture(t)
			empty := ""
			in.Checkpoint.Records[1].BlobBase64 = &empty
			in.Checkpoint.Records[1].Size = 0
			if missing {
				in.Checkpoint.Records[1].BlobBase64 = nil
			}
			var result map[string]any
			status := "available"
			if missing {
				status = "partial"
			}
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", in, &result).StatusCode; got != 200 || result["checkpoint"] != status {
				t.Fatalf("blob state: %d %+v", got, result)
			}
			var preview checkpointDiff
			postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/native/checkpoints/1/preview", checkpointRootsRequest{AuthorizedRoots: []string{in.Checkpoint.Records[1].Root}}, &preview)
			if missing && preview.CaptureErrors != 1 {
				t.Fatalf("missing blob not reported: %+v", preview)
			}
			if !missing && preview.RestoreFiles != 1 {
				t.Fatalf("empty blob lost: %+v", preview)
			}
		})
	}
}
