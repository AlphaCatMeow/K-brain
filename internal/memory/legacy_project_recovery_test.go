package memory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLegacyMigrationPreservesExistingBackup(t *testing.T) {
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "dup", "2026-09-02T00:00:00Z", "legacy body")
	seedProjectEntry(t, root, current, workdir, "dup", "2026-10-01T00:00:00Z", "current body")
	backup := filepath.Join(root, "projects", current, ".legacy-"+legacy, "dup.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("previous preserved memory")
	if err := os.WriteFile(backup, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := rawStore(root).migrateLegacyProjects(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("previous backup overwritten: %q", got)
	}
}

func TestLegacyMigrationConcurrentProcesses(t *testing.T) {
	if root := os.Getenv("KB_TEST_LEGACY_MIGRATION_ROOT"); root != "" {
		isWindows = true
		if err := rawStore(root).migrateLegacyProjects(); err != nil {
			t.Fatal(err)
		}
		return
	}
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, current, workdir, "dup", "2026-10-01T00:00:00Z", "current body")
	seedProjectEntry(t, root, legacy, workdir, "dup", "2026-09-02T00:00:00Z", "legacy body")
	backup := filepath.Join(root, "projects", current, ".legacy-"+legacy, "dup.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("previous backup"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLegacyMigrationConcurrentProcesses$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "KB_TEST_LEGACY_MIGRATION_ROOT="+root)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("subprocess: %v %s", err, output)
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(backup)
	if err != nil || string(got) != "previous backup" {
		t.Fatalf("backup changed: %q %v", got, err)
	}
	got, err = os.ReadFile(backup + ".1")
	if err != nil || !strings.Contains(string(got), "legacy body") {
		t.Fatalf("legacy backup lost: %q %v", got, err)
	}
	if _, err := os.Stat(backup + ".2"); !os.IsNotExist(err) {
		t.Fatalf("duplicate migration backup: %v", err)
	}
}

func TestLegacyBackupRepeatedImportKeepsEveryVersion(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(fmt.Sprint(newer), func(t *testing.T) {
			root, workdir, legacy, current := legacyFixture(t)
			seedProjectEntry(t, root, current, workdir, "dup", "2026-10-01T00:00:00Z", "current body")
			updated := "2026-09-02T00:00:00Z"
			if newer {
				updated = "2026-11-02T00:00:00Z"
			}
			s := rawStore(root)
			for i := 0; i < 3; i++ {
				seedProjectEntry(t, root, legacy, workdir, "dup", updated, fmt.Sprintf("import-%d", i))
				if err := s.migrateLegacyProjects(); err != nil {
					t.Fatal(err)
				}
			}
			var bodies string
			err := filepath.WalkDir(filepath.Join(root, "projects", current), func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				bodies += string(data)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"current body", "import-0", "import-1", "import-2"} {
				if !strings.Contains(bodies, want) {
					t.Fatalf("lost %s", want)
				}
			}
			got, err := s.Read(ReadArgs{Slug: "dup", Scope: "project", Workdir: workdir})
			if err != nil {
				t.Fatal(err)
			}
			want := "current body"
			if newer {
				want = "import-0"
			}
			if !strings.Contains(got.Body, want) {
				t.Fatalf("live body %q, want %s", got.Body, want)
			}
			list, err := s.List(ListArgs{IncludeAllProjects: true})
			if err != nil || len(list.Entries) != 1 {
				t.Fatalf("backup indexed: %+v %v", list, err)
			}
		})
	}
}

func TestLegacyMigrationRetriesPartialFailureOnOpen(t *testing.T) {
	root, workdir, legacy, current := legacyFixture(t)
	seedProjectEntry(t, root, legacy, workdir, "a", "2026-09-02T00:00:00Z", "a body")
	if err := os.MkdirAll(filepath.Join(root, "projects", legacy, "z"), 0700); err != nil {
		t.Fatal(err)
	}
	seedProjectEntry(t, root, legacy, workdir, "z/nested", "2026-09-02T00:00:00Z", "z body")
	seedProjectEntry(t, root, current, workdir, "a", "2026-10-01T00:00:00Z", "current body")
	blocker := filepath.Join(root, "projects", current, "z")
	if err := os.WriteFile(blocker, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	s := rawStore(root)
	if err := s.saveState(storeState{Rejections: []Rejection{{Slug: "gone", Scope: "project", WorkdirHash: legacy}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(root); err != nil {
		t.Fatal(err)
	}
	if _, done := legacyMigrations.Load(root); done {
		t.Fatal("failed migration marked complete")
	}
	if readWorkdirMarker(filepath.Join(root, "projects", legacy)) != workdir {
		t.Fatal("retry marker lost")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", legacy)); !os.IsNotExist(err) {
		t.Fatal("legacy directory remains", err)
	}
	if state, err := s.state(); err != nil || state.Rejections[0].WorkdirHash != current {
		t.Fatalf("rejections %+v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", current, "z", "nested.md")); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyBackupConcurrentReservations(t *testing.T) {
	root := t.TempDir()
	s := rawStore(root)
	dst := filepath.Join(root, ".legacy", "dup.md")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		src := filepath.Join(root, fmt.Sprintf("source-%d", i))
		if err := os.WriteFile(src, []byte(fmt.Sprint(i)), 0600); err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			if err := s.preserveLegacyFile(src, dst); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]bool{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(dst), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	if len(entries) != 12 || len(contents) != 12 {
		t.Fatalf("backups=%d unique=%d", len(entries), len(contents))
	}
}
