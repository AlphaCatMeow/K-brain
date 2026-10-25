package agent

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Without a memory runtime (TUI, kn run) nothing re-injects memory when it is turned
// back on, so the last memory instruction in History would still say "off".
func TestMemoryOnAgainLiftsOffInstructionWithoutRuntime(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	a := New(nil, "fixture", 100, "system")
	turn := func() string {
		snapshots := a.contextSnapshots()
		a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "hi", PromptSnapshots: snapshots})
		return promptContextText(a.Messages[len(a.Messages)-1])
	}
	a.SetMemoryEnabled(false)
	if got := turn(); !strings.Contains(got, "Memory is turned off") {
		t.Fatalf("off turn lacks the off instruction: %q", got)
	}
	a.SetMemoryEnabled(true)
	if got := turn(); !strings.Contains(got, "Memory is turned on again") {
		t.Fatalf("re-enabled turn must lift the off instruction: %q", got)
	}
	if got := turn(); strings.Contains(got, "Memory is turned") {
		t.Fatalf("the on instruction is sent once, not every turn: %q", got)
	}
	a.SetMemoryEnabled(false)
	if got := turn(); !strings.Contains(got, "Memory is turned off") {
		t.Fatalf("turning memory off again must re-send the off instruction: %q", got)
	}
}
