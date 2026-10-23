package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memory.md is hand-editable ("edit or delete lines there directly"). One line
// the store rejects (body over 8 KiB) aborts ImportMarkdown before any later
// entry, on every start, so later remember entries never reach the store.
func TestImportMarkdownOversizedEntryDoesNotBlockLaterEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	content := "- [ ] " + strings.Repeat("a", 9*1024) + "\r\n- [ ] prefers pnpm\r\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	for start := 1; start <= 2; start++ {
		n, err := store.ImportMarkdown(scope)
		list, lerr := store.List(ListArgs{Scope: "global"})
		if lerr != nil {
			t.Fatal(lerr)
		}
		found := false
		for _, e := range list.Entries {
			got, rerr := store.Read(ReadArgs{Slug: e.Slug, Scope: "global"})
			if rerr == nil && got.Body == "prefers pnpm" {
				found = true
			}
		}
		if !found {
			t.Fatalf("start %d: valid entry after an oversized line was not imported (n=%d err=%v)", start, n, err)
		}
	}
}
