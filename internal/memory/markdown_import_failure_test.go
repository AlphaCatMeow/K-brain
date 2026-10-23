package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func globalBodies(t *testing.T, store *Store) []string {
	t.Helper()
	list, err := store.List(ListArgs{Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, e := range list.Entries {
		got, err := store.Read(ReadArgs{Slug: e.Slug, Scope: "global"})
		if err == nil {
			bodies = append(bodies, got.Body)
		}
	}
	return bodies
}

// memory.md exists but cannot be read at the first backend start (here: a
// directory stands in for a locked / permission-denied file). Entries()
// swallows the read error, so the one-time import marks itself done and the
// facts are never carried into the store once the file is readable again.
func TestImportMarkdownUnreadableFileIsNotMarkedImported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	n, firstErr := store.ImportMarkdown(scope)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("- [ ] prefers pnpm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportMarkdown(scope); err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 1 {
		t.Fatalf("unreadable memory.md at first start: first import n=%d err=%v, after it became readable store has %q", n, firstErr, bodies)
	}
}

// The state save that sets markdownImported fails after the entries were
// written. kn backend only logs the error and keeps serving, so the user can
// delete the imported entry; the retry on the next start brings it back.
func TestImportMarkdownRetryAfterStateSaveFailureKeepsDeletion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("relies on Windows refusing to rename over an open file")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] prefers tabs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "memory")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, ".kbrain-state.json")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		if err := os.WriteFile(statePath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	_, saveErr := store.ImportMarkdown(scope)
	held.Close()
	if saveErr == nil {
		t.Skip("state save did not fail; cannot inject the I/O error here")
	}
	// Whatever the failed attempt wrote, the retry completes the import once and
	// a deletion made afterwards sticks.
	if _, err := store.ImportMarkdown(scope); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ListArgs{Scope: "global"})
	if err != nil || len(list.Entries) != 1 {
		t.Fatalf("retry after %v left %+v %v", saveErr, list.Entries, err)
	}
	if _, err := store.Delete(DeleteArgs{ReadArgs: ReadArgs{Slug: list.Entries[0].Slug, Scope: "global"}, Actor: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportMarkdown(scope); err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 0 {
		t.Fatalf("deleted entry came back: %q", bodies)
	}
}

// A store written by the previous revision of this branch records imported
// hashes in markdownImports and has no markdownImported flag. The user deleted
// the imported entry; upgrading re-runs the import and resurrects it.
func TestImportMarkdownUpgradeFromHashListKeepsDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] prefers tabs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "memory")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("prefers tabs"))
	legacy := `{"markdownImports":["` + hex.EncodeToString(sum[:6]) + `"]}`
	if err := os.WriteFile(filepath.Join(root, ".kbrain-state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := store.ImportMarkdown(Scope{Path: path, Name: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 0 {
		t.Fatalf("upgrade re-imported %d entries the old state already recorded: %q", n, bodies)
	}
}

// The one-time import rewrites .kbrain-state.json; organizer runs, rejections
// and the organizer epoch written before it must survive.
func TestImportMarkdownKeepsOtherStateFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] prefers tabs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "memory")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := `{"rejections":[{"slug":"x","scope":"global","rejectedAt":1,"actor":"user","reason":"wrong"}],"runs":[{"id":"run-1","status":"done"}],"audit":[{"k":1}],"organizerEpoch":7}`
	if err := os.WriteFile(filepath.Join(root, ".kbrain-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportMarkdown(Scope{Path: path, Name: "installation"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.state()
	if err != nil {
		t.Fatal(err)
	}
	if !got.MarkdownImported || len(got.Rejections) != 1 || len(got.Runs) != 1 || len(got.Audit) != 1 || got.OrganizerEpoch != 7 {
		t.Fatalf("state after import: %+v", got)
	}
}
