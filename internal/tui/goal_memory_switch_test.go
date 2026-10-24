package tui

import (
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSetGoalRecordsMemorySwitch(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st
	if m.sessionID, err = st.Create(t.TempDir(), m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.cancelGoalFromContext)
	goalMemoryOff := func() bool {
		t.Helper()
		meta, _, err := st.Load(m.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		return meta.GoalMemoryOff
	}

	off := false
	m.cfg.Memory.Enabled = &off
	m.setGoal("written while memory is off")
	if !goalMemoryOff() {
		t.Fatal("a goal set while memory is off must be recorded as such")
	}
	m.cfg.Memory.Enabled = nil
	m.setGoal("written while memory is on")
	if goalMemoryOff() {
		t.Fatal("a new goal set while memory is on must not inherit the off mark")
	}
}
