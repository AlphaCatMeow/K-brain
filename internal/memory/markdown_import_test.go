package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportMarkdownRunsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")
	if err := os.WriteFile(path, []byte("- [ ] 所有 UI 工作遵守 design.md 规范\n- [x] stale fact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Path: path, Name: "installation"}
	n, err := store.ImportMarkdown(scope)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	list, err := store.List(ListArgs{Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 || list.Entries[0].MemoryType != "user" || !list.Entries[0].Unreviewed {
		t.Fatalf("entries=%+v", list.Entries)
	}
	slug := list.Entries[0].Slug
	got, err := store.Read(ReadArgs{Slug: slug, Scope: "global"})
	if err != nil || !strings.Contains(got.Body, "design.md") {
		t.Fatalf("body=%q err=%v", got.Body, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("memory.md must stay for TUI and kn: %v", err)
	}

	// The import runs once: deletions in the store stick and later memory.md
	// lines stay with the hosts that still use the file.
	if _, err := store.Delete(DeleteArgs{ReadArgs: ReadArgs{Slug: slug, Scope: "global"}, Actor: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := scope.Remember("prefers pnpm"); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ImportMarkdown(scope); err != nil || n != 0 {
		t.Fatalf("second import n=%d err=%v", n, err)
	}
	if list, _ := store.List(ListArgs{Scope: "global"}); len(list.Entries) != 0 {
		t.Fatalf("second import wrote %+v", list.Entries)
	}
}

func TestImportMarkdownMissingFile(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.ImportMarkdown(Scope{Path: filepath.Join(t.TempDir(), "absent.md")}); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
