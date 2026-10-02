package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointCaptureStoresPreImageAndMissingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	capture := newCheckpointCapture(t.TempDir(), "session", "run", "turn")
	capture.capture(path)
	missing := filepath.Join(root, "new.txt")
	capture.capture(missing)
	if err := os.WriteFile(path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := capture.commit(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Entries) != 2 || record.Entries[0].Path != path || record.Entries[1].Path != missing {
		t.Fatalf("unexpected checkpoint entries: %+v", record.Entries)
	}
	if string(record.Entries[0].Data) != "before" || !record.Entries[1].Missing {
		t.Fatalf("pre-images were not retained: %+v", record.Entries)
	}
}

func TestAuthorizedFileRejectsSymlinkPath(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path := filepath.Join(link, "file.txt")
	if authorizedFile(path, []string{root}) {
		t.Fatal("symlink path was authorized")
	}
}
