package agent

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// A session resumed under the backend memory runtime carries the memory.md
// snapshot injected before this change. With memoryDisabled the saved-memory
// source is skipped entirely, so the "no longer applies" marker is never sent
// and the model keeps the stale <memory> block (which also tells it to use
// forget, now unavailable).
func TestMemoryRuntimeClearsResumedSavedMemorySnapshot(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	stale := "\n\n<memory>\n\nSaved installation memory (memory.md — edit or delete lines there directly; forget marks an entry done):\n- 1. prefers tabs\n</memory>"
	ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "hi", PromptSnapshots: []ai.PromptSnapshot{{Source: savedMemorySource, Text: stale}}})
	ag.SetMemoryRuntime(&cacheMemoryFixture{})
	cleared := false
	for _, s := range ag.contextSnapshots() {
		if s.Source == savedMemorySource && strings.Contains(s.Text, "no longer apply") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("resumed saved-memory snapshot is never cleared once the memory runtime replaces markdown memory")
	}
}
