package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// 745d0be: "drop its hash before returning and let the next start retry it".
// The release is `_ = s.saveState(state)`. When the fault that broke the entry
// write also breaks that state save (full disk, a locked state file), the error
// is discarded, the claim stays on disk with no entry behind it, and the next
// start skips the entry and marks the import done: the fact is lost and the
// caller was told only that the import is incomplete.
//
// Fault injection: a directory occupies the entry path (write fails), and a
// goroutine holds .kbrain-state.json open as soon as the claim lands, so the
// release rename fails (Windows refuses to rename over an open file). Many
// project files widen the window between claim and release (load walks them).
func TestImportMarkdownReleaseFailureDoesNotLoseEntry(t *testing.T) {
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
	filler := filepath.Join(root, "projects", "0000000000000000")
	if err := os.MkdirAll(filler, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 3000 {
		if err := os.WriteFile(filepath.Join(filler, fmt.Sprintf("f%04d.md", i)), []byte("---\nname: f\n---\n\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("prefers tabs"))
	hash := hex.EncodeToString(sum[:6])
	target := filepath.Join(root, "global", "user", "imported-"+hash+".md")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
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
			if data, err := os.ReadFile(statePath); err == nil && strings.Contains(string(data), hash) {
				if f, err := os.Open(statePath); err == nil {
					mu.Lock()
					held = f
					mu.Unlock()
				}
				return
			}
			time.Sleep(200 * time.Microsecond)
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
	if firstErr == nil {
		t.Skip("entry write did not fail")
	}
	state, err := store.state()
	if err != nil {
		t.Fatal(err)
	}
	if held == nil || !strings.Contains(strings.Join(append(state.MarkdownImports, state.MarkdownPending...), ","), hash) {
		t.Skipf("release save was not interrupted (held=%v state=%+v); fault not injected", held != nil, state)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(root, "projects")} {
		_ = os.RemoveAll(d)
	}
	n, retryErr := store.ImportMarkdown(scope)
	if bodies := globalBodies(t, store); len(bodies) != 1 {
		t.Fatalf("write failed (%v) and its claim could not be released; next start imported n=%d err=%v, store has %q: the entry is lost", firstErr, n, retryErr, bodies)
	}
}
