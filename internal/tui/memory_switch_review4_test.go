package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// liveAgentTurnsMemoryOff saves the switch off from another config copy, as LiveAgent does.
func liveAgentTurnsMemoryOff(t *testing.T) {
	t.Helper()
	other, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	other.Memory.Enabled = &off
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
}

// /review sets m.busy (review.go) but ends with reviewMsg, never turnDoneMsg. A poll that
// lands while it runs parks LiveAgent's off switch in memoryPending, and nothing applies it
// once the review is done: the agent keeps memory on with no turn running.
func TestReviewMemoryPendingFromReviewAppliedWhenReviewEnds(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// With a program attached /review returns its work as a command instead of running it.
	m.prog = tea.NewProgram(nil)
	run := m.reviewCommand([]string{"HEAD~1"})
	if run == nil || !m.busy {
		t.Fatalf("fixture: /review did not start (busy=%v)", m.busy)
	}
	liveAgentTurnsMemoryOff(t)
	runCfgSync(t, m)
	m.prog = nil
	m.Update(run())
	if m.busy {
		t.Fatal("fixture: review did not end")
	}
	if m.agent.MemoryEnabled() {
		t.Fatalf("LiveAgent turned memory off during /review; after the review ended no turn is running but the agent still has memory on (pending=%v)", m.memoryPending != nil)
	}
}

// /goal-from-context sets m.busy while it formulates and then starts the goal turn
// itself. The off switch that arrived meanwhile is still pending, so the goal turn offers
// the model "remember" although LiveAgent turned memory off before the turn began.
func TestReviewMemoryPendingFromGoalFormulationLeaksIntoGoalTurn(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	tools := make(chan []string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		var names []string
		for _, d := range req.Tools {
			names = append(names, d.Function.Name)
		}
		tools <- names
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m.agent = agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys")
	m.agent.Messages = []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "we should fix the parser"},
		{Role: "assistant", Content: "agreed"},
	}

	m.startGoalFromContext([]string{"/goal-from-context"})
	if !m.busy || m.goalRequest == nil {
		t.Fatal("fixture: /goal-from-context did not start")
	}
	liveAgentTurnsMemoryOff(t)
	runCfgSync(t, m)
	m.prog = nil
	// The formulation request returns; finishGoalFromContext submits the goal turn.
	m.Update(goalFromContextMsg{request: m.goalRequest, goal: "fix the parser"})

	select {
	case names := <-tools:
		for _, n := range names {
			if n == "remember" {
				t.Fatalf("the goal turn started after LiveAgent turned memory off still offered remember (pending=%v, agent memory on=%v)",
					m.memoryPending != nil, m.agent.MemoryEnabled())
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture: goal turn sent no request")
	}
}
