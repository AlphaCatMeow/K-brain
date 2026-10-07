package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestLocalStoresSurviveRuntimeRestart(t *testing.T) {
	root := t.TempDir()
	data, err := OpenLocal(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	memoryPath, err := filepath.EvalSymlinks(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if data.Memory.Root() != memoryPath {
		t.Fatal("memory escaped data root")
	}
	data.Config.Language = "en"
	if err := data.SaveConfig(data.Config); err != nil {
		t.Fatal(err)
	}
	id, err := data.Sessions.Create(root, "removed-model", "removed-provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Sessions.Save(id, 0, []ai.Message{{Role: "user", Content: "persisted history"}}, "removed-model", "removed-provider"); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLocal(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Config.Language != "en" || len(reopened.Config.Providers) != 0 {
		t.Fatal("configuration changed during restart")
	}
	_, messages, err := reopened.Sessions.Load(id)
	if err != nil || len(messages) != 1 || messages[0].Content != "persisted history" {
		t.Fatalf("history lost: %v %v", messages, err)
	}
}

func TestLocalInstancesAreIsolated(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", filepath.Join(t.TempDir(), "unused-default"))
	first, err := OpenLocal(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenLocal(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	id, err := first.Sessions.Create(first.Root, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Sessions.Load(id); err == nil {
		t.Fatal("session leaked between data roots")
	}
	if _, err := os.Stat(os.Getenv("LIVEAGENT_HOME")); !os.IsNotExist(err) {
		t.Fatalf("explicit root touched default data: %v", err)
	}
}

func TestLocalConfigInitializationDoesNotReplaceExistingData(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	content := []byte("{\"language\":\"en\"}\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	data, err := OpenLocal(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Fatal("existing configuration was rewritten")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("configuration permissions were not restricted")
		}
	}
}

func TestLocalInvalidConfigIsNotReplaced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocal(Options{Root: root}); err == nil {
		t.Fatal("accepted broken configuration")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "broken" {
		t.Fatal("replaced broken configuration without user action")
	}
}

func TestLocalRejectsConfigSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege is not guaranteed")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(target, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocal(Options{Root: root}); err == nil {
		t.Fatal("followed configuration symlink")
	}
}

func TestDesktopMigrationPreservesDataAndPreviousMarkers(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "import", true: "already-imported"}[completed], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			root, legacy := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"language":"en"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacy, "deleted.txt"), []byte("legacy"), 0600); err != nil {
				t.Fatal(err)
			}
			if completed {
				if err := os.WriteFile(filepath.Join(root, ".migration-desktop-kbrain"), []byte("complete\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := OpenLocal(Options{Root: root, LegacyDesktopDir: legacy})
			if err != nil {
				t.Fatal(err)
			}
			data.Close()
			if completed {
				if _, err := os.Stat(filepath.Join(root, "deleted.txt")); !os.IsNotExist(err) {
					t.Fatal("resurrected deleted data after old migration")
				}
			} else {
				if data.Config.Language != "en" {
					t.Fatal("failed to migrate configuration")
				}
				if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
					t.Fatal(err)
				}
				again, err := OpenLocal(Options{Root: root, LegacyDesktopDir: legacy})
				if err != nil {
					t.Fatal(err)
				}
				again.Close()
				if _, err := os.Stat(filepath.Join(root, "deleted.txt")); !os.IsNotExist(err) {
					t.Fatal("replayed completed migration")
				}
			}
		})
	}
}
