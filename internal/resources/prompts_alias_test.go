package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPromptPreservesClientPathAlias(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := OpenPrompts(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.SetProject(ProjectPrompt{Workdir: alias, Prompt: "alias prompt", Strategy: "append"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range state.Projects {
		if item.SourceWorkdir != alias || item.Workdir == alias {
			t.Fatalf("alias lost: %+v", item)
		}
	}
	reopened, err := OpenPrompts(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, effective, err := reopened.Resolve(alias, nil)
	if err != nil || effective != "alias prompt" {
		t.Fatalf("effective=%q err=%v", effective, err)
	}
}
