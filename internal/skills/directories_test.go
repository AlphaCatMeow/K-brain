package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirsForUsesRequestedProject(t *testing.T) {
	server, project := t.TempDir(), t.TempDir()
	t.Chdir(server)
	path := filepath.Join(project, ".agents", "skills", "project-only")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: project-only\ndescription: project-specific skill\n---\nUse this project.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dirs := DirsFor(project)
	if len(dirs) < 2 || dirs[0] != filepath.Join(project, ".liveagent", "skills") || dirs[1] != filepath.Join(project, ".agents", "skills") {
		t.Fatalf("expected requested project directories before global skills, got %v", dirs)
	}
	for _, skill := range Scan(dirs...) {
		if skill.Name == "project-only" {
			return
		}
	}
	t.Fatal("requested project skill not found")
}
