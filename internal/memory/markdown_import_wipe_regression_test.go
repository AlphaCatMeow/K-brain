package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// 2c1b032 settles a pending import claim in deleteLocked, but WipeAll
// (/v1/memory/manage memory_wipe_all) removes entry files directly and keeps
// markdownPending. State below is exactly what a failed finish() save leaves
// (the fault TestImportMarkdownFinishSaveFailureDoesNotResurrectDeletion
// injects): entry file written, hash still pending, import not marked done.
// The backend serves, the user wipes all memory, the next start writes the
// wiped entry back.
func TestImportMarkdownFinishSaveFailureDoesNotResurrectAfterWipeAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] prefers tabs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	if _, err := store.ImportMarkdown(scope); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("prefers tabs"))
	hash := hex.EncodeToString(sum[:6])
	slug := "imported-" + hash
	// Roll the state back to what finish() had on disk before its failed save.
	state, err := store.state()
	if err != nil {
		t.Fatal(err)
	}
	state.MarkdownImported = false
	state.MarkdownImports = nil
	state.MarkdownPending = []string{hash}
	if err := store.saveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ReadArgs{Slug: slug, Scope: "global"}); err != nil {
		t.Fatalf("precondition: imported entry should be served: %v", err)
	}
	if _, err := store.Dispatch("memory_wipe_all", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 0 {
		t.Fatalf("precondition: wipe left %q", bodies)
	}
	// Next backend start.
	n, retryErr := store.ImportMarkdown(scope)
	if _, err := store.Read(ReadArgs{Slug: slug, Scope: "global"}); err == nil {
		t.Fatalf("user wiped all memory while %s was pending; next start (n=%d err=%v) wrote it back", slug, n, retryErr)
	}
}
