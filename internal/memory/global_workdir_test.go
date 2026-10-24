package memory

import (
	"path/filepath"
	"testing"
	"time"
)

// The LiveAgent memory panel and the chat memory_manager tool pass the current
// workspace even for global entries. A global entry must stay the same entry:
// accepting it from a workspace once rewrote it as new, resetting createdAt and
// replacing its source with "accept".
func TestGlobalEntryIgnoresWorkdir(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(WriteArgs{Slug: "imported-abc", Scope: "global", MemoryType: "user", Description: "d", Body: "b", Actor: "migration"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(ReadArgs{Slug: "imported-abc", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := store.Accept(ReadArgs{Slug: "imported-abc", Scope: "global", Workdir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	after, err := store.Read(ReadArgs{Slug: "imported-abc", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Meta.Unreviewed {
		t.Fatal("accept did not mark the entry reviewed")
	}
	if after.Meta.CreatedAt != before.Meta.CreatedAt {
		t.Fatalf("accept from a workspace reset createdAt: %d -> %d", before.Meta.CreatedAt, after.Meta.CreatedAt)
	}
	if source, _ := after.Meta.Source.(map[string]any); source["actor"] != "migration" {
		t.Fatalf("accept from a workspace replaced the source: %v", after.Meta.Source)
	}
}
