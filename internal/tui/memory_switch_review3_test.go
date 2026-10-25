package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// The TUI polls config.json while a turn is running (Update keeps running on the UI
// goroutine). LiveAgent switching memory off lands between the request that offered
// "remember" and the tool call the model made in reply. The agent documents the switch
// as taking effect "from the next turn on" and the backend applies it only at a run
// boundary; the TUI applies it at once, so the in-flight call fails as an unknown tool
// in a turn whose context never told the model memory was off.
func TestReviewCfgSyncMidTurnKeepsOfferedMemoryTool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	call := 0
	offered := false
	toolResult := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		call++
		n := call
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			for _, d := range req.Tools {
				if d.Function.Name == "remember" {
					offered = true
				}
			}
			// LiveAgent turns memory off while this response is streaming; the TUI's
			// next poll picks it up through the real cfgSync/Update path.
			other, err := config.Load()
			if err != nil {
				t.Error(err)
				return
			}
			off := false
			other.Memory.Enabled = &off
			if err := other.Save(); err != nil {
				t.Error(err)
				return
			}
			runCfgSync(t, m)
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"remember","arguments":"{\"text\":\"likes tea\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		for _, msg := range req.Messages {
			if msg.Role == "tool" && msg.ToolCallID == "t1" {
				toolResult = msg.Content
			}
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m.agent = agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys")

	if _, err := m.agent.Turn(context.Background(), "remember I like tea", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if !offered {
		t.Fatal("fixture: the first request did not offer remember")
	}
	if strings.Contains(toolResult, "unknown tool") {
		t.Fatalf("a memory tool offered in this turn's request failed after a mid-turn config poll: %q", toolResult)
	}
	if m.memoryPending == nil || *m.memoryPending {
		t.Fatal("the mid-turn poll did not hold LiveAgent's off switch for the turn boundary")
	}
	m.Update(turnDoneMsg{})
	if m.agent.MemoryEnabled() {
		t.Fatal("LiveAgent's off switch was not applied when the turn ended")
	}
	if m.memoryPending != nil {
		t.Fatal("the pending switch was not cleared after it was applied")
	}
}

// Legacy form ("memory" off in config.json, no switch file). LiveAgent loads it and turns
// memory on; a TUI that loaded the same legacy file saves an unrelated setting before its
// next poll. The PR's Save comment promises a stale copy "never undoes a switch another
// process set", but the TUI's copy still holds the unmigrated off value with a disk state
// of "on, no file", so needsWrite is true and the off switch file is written.
// Not a regression against 2d06b37 (Save always wrote the switch there).
func TestReviewLegacyMemoryOffStaleTUIUndoesLiveAgentOn(t *testing.T) {
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
	raw = append(append(append([]byte{}, raw[:i+1]...), []byte(`"memory": {"enabled": false},`)...), raw[i+1:]...)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The TUI holds the legacy value the way it does after startup Load or a poll.
	runCfgSync(t, m)
	if m.cfg.Memory.IsEnabled() {
		t.Fatal("fixture: TUI did not take the legacy off value")
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
	if got, err := config.Load(); err != nil || !got.Memory.IsEnabled() {
		t.Fatalf("fixture: LiveAgent's on did not persist (err=%v)", err)
	}

	m.setEffort("high")

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Memory.IsEnabled() {
		t.Fatal("an unrelated save from the TUI's stale legacy copy turned memory back off after LiveAgent turned it on")
	}
}
