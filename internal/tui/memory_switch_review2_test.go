package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// runCfgSync drives one config poll through the real cfgSync/Update path.
func runCfgSync(t *testing.T, m *model) {
	t.Helper()
	m.cfgMod = time.Time{}
	m.prog = tea.NewProgram(nil)
	_, cmd := m.cfgSync()
	seq := reflect.ValueOf(cmd())
	if seq.Kind() != reflect.Slice || seq.Len() == 0 {
		t.Fatalf("cfgSync did not produce a sequence: %T", cmd())
	}
	msg := seq.Index(0).Interface().(tea.Cmd)()
	sync, ok := msg.(cfgSyncMsg)
	if !ok {
		t.Fatalf("first message = %T, want cfgSyncMsg", msg)
	}
	m.Update(sync)
}

// A "memory" key in config.json with no switch file is the legacy form Load still
// honours (hand edit, or a build that kept the switch in config.json). The TUI adopts
// it through cfgSync; its next unrelated save must move it to the switch file, not
// drop it: marshalConfig strips "memory" from config.json, so skipping the switch
// write silently turns memory back on everywhere.
func TestReviewCfgSyncLegacyMemoryOffSurvivesTUISave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.IndexByte(raw, '{')
	if i < 0 {
		t.Fatalf("fixture: no object in config.json: %s", raw)
	}
	raw = append(append(append([]byte{}, raw[:i+1]...), []byte(`"memory": {"enabled": false},`)...), raw[i+1:]...)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := config.Load(); err != nil || got.Memory.IsEnabled() {
		t.Fatalf("fixture: Load should read the legacy key as off (err=%v)", err)
	}

	runCfgSync(t, m)
	if m.agent.MemoryEnabled() {
		t.Fatal("fixture: cfgSync did not apply the legacy off value")
	}

	m.setEffort("high")

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.IsEnabled() {
		_, statErr := os.Stat(p + ".memory-switch.json")
		t.Fatalf("legacy memory off was lost by an unrelated TUI save (switch file stat: %v); the TUI itself still runs with memory off", statErr)
	}
}
