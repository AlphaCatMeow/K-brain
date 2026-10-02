package backend

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestCheckpointHTTPResponseArrays(t *testing.T) {
	for _, scenario := range []string{"success", "partial", "empty", "conflict", "write-failure"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
			input := legacyCheckpointFixture(t)
			workspace := input.Checkpoint.Records[1].Root
			switch scenario {
			case "partial":
				input.Checkpoint.Records[1].BlobBase64 = nil
				input.Checkpoint.Records = input.Checkpoint.Records[:2]
			case "empty":
				input.Checkpoint.Records = input.Checkpoint.Records[:1]
			case "write-failure":
				if err := os.Mkdir(filepath.Join(workspace, "binary"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var imported map[string]any
			if got := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &imported).StatusCode; got != http.StatusOK {
				t.Fatalf("import: %d %+v", got, imported)
			}
			var preview map[string]any
			endpoint := server.URL + "/v1/sessions/native/checkpoints/1"
			if got := postJSON(t, http.DefaultClient, endpoint+"/preview", checkpointRootsRequest{AuthorizedRoots: []string{workspace}}, &preview).StatusCode; got != http.StatusOK {
				t.Fatalf("preview: %d %+v", got, preview)
			}
			entries, ok := preview["entries"].([]any)
			if !ok {
				t.Fatalf("preview entries must be an array: %+v", preview)
			}
			if (scenario == "partial" || scenario == "empty") && len(entries) != 0 {
				t.Fatalf("expected empty preview entries: %+v", preview)
			}
			expected := []checkpointExpected{}
			for _, value := range entries {
				entry := value.(map[string]any)
				expected = append(expected, checkpointExpected{Key: entry["key"].(string), CurrentHash: entry["current_hash"].(string)})
			}
			if scenario == "conflict" {
				expected = nil
			}
			var result map[string]any
			got := postJSON(t, http.DefaultClient, endpoint+"/rewind", checkpointRewindRequest{AuthorizedRoots: []string{workspace}, Expected: expected}, &result).StatusCode
			want := http.StatusOK
			if scenario == "conflict" {
				want = http.StatusConflict
			} else if scenario == "write-failure" {
				want = http.StatusInternalServerError
			}
			if got != want {
				t.Fatalf("rewind: %d, want %d: %+v", got, want, result)
			}
			for _, key := range []string{"conflicts", "failed"} {
				values, ok := result[key].([]any)
				if !ok {
					t.Fatalf("%s must be an array: %+v", key, result)
				}
				populated := scenario == "conflict" && key == "conflicts" || scenario == "write-failure" && key == "failed"
				if (len(values) > 0) != populated {
					t.Fatalf("unexpected %s entries: %+v", key, result)
				}
			}
			if scenario == "partial" && result["capture_errors"] != float64(1) {
				t.Fatalf("partial capture errors lost: %+v", result)
			}
			if scenario == "success" && result["restored_files"] != float64(1) {
				t.Fatalf("preimage not restored: %+v", result)
			}
		})
	}
}
