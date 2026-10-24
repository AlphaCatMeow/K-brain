package backend

import (
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFilesystemMutationLifecycleAndConflicts(t *testing.T) {
	dir := t.TempDir()
	s := &Server{filesystemRoots: []string{dir}}
	r := httptest.NewRequest("POST", "/v1/fs", nil)
	call := func(in filesystemInput) map[string]any {
		t.Helper()
		in.Workdir = dir
		out, err := s.filesystem(r, in)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	call(filesystemInput{Action: "mkdir", Path: "new"})
	created := call(filesystemInput{Action: "write_text", Path: "new/file.txt", Content: "first\n", Mode: "rewrite"})
	if created["existed_before"] != false || created["total_lines"] != 1 {
		t.Fatal(created)
	}
	if _, err := s.filesystem(r, filesystemInput{Action: "write_text", Workdir: dir, Path: "new/file.txt", Content: "overwrite"}); err == nil {
		t.Fatal("overwrite without read accepted")
	}
	read := call(filesystemInput{Action: "read_text", Path: "new/file.txt"})
	preview := call(filesystemInput{Action: "read_preview", Path: "new/file.txt"})
	if preview["data"] != base64.StdEncoding.EncodeToString([]byte("first\n")) || preview["content_hash"] != read["content_hash"] {
		t.Fatal(preview)
	}
	mtime, hash := read["mtime_ms"].(int64), read["content_hash"].(string)
	input := filesystemInput{Action: "write_text", Workdir: dir, Path: "new/file.txt", Content: "second", ExpectedMtime: &mtime, ExpectedHash: &hash}
	if err := os.WriteFile(filepath.Join(dir, "new/file.txt"), []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.filesystem(r, input); err == nil {
		t.Fatal("stale version accepted")
	}
	read = call(filesystemInput{Action: "read_text", Path: "new/file.txt"})
	mtime, hash = read["mtime_ms"].(int64), read["content_hash"].(string)
	call(input)
	if _, err := s.filesystem(r, input); err == nil {
		t.Fatal("replayed stale mutation accepted")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if _, err := s.filesystem(r, filesystemInput{Action: "rename", Workdir: dir, Path: "new/file.txt", ToPath: "new/renamed.txt"}); err == nil {
			t.Fatal("unsupported exclusive rename accepted")
		}
		call(filesystemInput{Action: "delete", Path: "new/file.txt"})
		call(filesystemInput{Action: "delete", Path: "new"})
		return
	}
	call(filesystemInput{Action: "rename", Path: "new/file.txt", ToPath: "new/renamed.txt"})
	call(filesystemInput{Action: "write_text", Path: "new/occupied.txt", Content: "preserved"})
	if _, err := s.filesystem(r, filesystemInput{Action: "rename", Workdir: dir, Path: "new/renamed.txt", ToPath: "new/occupied.txt"}); err == nil {
		t.Fatal("rename overwrote destination")
	}
	call(filesystemInput{Action: "delete", Path: "new/occupied.txt"})
	if _, err := s.filesystem(r, filesystemInput{Action: "delete", Workdir: dir, Path: "new"}); err == nil {
		t.Fatal("nonempty directory recursively removed")
	}
	call(filesystemInput{Action: "delete", Path: "new/renamed.txt"})
	call(filesystemInput{Action: "delete", Path: "new"})
	for _, in := range []filesystemInput{{Action: "write_text", Path: "../escape", Content: "bad"}, {Action: "mkdir", Path: "."}, {Action: "rename", Path: "missing", ToPath: "../escape"}} {
		in.Workdir = dir
		if _, err := s.filesystem(r, in); err == nil {
			t.Fatalf("accepted invalid mutation: %+v", in)
		}
	}
}
