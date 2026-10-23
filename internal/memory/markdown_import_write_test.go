package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The entry file cannot be created at the first backend start (here a
// directory occupies the target path, standing in for a transient lock or
// permission error). ImportMarkdown returns the I/O error and leaves the import
// unfinished, but the hash was already claimed, so the retry after the
// obstruction clears skips the entry and marks the import done: the fact is
// never carried into the store although no copy of it was ever written.
func TestImportMarkdownWriteFailureIsRetried(t *testing.T) {
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
	target := filepath.Join(root, "global", "user", "imported-"+hex.EncodeToString(sum[:6])+".md")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	_, firstErr := store.ImportMarkdown(scope)
	if firstErr == nil {
		t.Skip("write did not fail; cannot inject the I/O error here")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	n, err := store.ImportMarkdown(scope)
	if err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 1 {
		t.Fatalf("first import failed with %v; retry imported n=%d, store has %q", firstErr, n, bodies)
	}
}

// A store that cannot be loaded (an oversized .md file in it) makes every
// writeLocked fail with a plain error. Each backend start claims one more
// memory.md entry before failing, so by the time the store is repaired the
// entries are all claimed and none of them was ever written.
func TestImportMarkdownBrokenStoreDoesNotDrainEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] prefers tabs\n- [ ] uses pnpm\n- [ ] works in UTC+8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "memory")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "global", "huge.md")
	if err := os.MkdirAll(filepath.Dir(big), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 1<<20+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	for start := 1; start <= 3; start++ {
		if _, err := store.ImportMarkdown(scope); err == nil {
			t.Fatalf("start %d: import succeeded against a store that cannot load", start)
		}
	}
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	n, err := store.ImportMarkdown(scope)
	if err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 3 {
		t.Fatalf("after three failed starts and a repaired store, import n=%d left %q", n, bodies)
	}
}

// The round-4 scenario exactly: the entry was written and its hash claimed, but
// the final save that sets markdownImported failed, so the import is still
// unfinished. The user deletes the imported entry while the backend keeps
// running; the retry on the next start must not bring it back. The persisted
// state is built directly because no portable hook fails only the final save.
func TestImportMarkdownDeletionBeforeRetryAfterFinalSaveFailure(t *testing.T) {
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
	hash := hex.EncodeToString(sum[:6])
	if _, err := store.Write(WriteArgs{Slug: "imported-" + hash, Scope: "global", MemoryType: "user", Description: "prefers tabs", Body: "prefers tabs", Actor: "migration"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kbrain-state.json"), []byte(`{"markdownImports":["`+hash+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(DeleteArgs{ReadArgs: ReadArgs{Slug: "imported-" + hash, Scope: "global"}, Actor: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportMarkdown(Scope{Path: path, Name: "installation"}); err != nil {
		t.Fatal(err)
	}
	if bodies := globalBodies(t, store); len(bodies) != 0 {
		t.Fatalf("entry deleted before the retry came back: %q", bodies)
	}
}
