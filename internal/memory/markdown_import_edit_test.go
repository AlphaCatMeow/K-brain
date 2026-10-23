package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// memory.md stays writable for TUI / kn (remember, forget, hand edits). A line
// changed after it was imported is imported again under a new hash on the next
// backend start, while the earlier copy stays in the store, so backend sessions
// receive both the superseded and the corrected fact.
func TestImportMarkdownEditedLineDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	store, err := OpenStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	for _, content := range []string{"- [ ] prefers tabs\n", "- [ ] prefers spaces\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ImportMarkdown(scope); err != nil {
			t.Fatal(err)
		}
	}
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
	if len(bodies) != 1 {
		t.Fatalf("edited memory.md line left %d store entries: %q", len(bodies), bodies)
	}
}
