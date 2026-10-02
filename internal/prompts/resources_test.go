package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

func TestWithResourcesChangesProviderSystemInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", "")
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := resources.CanonicalWorkdir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Trust(canonical); err != nil {
		t.Fatal(err)
	}
	store, err := resources.OpenPrompts(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(resources.AgentTemplate{ID: "review", Name: "Review", Prompt: "review mode", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	assembled, err := WithResources("base system", project, store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(assembled, "review mode") {
		t.Fatalf("assembled upstream input = %q", assembled)
	}
	if _, err = store.SetProject(resources.ProjectPrompt{Workdir: project, Prompt: "project policy", Strategy: "replace"}, nil); err != nil {
		t.Fatal(err)
	}
	assembled, err = WithResources("base system", project, store)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(assembled, "review mode") || !strings.Contains(assembled, "project policy") || !strings.Contains(assembled, "base system") {
		t.Fatalf("replace upstream input = %q", assembled)
	}
	if _, err = store.SetProject(resources.ProjectPrompt{Workdir: project, Prompt: " \n\t", Strategy: "replace"}, nil); err != nil {
		t.Fatal(err)
	}
	assembled, err = WithResources("base system", project, store)
	if err != nil || !strings.Contains(assembled, "review mode") || strings.Contains(assembled, "Project agent prompt") {
		t.Fatalf("blank replace must use global prompt: %q, %v", assembled, err)
	}
}
