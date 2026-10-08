package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestLegacyHashesMatchDesktopStore(t *testing.T) {
	// Real ids from a Windows install written by LiveAgent 1.3.x.
	p := `D:\Documents\Projects\Web\LiveAgent`
	if got := hashProjectPath(p); got != "8992e417eddee43c" {
		t.Fatalf("current id %s", got)
	}
	if got := legacyHashesFor(p, p, true); !slices.Contains(got, "f33ea6e0d31111bf") {
		t.Fatalf("legacy ids %v", got)
	}
	if got := verbatimPath(`\\srv\share\x`); got != `\\?\UNC\srv\share\x` {
		t.Fatal(got)
	}
	if got := legacyHashesFor(p, p, false); len(got) != 0 {
		t.Fatalf("non-windows clean path has no legacy id: %v", got)
	}
}

func legacyFixture(t *testing.T) (root, workdir, legacy, current string) {
	t.Helper()
	prev := isWindows
	isWindows = true
	t.Cleanup(func() { isWindows = prev })
	root, _ = filepath.EvalSymlinks(t.TempDir())
	workdir, _ = filepath.EvalSymlinks(t.TempDir())
	resolved, _ := resolveProjectPath(workdir)
	legacy = hashProjectPath(verbatimPath(resolved))
	current, _ = ProjectHash(workdir)
	if legacy == current {
		t.Fatal("fixture ids collide")
	}
	return
}

func seedProjectEntry(t *testing.T, root, dir, workdir, slug, updated, body string) {
	t.Helper()
	d := filepath.Join(root, "projects", dir)
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(map[string]string{"path": workdir})
	if err := os.WriteFile(filepath.Join(d, ".workdir.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: " + slug + "\ntype: project\nscope: project\ndescription: d\ncreatedAt: 2026-09-01T00:00:00Z\nupdatedAt: " + updated + "\nsource:\n  unreviewed: true\nlinks: []\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(d, slug+".md"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func rawStore(root string) *Store { return &Store{root: root, mu: &sync.Mutex{}} }

func TestLegacyProjectEntryUsableWithoutMigration(t *testing.T) {
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "p-one", "2026-09-02T00:00:00Z", "legacy body")
	s := rawStore(root)
	list, err := s.List(ListArgs{IncludeAllProjects: true})
	if err != nil || len(list.Entries) != 1 || list.Entries[0].WorkdirHash != current || list.Entries[0].WorkdirPath != workdir {
		t.Fatalf("%v %+v", err, list.Entries)
	}
	// The panel sends the marker path with the id it listed; old clients send the legacy id.
	for _, args := range []ReadArgs{
		{Slug: "p-one", Scope: "project", Workdir: workdir, WorkdirHash: current},
		{Slug: "p-one", Scope: "project", Workdir: workdir, WorkdirHash: legacy},
		{Slug: "p-one", Scope: "project", WorkdirHash: legacy},
		{Slug: "p-one", Scope: "project", WorkdirHash: current},
	} {
		if _, err := s.Read(args); err != nil {
			t.Fatalf("%+v: %v", args, err)
		}
	}
	if _, err := s.Accept(ReadArgs{Slug: "p-one", Scope: "project", Workdir: workdir, WorkdirHash: current}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", legacy, "p-one.md")); !os.IsNotExist(err) {
		t.Fatal("legacy copy left behind after accept")
	}
	list, _ = s.List(ListArgs{IncludeAllProjects: true})
	if len(list.Entries) != 1 || list.Entries[0].Unreviewed || list.Entries[0].WorkdirHash != current {
		t.Fatalf("%+v", list.Entries)
	}
}

func TestUnrelatedHashStillRejected(t *testing.T) {
	root, workdir, _, _ := legacyFixture(t)
	_, err := rawStore(root).Read(ReadArgs{Slug: "x", Scope: "project", Workdir: workdir, WorkdirHash: "0123456789abcdef"})
	var se *StoreError
	if err == nil || !strings.Contains(err.Error(), "disagree") || !asStoreError(err, &se) || se.Code != "scope_mismatch" {
		t.Fatal(err)
	}
}

func asStoreError(err error, out **StoreError) bool {
	e, ok := err.(*StoreError)
	*out = e
	return ok
}

func TestMigrationFoldsLegacyDirectory(t *testing.T) {
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "p-one", "2026-09-02T00:00:00Z", "legacy body")
	s := rawStore(root)
	if err := s.saveState(storeState{Rejections: []Rejection{{Slug: "gone", Scope: "project", WorkdirHash: legacy}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateLegacyProjects(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", legacy)); !os.IsNotExist(err) {
		t.Fatal("legacy directory still present")
	}
	if readWorkdirMarker(filepath.Join(root, "projects", current)) != workdir {
		t.Fatal("marker not carried over")
	}
	if st, _ := s.state(); st.Rejections[0].WorkdirHash != current {
		t.Fatalf("%+v", st.Rejections)
	}
	if err := s.migrateLegacyProjects(); err != nil {
		t.Fatal("not idempotent:", err)
	}
	if _, err := s.Read(ReadArgs{Slug: "p-one", Scope: "project", Workdir: workdir, WorkdirHash: current}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationKeepsNewerCopyAndPreservesOlder(t *testing.T) {
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "dup", "2026-09-02T00:00:00Z", "old body")
	seedProjectEntry(t, root, legacy, workdir, "only-legacy", "2026-09-02T00:00:00Z", "kept")
	seedProjectEntry(t, root, current, workdir, "dup", "2026-10-01T00:00:00Z", "new body")
	s := rawStore(root)
	if err := s.migrateLegacyProjects(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ReadArgs{Slug: "dup", Scope: "project", Workdir: workdir})
	if err != nil || !strings.Contains(got.Body, "new body") {
		t.Fatalf("%v %q", err, got.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", current, ".legacy-"+legacy, "dup.md")); err != nil {
		t.Fatal("older copy not preserved:", err)
	}
	list, _ := s.List(ListArgs{IncludeAllProjects: true})
	if len(list.Entries) != 2 {
		t.Fatalf("project split or conflict copy indexed: %+v", list.Entries)
	}
}

func TestDeleteProjectRemovesLegacyDirectory(t *testing.T) {
	root, workdir, legacy, _ := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "p-one", "2026-09-02T00:00:00Z", "b")
	if _, err := rawStore(root).DeleteProject(workdir, "user", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", legacy)); !os.IsNotExist(err) {
		t.Fatal("legacy directory survived project deletion")
	}
}
