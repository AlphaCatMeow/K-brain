package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// The memory switch also governs the TUI, but the TUI cannot change it; the startup
// report is where a user learns why remember/forget are gone.
func TestStartupReportNotesMemoryOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())
	off := false
	on := true
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"off", &config.Config{Memory: config.MemoryConfig{Enabled: &off}}, true},
		{"on", &config.Config{Memory: config.MemoryConfig{Enabled: &on}}, false},
		{"default", &config.Config{}, false},
		{"no config", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tasksModel("http://unused")
			m.cfg = tc.cfg
			m.startupReport()
			out := reportText(m)
			if got := strings.Count(out, "memory is off"); got != map[bool]int{true: 1, false: 0}[tc.want] {
				t.Fatalf("memory-off notices = %d, want notice=%v:\n%s", got, tc.want, out)
			}
			if tc.want && !strings.Contains(out, "LiveAgent") {
				t.Errorf("notice should say where to turn memory back on:\n%s", out)
			}
		})
	}
}
