package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestLegacyCheckpointUnresolvedRepairHTTP(t *testing.T) {
	for _, hadCheckpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-request", true: "new-request"}[hadCheckpoint], func(t *testing.T) {
			root := t.TempDir()
			store, err := session.Open(filepath.Join(root, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newTestServer(t, store, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
			repaired := legacyCheckpointFixture(t)
			old := repaired
			old.SourceFingerprint = "unresolved-v1"
			old.Checkpoint = nil
			old.SourceMetadata = json.RawMessage(`{"original":{"id":"native","checkpointStatus":"unresolved"},"checkpoint":{"status":"unresolved","nativePath":"~/.liveagent/checkpoints/native","reason":"unresolved"},"file_ledger":{"metadata":"keep"}}`)
			if hadCheckpoint {
				old.Checkpoint = &legacyCheckpointImport{Status: "unresolved", NativePath: "~/.liveagent/checkpoints/native", Reason: "unresolved"}
			}
			var result map[string]any
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", old, &result).StatusCode; got != 200 || result["checkpoint"] != "unresolved" {
				t.Fatalf("old import: %d %+v", got, result)
			}
			repaired.SourceMetadata = json.RawMessage(`{"original":{"id":"native","checkpoint":{"status":"available"}},"checkpoint":{"status":"available"},"file_ledger":{"metadata":"keep"}}`)
			for _, mutate := range []func(*legacyHistoryImportRequest){
				func(in *legacyHistoryImportRequest) { in.Title = "tampered title" },
				func(in *legacyHistoryImportRequest) {
					in.ActiveContext = &legacyActiveContext{Cutoff: 1, Summary: "tampered"}
				},
				func(in *legacyHistoryImportRequest) {
					in.SourceMetadata = json.RawMessage(`{"checkpoint":{"status":"available"},"file_ledger":{"metadata":"changed"}}`)
				},
			} {
				bad := repaired
				mutate(&bad)
				if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", bad, &result).StatusCode; got != 409 {
					t.Fatalf("changed history accepted: %d %+v", got, result)
				}
			}
			originalMessages := store.RawMessages("native")
			if err := store.Save("native", len(originalMessages), append(append([]ai.Message(nil), originalMessages...), ai.Message{Role: "assistant", Content: "new backend turn"}), "m", ""); err != nil {
				t.Fatal(err)
			}
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", repaired, &result).StatusCode; got != 409 {
				t.Fatalf("modified backend history repaired: %d %+v", got, result)
			}
			if err := store.ReplaceFrom("native", 0, originalMessages, "m", ""); err != nil {
				t.Fatal(err)
			}
			partial := repaired
			partialCheckpoint := *repaired.Checkpoint
			partialCheckpoint.Status = "partial"
			partial.Checkpoint = &partialCheckpoint
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", partial, &result).StatusCode; got != 409 {
				t.Fatalf("partial repair accepted: %d %+v", got, result)
			}
			blocker := filepath.Join(root, "events", "checkpoints")
			if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", repaired, &result).StatusCode; got != 500 {
				t.Fatalf("repair persistence failure: %d %+v", got, result)
			}
			if err := os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", repaired, &result).StatusCode; got != 200 || result["checkpoint"] != "available" || result["status"] != "already_imported" {
					t.Fatalf("repair %d: %d %+v", i, got, result)
				}
			}
			meta, _, err := store.Load("native")
			if err != nil {
				t.Fatal(err)
			}
			if meta.ImportMetadata != string(old.SourceMetadata) || meta.ImportFingerprint != "unresolved-v1" {
				t.Fatal("repair rewrote original provenance")
			}
			if len(store.Snapshots("native")) != 2 {
				t.Fatal("repair did not register checkpoints")
			}
			if err := store.ClearSnapshots("native"); err != nil {
				t.Fatal(err)
			}
			server.Close()
			store.Close()
			reopened, err := session.Open(filepath.Join(root, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, restarted := newTestServer(t, reopened, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
			if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", repaired, &result).StatusCode; got != 200 || result["checkpoint"] != "available" {
				t.Fatalf("restart repair: %d %+v", got, result)
			}
			if len(reopened.Snapshots("native")) != 0 {
				t.Fatal("repair resurrected consumed references")
			}
			changed := repaired
			changed.SourceFingerprint = "different-repair"
			if got := postJSON(t, http.DefaultClient, restarted.URL+"/v1/migrations/liveagent-history", changed, &result).StatusCode; got != 409 {
				t.Fatalf("second repair accepted: %d %+v", got, result)
			}
		})
	}
}
