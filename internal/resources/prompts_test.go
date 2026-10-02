package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestPromptStoreTemplateSwitchAndProjectStrategy(t *testing.T) {
	home := t.TempDir()
	old := os.Getenv("K_BRAIN_HOME")
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", "")
	t.Cleanup(func() { _ = os.Setenv("K_BRAIN_HOME", old) })
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalWorkdir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Trust(canonical); err != nil {
		t.Fatal(err)
	}
	if !config.Trusted(canonical) {
		t.Fatal("project trust did not round-trip")
	}
	store, err := OpenPrompts(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(AgentTemplate{ID: "one", Name: "One", Prompt: "global one", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(AgentTemplate{ID: "two", Name: "Two", Prompt: "global two", Enabled: false}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, effective, err := store.Resolve(project, nil)
	if err != nil || effective != "global one" {
		t.Fatalf("initial effective = %q, err=%v", effective, err)
	}
	if _, err = store.Patch("two", TemplatePatch{Enabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	_, _, effective, err = store.Resolve(project, nil)
	if err != nil || effective != "global two" {
		t.Fatalf("switched effective = %q, err=%v", effective, err)
	}
	if _, err = store.SetProject(ProjectPrompt{Workdir: project, Prompt: "project", Strategy: "append"}, nil); err != nil {
		t.Fatal(err)
	}
	global, _, effective, err := store.Resolve(project, nil)
	if err != nil || global != "global two" || effective != "global two\n\nproject" {
		t.Fatalf("append effective = %q, err=%v", effective, err)
	}
	if _, err = store.SetProject(ProjectPrompt{Workdir: project, Prompt: "project", Strategy: "replace"}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, effective, err = store.Resolve(project, nil)
	if err != nil || effective != "project" {
		t.Fatalf("replace effective = %q, err=%v", effective, err)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(home, "agents.json"))), "global two") {
		t.Fatal("template switch was not persisted")
	}
}

func TestProjectPromptSaveNeedsNoFilesystemTrustAndSurvivesMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", "")
	project := t.TempDir()
	canonical, err := CanonicalWorkdir(project)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenPrompts(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetProject(ProjectPrompt{Workdir: project, Prompt: "user settings", Strategy: "append"}, nil); err != nil {
		t.Fatal(err)
	}
	if config.Trusted(canonical) {
		t.Fatal("saving user settings granted filesystem trust")
	}
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenPrompts(home)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Snapshot().Projects[config.ProjectID(canonical)].Prompt != "user settings" {
		t.Fatal("stored project prompt was lost")
	}
}

func TestPromptMutationWritesOnlyItsAtomicDomain(t *testing.T) {
	root := t.TempDir()
	store, err := OpenPrompts(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "project-prompts.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(AgentTemplate{ID: "one", Name: "One", Enabled: true}, nil); err != nil {
		t.Fatalf("global write depended on project file: %v", err)
	}
	before := store.Snapshot()
	if _, err := store.SetProject(ProjectPrompt{Workdir: t.TempDir(), Prompt: "failed save", Strategy: "append"}, nil); err == nil {
		t.Fatal("project write unexpectedly replaced a directory")
	}
	if after := store.Snapshot(); after.Revision != before.Revision || len(after.Projects) != 0 {
		t.Fatalf("failed mutation committed: %+v", after)
	}
}

func boolPtr(value bool) *bool { return &value }
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
