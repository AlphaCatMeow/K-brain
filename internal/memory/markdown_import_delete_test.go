package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// 8877fa3: "the import runs before the backend serves, so this cannot resurrect
// a deletion". cmd/kn/backend.go only logs an ImportMarkdown error and goes on
// to serve. When the entry write lands but finish()'s state save fails (the
// same save fault the pending/done split was built for), the hash stays in
// markdownPending, the backend serves the imported entry, and the user deletes
// it through /v1/memory/manage (Store.Delete). The next start sees the pending
// hash with no entry behind it and writes the entry again.
//
// Fault injection: a goroutine holds .kbrain-state.json open as soon as the
// entry file appears, so finish()'s rename fails (Windows refuses to rename
// over an open file).
func TestImportMarkdownFinishSaveFailureDoesNotResurrectDeletion(t *testing.T) {
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
	sum := sha256.Sum256([]byte("prefers tabs"))
	hash := hex.EncodeToString(sum[:6])
	slug := "imported-" + hash
	target := filepath.Join(root, "global", "user", slug+".md")
	statePath := filepath.Join(root, ".kbrain-state.json")
	var (
		held *os.File
		mu   sync.Mutex
		done = make(chan struct{})
		quit = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			default:
			}
			if _, err := os.Stat(target); err == nil {
				if f, err := os.Open(statePath); err == nil {
					mu.Lock()
					held = f
					mu.Unlock()
				}
				return
			}
		}
	}()
	scope := Scope{Path: path, Name: "installation"}
	_, firstErr := store.ImportMarkdown(scope)
	close(quit)
	<-done
	mu.Lock()
	if held != nil {
		held.Close()
	}
	mu.Unlock()
	state, err := store.state()
	if err != nil {
		t.Fatal(err)
	}
	if firstErr == nil || held == nil || len(state.MarkdownPending) != 1 {
		t.Skipf("finish save was not interrupted (err=%v held=%v state=%+v); fault not injected", firstErr, held != nil, state)
	}
	// backend.go logs firstErr and serves; the user deletes the imported entry.
	if _, err := store.Read(ReadArgs{Slug: slug, Scope: "global"}); err != nil {
		t.Fatalf("entry write should have landed before the failed finish save: %v", err)
	}
	if _, err := store.Delete(DeleteArgs{ReadArgs: ReadArgs{Slug: slug, Scope: "global"}, Actor: "user"}); err != nil {
		t.Fatal(err)
	}
	// Next backend start.
	n, retryErr := store.ImportMarkdown(scope)
	if _, err := store.Read(ReadArgs{Slug: slug, Scope: "global"}); err == nil {
		t.Fatalf("first import failed after the write landed (%v); the user deleted %s while the backend served; next start (n=%d err=%v) wrote it back", firstErr, slug, n, retryErr)
	}
}
