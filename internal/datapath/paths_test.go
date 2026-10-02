package datapath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestResolveMigratesWithoutOverwriteAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, LegacyDirName)
	current := filepath.Join(root, LiveAgentDirName)
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "sessions", "old.jsonl"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "config.json"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(root, "", "")
	if err != nil || got != current {
		t.Fatalf("Resolve() = %q, %v", got, err)
	}
	for path, want := range map[string]string{
		filepath.Join(current, "config.json"):           "current",
		filepath.Join(current, "sessions", "old.jsonl"): "old",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("migrated %q = %q, %v", path, data, err)
		}
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy directory was removed: %v", err)
	}
	if _, err := Resolve(root, "", ""); err != nil {
		t.Fatalf("second Resolve() failed: %v", err)
	}
}

func TestResolveExplicitOverridesDoNotMigrateHome(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, LegacyDirName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(root, filepath.Join(root, "fixture"), "")
	if err != nil || got != filepath.Join(root, "fixture") {
		t.Fatalf("preferred Resolve() = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, LiveAgentDirName)); !os.IsNotExist(err) {
		t.Fatalf("preferred override unexpectedly touched default root: %v", err)
	}
}

func TestMigrateConcurrentNestedFiles(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "old"), filepath.Join(root, "new")
	for i := 0; i < 12; i++ {
		dir := filepath.Join(src, "branch", string(rune('a'+i)), "deep")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 12; j++ {
			name := filepath.Join(dir, "file-"+string(rune('a'+j)))
			if err := os.WriteFile(name, []byte(name), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	}
	if runtime.GOOS != "windows" {
		for i := 0; i < 24; i++ {
			if err := os.Symlink("branch/a/deep/file-a", filepath.Join(src, "link-"+string(rune('a'+i)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	const workers = 12
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- Migrate(src, dst)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration failed: %v", err)
		}
	}
	for i := 0; i < 12; i++ {
		for j := 0; j < 12; j++ {
			path := filepath.Join(dst, "branch", string(rune('a'+i)), "deep", "file-"+string(rune('a'+j)))
			data, err := os.ReadFile(path)
			want := filepath.Join(src, "branch", string(rune('a'+i)), "deep", "file-"+string(rune('a'+j)))
			if err != nil || string(data) != want {
				t.Fatalf("bad migrated file %q: %q, %v", path, data, err)
			}
		}
	}
}

func TestMigratePreservesModesAndSymlinksWithoutFollowingTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes and symlinks")
	}
	root := t.TempDir()
	src, dst := filepath.Join(root, "old"), filepath.Join(root, "new")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o751); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(src, "nested", "run.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "protected"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "protected", "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "protected", "new"), []byte("must not escape"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-relative", filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "protected")); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(src, dst); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Stat(filepath.Join(dst, "nested"))
	if err != nil || directory.Mode().Perm() != 0750 {
		t.Fatalf("directory mode: %v %v", directory, err)
	}
	migrated, err := os.Stat(filepath.Join(dst, "nested", "run.sh"))
	if err != nil || migrated.Mode().Perm() != 0o751 {
		t.Fatalf("destination mode was not preserved: %v, %v", migrated, err)
	}
	if target, targetErr := os.Readlink(filepath.Join(dst, "protected")); targetErr != nil || target != outside {
		t.Fatalf("destination symlink changed: %q, %v", target, targetErr)
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside overwritten: %s, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("traversed target symlink: %v", err)
	}
	info, err := os.Stat(filepath.Join(dst, "config.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private mode: %v %v", info, err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "dangling")); err != nil || link != "missing-relative" {
		t.Fatalf("dangling link: %q %v", link, err)
	}
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil || link != outside {
		t.Fatalf("source symlink was not preserved: %q, %v", link, err)
	}
}

func TestPublishInterruptedCopyLeavesNoTarget(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "target")
	reader := &failingReader{data: []byte("partial"), err: errors.New("interrupted")}
	if err := publish(dst, reader, 0o640); err == nil {
		t.Fatal("interrupted copy should fail")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("interrupted copy published a target: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial copy left entries: %v", entries)
	}
	if err := publish(dst, strings.NewReader("complete"), 0640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "complete" {
		t.Fatalf("retry: %q %v", data, err)
	}
}

type failingReader struct {
	data []byte
	err  error
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	r.done = true
	return n, nil
}

func TestMigrationRejectsRootLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink fixture")
	}
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(src, dst); err == nil {
		t.Fatal("accepted destination root link")
	}
	if err := Migrate(dst, filepath.Join(root, "new")); err == nil {
		t.Fatal("accepted source root link")
	}
	if entries, err := os.ReadDir(src); err != nil || len(entries) != 0 {
		t.Fatalf("source changed: %v %v", entries, err)
	}
}

func TestPublishNeverReplacesExistingTarget(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "existing")
	if err := os.WriteFile(dst, []byte("user"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publish(dst, strings.NewReader("replacement"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := publish(dst, &failingReader{data: []byte("partial"), err: errors.New("failure")}, 0755); err == nil {
		t.Fatal("expected failure")
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "user" {
		t.Fatalf("overwritten: %s %v", data, err)
	}
}

func TestMigrateKeepsConflictingDirectoryType(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "old"), filepath.Join(root, "new")
	if err := os.MkdirAll(filepath.Join(src, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sessions", "session"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "sessions"), []byte("user"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(src, dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "sessions"))
	if err != nil || string(data) != "user" {
		t.Fatalf("conflict changed: %s %v", data, err)
	}
}
