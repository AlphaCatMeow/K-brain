package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestStoreExistingLiveAgentMarkdown(t *testing.T) {
	s := newStore(t)
	path := filepath.Join(s.Root(), "global", "user", "preference.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: preference\ntype: user\nscope: global\ndescription: 中文偏好\ncreatedAt: 2026-01-01T00:00:00Z\nsource:\n  unreviewed: true\n  conversationId: legacy\nlinks: []\n---\n\n---\nconfidence: high\nsource_quote: \"用户明确的偏好\"\n---\n\nUse Chinese.\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ReadArgs{Slug: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Confidence != "high" || !got.Meta.Unreviewed || !strings.Contains(got.Body, "Use Chinese.") {
		t.Fatalf("%+v", got)
	}
	after, _ := os.ReadFile(path)
	if string(after) != raw {
		t.Fatal("read modified legacy data")
	}
}
func TestStoreWriteEvidenceAcceptAndIsolation(t *testing.T) {
	s := newStore(t)
	r, err := s.Write(WriteArgs{Slug: "one", Scope: "global", MemoryType: "user", Description: "one", Body: "fact", Evidence: &Evidence{Confidence: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.AppliedConfidence != "low" || r.AutoDowngraded == nil || !*r.AutoDowngraded {
		t.Fatalf("%+v", r)
	}
	got, err := s.Read(ReadArgs{Slug: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Confidence != "low" {
		t.Fatalf("evidence roundtrip: %+v", got)
	}
	if _, err = s.Accept(ReadArgs{Slug: "one", Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Read(ReadArgs{Slug: "one"})
	if got.Meta.Unreviewed {
		t.Fatal("accept did not persist")
	}
	project := t.TempDir()
	_, err = s.Write(WriteArgs{Slug: "one", Scope: "project", Workdir: project, MemoryType: "project", Description: "project", Body: "private"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.Read(ReadArgs{Slug: "one", Workdir: project})
	if got.Body != "private" {
		t.Fatalf("project shadow: %+v", got)
	}
	got, _ = s.Read(ReadArgs{Slug: "one", Workdir: t.TempDir()})
	if strings.Contains(got.Body, "private") {
		t.Fatal("project leaked")
	}
}
func TestStoreRejectPaths(t *testing.T) {
	s := newStore(t)
	for _, slug := range []string{"../oops", "a/b", "", ".."} {
		if _, err := s.Write(WriteArgs{Slug: slug, Scope: "global", MemoryType: "user", Body: "fact"}); err == nil {
			t.Fatalf("accepted %q", slug)
		}
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(s.Root(), "global")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		t.Fatal(err)
	}
	if _, err := s.Write(WriteArgs{Slug: "one", Scope: "global", MemoryType: "user", Body: "fact"}); err == nil {
		t.Fatal("followed symlink")
	}
	files, _ := os.ReadDir(target)
	if len(files) != 0 {
		t.Fatal("external write")
	}
}
func TestStoreBatchDailyAndGroups(t *testing.T) {
	s := newStore(t)
	bullet := json.RawMessage(`{"localDate":"2026-09-28","dailyAppend":{"bullet":"done"},"decisions":[]}`)
	var a BatchArgs
	if err := json.Unmarshal(bullet, &a); err != nil {
		t.Fatal(err)
	}
	r, err := s.ApplyBatch(a)
	if err != nil || len(r.Warnings) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	got, err := s.Read(ReadArgs{Slug: "daily-2026-09-28", Scope: "global"})
	if err != nil || !strings.Contains(got.Body, "done") {
		t.Fatalf("%+v %v", got, err)
	}
	q, _ := s.QuotaSummary("")
	if q.Scopes[0].Used != 0 {
		t.Fatalf("daily counted %+v", q)
	}
}
