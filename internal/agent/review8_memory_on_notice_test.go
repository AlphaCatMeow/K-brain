package agent

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// review8Turn mirrors the per-turn preparation in Run: RefreshMemory, then (when a
// memory runtime is attached and memory is on) installRuntimeMemory, then the user
// message carries contextSnapshots. It returns everything the model sees in History
// after the off turn, so the test can tell whether the off instruction was lifted.
func review8Turn(a *Agent, runtimeIndex string) string {
	a.RefreshMemory()
	if runtimeIndex != "" && a.MemoryEnabled() {
		a.installRuntimeMemory(runtimeIndex)
	}
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "hi", PromptSnapshots: a.contextSnapshots()})
	return promptContextText(a.Messages[len(a.Messages)-1])
}

// TUI / kn run (no memory runtime) with an existing saved memory entry: the off
// turn puts memoryOffNotice on the saved-memory source. Turning memory back on
// re-injects the saved block on that source, so the new lift loop skips it and the
// "Do not save memories ... tell the user memory is turned off" instruction is never
// revoked, while remember/forget come back.
func TestReview8MemoryOnAgainLiftsOffInstructionWithSavedMemory(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	if err := memory.Installation().Remember("user prefers tabs"); err != nil {
		t.Fatal(err)
	}
	a := New(nil, "fixture", 100, "system")
	if got := review8Turn(a, ""); !strings.Contains(got, "user prefers tabs") {
		t.Fatalf("fixture: saved memory not injected: %q", got)
	}
	a.SetMemoryEnabled(false)
	if got := review8Turn(a, ""); !strings.Contains(got, "Memory is turned off") {
		t.Fatalf("off turn lacks the off instruction: %q", got)
	}
	a.SetMemoryEnabled(true)
	got := review8Turn(a, "")
	if !strings.Contains(got, "user prefers tabs") {
		t.Fatalf("re-enabled turn must re-inject saved memory: %q", got)
	}
	if !strings.Contains(got, "Memory is turned on again") {
		t.Fatalf("re-enabled turn re-injects saved memory but never lifts the off instruction; History still ends with %q", memoryOffNotice)
	}
}

// Backend session with a memory runtime (LiveAgent desktop): saved-memory is the
// read-only session block (empty here), so the off notice goes onto the runtime
// index source. Re-enabling re-installs the index on that source; again nothing
// lifts the off instruction.
func TestReview8MemoryOnAgainLiftsOffInstructionWithRuntimeIndex(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	a := New(nil, "fixture", 100, "system")
	a.markdownMemoryReadOnly = true
	const index = "User:\n- tabs [user/preference] prefers tabs"
	if got := review8Turn(a, index); !strings.Contains(got, "Current memory index") {
		t.Fatalf("fixture: runtime index not injected: %q", got)
	}
	a.SetMemoryEnabled(false)
	if got := review8Turn(a, index); !strings.Contains(got, "Memory is turned off") {
		t.Fatalf("off turn lacks the off instruction: %q", got)
	}
	a.SetMemoryEnabled(true)
	got := review8Turn(a, index)
	if !strings.Contains(got, "Current memory index") {
		t.Fatalf("re-enabled turn must re-inject the index: %q", got)
	}
	if strings.Count(got, "Memory is turned on again") != 1 {
		t.Fatalf("re-enabled turn must lift the off instruction exactly once; got %d in %q", strings.Count(got, "Memory is turned on again"), got)
	}
}
