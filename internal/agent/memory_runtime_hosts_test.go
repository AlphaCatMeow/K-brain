package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func review3Home(t *testing.T, id string, install, session string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	if install != "" {
		if err := os.WriteFile(filepath.Join(home, "memory.md"), []byte(install), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if id == "" {
		return
	}
	dir := filepath.Join(home, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if session != "" {
		if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte(session), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func review3Reload(t *testing.T, a *Agent) []ai.Message {
	t.Helper()
	stored, err := json.Marshal(a.MessagesSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored []ai.Message
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	return restored
}

// Mixed history: the pre-runtime snapshot carried installation + session
// entries. After the runtime attaches, the end-to-end requests must keep the
// provider prefix, emit exactly one replacement saved-memory snapshot that
// keeps the session note, and not re-emit it on later turns or reloads.
func TestMixedSnapshotEndToEnd(t *testing.T) {
	const id = "sess-review-3"
	review3Home(t, id, "- [ ] prefers tabs\n", "- [ ] staging deploy uses port 8443\n")
	var requests []ai.Request
	server := textServer(t, func(_ int, r ai.Request) string { requests = append(requests, r); return "done" })
	defer server.Close()
	build := func(runtime bool) *Agent {
		a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable",
			WithSystemPromptResolver(func() (string, error) { return "stable", nil }))
		a.SetSessionID(id)
		if runtime {
			a.SetMemoryRuntime(&cacheMemoryFixture{block: "imported: prefers tabs"})
		}
		return a
	}
	a := build(false)
	if _, err := a.TurnAuthored(t.Context(), "t0", Events{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		history := review3Reload(t, a)
		a = build(true)
		a.RestoreMessages(history)
		if _, err := a.TurnAuthored(t.Context(), "t"+string(rune('0'+i)), Events{}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(requests); i++ {
		if !reflect.DeepEqual(requests[i-1].Messages, requests[i].Messages[:len(requests[i-1].Messages)]) {
			t.Fatalf("request %d rewrote the prefix", i+1)
		}
	}
	var memoryBlocks []string
	for _, m := range requests[len(requests)-1].Messages {
		if at := strings.Index(m.Content, "<memory>"); at >= 0 {
			memoryBlocks = append(memoryBlocks, m.Content[at:])
		}
	}
	t.Logf("saved-memory blocks the model sees, oldest first:\n%s", strings.Join(memoryBlocks, "\n----\n"))
	if len(memoryBlocks) != 2 {
		t.Fatalf("want old block + one replacement, got %d", len(memoryBlocks))
	}
	latest, _, _ := strings.Cut(memoryBlocks[1], "</memory>")
	if !strings.Contains(latest, "port 8443") || strings.Contains(latest, "prefers tabs") || strings.Contains(latest, "forget") {
		t.Fatalf("latest saved-memory block = %q", latest)
	}
}

// New backend session: no transcript yet, so memory.Session returns an empty
// Scope. Installation memory.md exists but was imported; nothing markdown-based
// should be injected and no saved-memory snapshot recorded.
func TestNewSessionWithoutTranscriptInjectsNothing(t *testing.T) {
	review3Home(t, "", "- [ ] prefers tabs\n", "")
	var requests []ai.Request
	server := textServer(t, func(_ int, r ai.Request) string { requests = append(requests, r); return "done" })
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.SetSessionID("sess-new-1")
	a.SetMemoryRuntime(&cacheMemoryFixture{})
	for _, in := range []string{"a", "b"} {
		if _, err := a.TurnAuthored(t.Context(), in, Events{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range a.MessagesSnapshot() {
		for _, s := range m.PromptSnapshots {
			if s.Source == savedMemorySource {
				t.Fatalf("new session recorded saved-memory snapshot %q", s.Text)
			}
		}
	}
	for _, m := range requests[len(requests)-1].Messages {
		if strings.Contains(m.Content, "prefers tabs") || strings.Contains(m.Content, "<memory>") {
			t.Fatalf("markdown memory leaked into request: %q", m.Content)
		}
	}
	for _, tool := range a.AllTools() {
		if n := tool.Def.Function.Name; n == "remember" || n == "forget" {
			t.Fatalf("tool %s still offered", n)
		}
	}
}

// Subagent of a runtime-backed parent: no markdown memory at all and no
// remember/forget tools (memoryDisabled short-circuits RefreshMemory).
func TestSubagentOfRuntimeParentHasNoMarkdownMemory(t *testing.T) {
	const id = "sess-review-sub"
	review3Home(t, id, "- [ ] prefers tabs\n", "- [ ] staging deploy uses port 8443\n")
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.SetSessionID(id)
	parent.SetMemoryRuntime(&cacheMemoryFixture{})
	sub := parent.newSub(SubModel{Client: parent.Client, Model: "m", MaxTokens: 100})
	sub.RefreshMemory()
	if sub.memoryBlock != "" || len(sub.contextSnapshots()) != 0 {
		t.Fatalf("subagent got markdown memory %q", sub.memoryBlock)
	}
	for _, tool := range sub.AllTools() {
		if n := tool.Def.Function.Name; n == "remember" || n == "forget" {
			t.Fatalf("subagent offered %s", n)
		}
	}
}

// Non-runtime hosts (TUI, ACP, kn print) keep the original prompt text byte for byte.
func TestNonRuntimePromptTextUnchanged(t *testing.T) {
	const id = "sess-review-tui"
	review3Home(t, id, "- [ ] prefers tabs\n", "- [ ] note\n")
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	a.SetSessionID(id)
	a.RefreshMemory()
	home := os.Getenv("LIVEAGENT_HOME")
	want := "\n\n<memory>\n\nSaved installation memory (" + filepath.Join(home, "memory.md") + " — edit or delete lines there directly; forget marks an entry done):\n- 1. prefers tabs" +
		"\n\nSaved session memory (" + filepath.Join(home, "sessions", id, "memory.md") + " — edit or delete lines there directly; forget marks an entry done):\n- 1. note\n</memory>"
	if a.memoryBlock != want {
		t.Fatalf("prompt text changed:\n got %q\nwant %q", a.memoryBlock, want)
	}
	names := map[string]bool{}
	for _, tool := range a.AllTools() {
		names[tool.Def.Function.Name] = true
	}
	if !names["remember"] || !names["forget"] {
		t.Fatal("non-runtime host lost remember/forget")
	}
}
