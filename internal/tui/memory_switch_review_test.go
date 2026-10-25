package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// LiveAgent turns memory off while a TUI is open; the TUI then saves its own
// (stale) copy of the config for an unrelated setting.
func TestReviewTUISaveKeepsLiveAgentMemoryOff(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}

	other, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	other.Memory.Enabled = &off
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}

	m.setEffort("high")

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.IsEnabled() {
		t.Fatal("an unrelated TUI save turned memory back on after LiveAgent switched it off")
	}
}

// The running TUI already follows config.json for theme and language; the memory
// switch saved by LiveAgent must reach m.cfg and the agent too.
func TestReviewConfigSyncAppliesMemorySwitch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	m.cfgMod = time.Time{}
	m.prog = tea.NewProgram(nil)

	other, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	other.Memory.Enabled = &off
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json.memory-switch.json")); err != nil {
		t.Fatalf("switch file not written: %v", err)
	}

	_, cmd := m.cfgSync()
	seq := reflect.ValueOf(cmd())
	if seq.Kind() != reflect.Slice || seq.Len() == 0 {
		t.Fatalf("cfgSync did not produce a sequence: %T", cmd())
	}
	first, ok := seq.Index(0).Interface().(tea.Cmd)
	if !ok {
		t.Fatalf("sequence element = %T, want tea.Cmd", seq.Index(0).Interface())
	}
	msg := first()
	sync, isSync := msg.(cfgSyncMsg)
	if !isSync {
		t.Fatalf("first message = %T, want cfgSyncMsg", msg)
	}
	m.Update(sync)

	if m.cfg.Memory.IsEnabled() {
		t.Error("m.cfg still has memory on after LiveAgent switched it off")
	}
	if m.agent.MemoryEnabled() {
		t.Error("the running agent still injects memory and offers memory tools after LiveAgent switched it off")
	}
}

// The notice lists what the switch hides; every tool the agent drops while off
// must be named, or users will look for memory_manager.
func TestReviewMemoryOffNoticeNamesEveryHiddenTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())
	off := false
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Memory: config.MemoryConfig{Enabled: &off}}
	m.agent.SetMemoryEnabled(false)
	m.startupReport()
	out := reportText(m)
	var hidden []string
	for _, tool := range m.agent.KnownTools() {
		if tool.Memory {
			hidden = append(hidden, tool.Def.Function.Name)
		}
	}
	if len(hidden) == 0 {
		t.Fatal("no memory tools registered; fixture broken")
	}
	for _, name := range hidden {
		if !strings.Contains(out, name) {
			t.Errorf("notice does not mention hidden tool %q:\n%s", name, out)
		}
	}
}

// /memory is the TUI's other memory surface; while off it must not claim the
// entries are injected into every turn.
func TestReviewMemoryCommandWhileOff(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	off := false
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Memory: config.MemoryConfig{Enabled: &off}}
	m.agent.SetMemoryEnabled(false)
	m.memoryCommand(nil)
	out := reportText(m)
	if strings.Contains(out, "injected into every turn") {
		t.Errorf("/memory claims injection while memory is off:\n%s", out)
	}
}

// The other direction: the TUI started with memory off, LiveAgent turned it back on,
// and a stale TUI save must not write the off switch again.
func TestTUISaveKeepsLiveAgentMemoryOn(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	off := false
	m.cfg.Memory.Enabled = &off
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}

	other, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	on := true
	other.Memory.Enabled = &on
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}

	m.setEffort("high")

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Memory.IsEnabled() {
		t.Fatal("an unrelated TUI save turned memory off again after LiveAgent switched it on")
	}
}
