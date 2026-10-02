package session

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestLegacyImportRestartAndActiveContext(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{true: "empty", false: "segmented"}[empty], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			messages := []ai.Message{{ID: "sys", Role: "system", Content: "system"}, {ID: "old", Role: "user", Content: "sealed"}, {ID: "active", Role: "user", Content: "active"}}
			boundary := &Compaction{Seq: 1, Cutoff: 2, Summary: "latest summary", DropPrior: true}
			if empty {
				messages = nil
				boundary = nil
			}
			imported := func(st *Store, fingerprint string) (bool, error) {
				return st.ImportHistory("legacy", "legacy", "source-v1", fingerprint, `{"segments":[0,1]}`, "", "m", "p", "title", time.Unix(1, 0), time.Unix(2, 0), true, false, false, "", messages, boundary)
			}
			if already, err := imported(store, "content-v1"); err != nil || already {
				t.Fatalf("import: %v %v", already, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if already, err := imported(store, "content-v1"); err != nil || !already {
				t.Fatalf("restart retry: %v %v", already, err)
			}
			if _, err := imported(store, "tampered"); err == nil {
				t.Fatal("content conflict accepted")
			}
			if !reflect.DeepEqual(store.RawMessages("legacy"), messages) {
				t.Fatal("raw messages changed")
			}
			_, active, err := store.Load("legacy")
			if err != nil {
				t.Fatal(err)
			}
			if empty && len(active) != 0 {
				t.Fatal("empty draft acquired messages")
			}
			if !empty && (len(active) != 3 || active[2].ID != "active" || active[1].Content != "Summary of the conversation so far:\n\nlatest summary") {
				t.Fatalf("active context: %+v", active)
			}
		})
	}
}
