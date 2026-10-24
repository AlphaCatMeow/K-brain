package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Backend sessions live under <LIVEAGENT_HOME>/sessions, so remember(scope=session)
// wrote notes to the memory.md beside the session transcript before this change.
// cmd/kn/backend.go imports only memory.Installation(); once the memory runtime is
// attached the resumed session is told its saved memory is "none", so the
// conversation's own session notes disappear without being imported anywhere.
func TestMemoryRuntimeKeepsResumedSessionScopedNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	const id = "sess-review-1"
	dir := filepath.Join(home, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("- [ ] staging deploy uses port 8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.SetSessionID(id)
	ag.RefreshMemory()
	if !strings.Contains(ag.memoryBlock, "port 8443") {
		t.Fatalf("precondition: session note not injected before runtime: %q", ag.memoryBlock)
	}
	// History persisted by the backend before this change carries the snapshot.
	ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "hi", PromptSnapshots: ag.contextSnapshots()})

	ag.SetMemoryRuntime(&cacheMemoryFixture{})
	ag.RefreshMemory()
	effective, _ := retainedSnapshot(ag.Messages, savedMemorySource)
	for _, s := range ag.contextSnapshots() {
		if s.Source == savedMemorySource {
			effective = s.Text
		}
	}
	if !strings.Contains(effective, "port 8443") {
		t.Fatalf("session-scoped note dropped from the resumed conversation; model now sees %q", effective)
	}
}
