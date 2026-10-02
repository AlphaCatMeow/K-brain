package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fixtureSkill(t *testing.T, dir, name string) string {
	t.Helper()
	src := filepath.Join(dir, name)
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nname: %s\ndescription: Fixture skill\n---\n\nbody\n", name)
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestManagedSkillDirectoryZipAndProtection(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	src := fixtureSkill(t, t.TempDir(), "fixture-skill")
	if _, err = m.Install(context.Background(), src, "", "fail", nil, nil); err != nil {
		t.Fatal(err)
	}
	content, truncated, lines, err := m.Read("fixture-skill/SKILL.md", 0, 200)
	if err != nil || truncated || lines == 0 || content == "" {
		t.Fatalf("read: %q truncated=%v lines=%d err=%v", content, truncated, lines, err)
	}

	archive := filepath.Join(t.TempDir(), "fixture.skill")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("fixture-skill/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("---\nname: zipped-skill\ndescription: Zip\n---\n"))
	_ = z.Close()
	_ = f.Close()
	if _, err = m.Install(context.Background(), archive, "", "fail", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Delete("skills-creator"); err == nil {
		t.Fatal("builtin delete accepted")
	}
	if _, err = m.Install(context.Background(), src, "skills-installer", "overwrite", nil, nil); err == nil {
		t.Fatal("builtin overwrite accepted")
	}
}

func TestManagedRejectsZipTraversalAndSymlink(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "bad.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("bad"))
	_ = z.Close()
	_ = f.Close()
	if _, err = m.Install(context.Background(), archive, "", "fail", nil, nil); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(archive), "escape")); !os.IsNotExist(err) {
		t.Fatal("escape file created")
	}

	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: symlinked\ndescription: bad\n---\n"), 0600)
	if err = os.Symlink("/etc/passwd", filepath.Join(src, "reference.md")); err == nil {
		if _, err = m.Install(context.Background(), src, "", "fail", nil, nil); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestManagedHTTPFixtureInstallAndCancellation(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	payload := []byte("---\nname: http-skill\ndescription: HTTP fixture\n---\n\nbody\n")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer httpServer.Close()
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetHTTPClient(httpServer.Client()); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(context.Background(), httpServer.URL+"/SKILL.md", "http-skill", "fail", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(context.Background(), "http://user:password@127.0.0.1/SKILL.md", "bad", "fail", nil, nil); err == nil {
		t.Fatal("userinfo URL accepted")
	}
}
