package approval

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Polling a process this run started has no effect of its own, and the start was reviewed
// and approved. Reviewing the poll only sees an opaque id, so a real reviewer rated it
// "unknown" and prompted for every ProcessWait. Such polls skip the action review; their
// output is still reviewed, and every other call (or a tainted run) is reviewed as before.
// Ownership comes from the tools reporting what they started, through the real catalog.
func TestPollingOwnProcessSkipsActionReview(t *testing.T) {
	root := t.TempDir()
	base := tools.WithRunIdentity(tools.WithWorkspaceRoots(tools.WithWorkingDir(context.Background(), root), []tools.WorkspaceRoot{{Path: root, Access: "write"}}), tools.RunIdentity{ConversationID: "conv", RunID: "run"})
	ctx, cancel := context.WithTimeout(base, 60*time.Second)
	defer cancel()
	terminals := tools.NewTerminalManager()
	defer terminals.CloseAll()
	processes := tools.NewManagedProcessManager()
	defer processes.CloseAll()
	catalog := tools.LiveAgentCatalogWithManagers(nil, terminals, processes)

	var mu sync.Mutex
	actionReviews, outputReviews := 0, 0
	verdict := low
	reviewer := &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		var e Evidence
		_ = json.Unmarshal([]byte(req.Messages[1].Content), &e)
		mu.Lock()
		defer mu.Unlock()
		if e.Phase == "action" {
			actionReviews++
		} else {
			outputReviews++
		}
		return verdict, ai.Usage{}, nil
	}}
	run := NewRun("auto", "run the build", reviewer, func(string, string, Report, bool) {})
	gates := 0
	hooked := tools.WithReview(ctx, run.Hooks(func(tools.GateRequest) (tools.GateDecision, string) {
		mu.Lock()
		gates++
		mu.Unlock()
		return tools.GateAllowOnce, ""
	}))
	call := func(tool string, args map[string]any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		result := tools.ExecuteResult(hooked, catalog, tool, raw, false)
		if result.Failed {
			t.Fatalf("%s %s failed: %s", tool, raw, result.Text)
		}
		return result
	}
	id := func(pattern, text string) string {
		t.Helper()
		match := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if match == nil {
			t.Fatalf("no id in %q", text)
		}
		return match[1]
	}
	bash := id(`\ABash session_id=(\S+)\n`, call("Bash", map[string]any{"command": "echo built", "yield_time_ms": 500}).Text)
	proc := id(`process_id=(\S+)`, call("ManagedProcess", map[string]any{"action": "start", "command": "echo served"}).Text)
	term := id(`session_id=(\S+)`, call("TerminalSession", map[string]any{"command": "echo shell"}).Text)

	reviewedBy := func(tool string, args map[string]any) bool {
		t.Helper()
		mu.Lock()
		before := actionReviews
		mu.Unlock()
		raw, _ := json.Marshal(args)
		tools.ExecuteResult(hooked, catalog, tool, raw, false)
		mu.Lock()
		defer mu.Unlock()
		return actionReviews != before
	}
	for _, own := range []struct {
		tool string
		args map[string]any
	}{
		{"ProcessWait", map[string]any{"session_id": bash, "yield_time_ms": 100}},
		{"ProcessWait", map[string]any{"process_id": proc, "yield_time_ms": 100}},
		{"ManagedProcess", map[string]any{"action": "wait", "process_id": proc, "yield_time_ms": 100}},
		{"ManagedProcess", map[string]any{"action": "status", "process_id": proc}},
		{"ManagedProcess", map[string]any{"action": "read_log", "process_id": proc}},
		{"ReadTerminal", map[string]any{"session_id": term}},
	} {
		if reviewedBy(own.tool, own.args) {
			t.Errorf("%s %v polls this run's process but was reviewed", own.tool, own.args)
		}
	}
	for _, other := range []struct {
		tool string
		args map[string]any
	}{
		{"ProcessWait", map[string]any{"session_id": "bash-not-started-here"}},
		{"ManagedProcess", map[string]any{"action": "status"}}, // lists other runs' processes too
		{"ProcessStop", map[string]any{"session_id": bash}},    // has an effect
	} {
		if !reviewedBy(other.tool, other.args) {
			t.Errorf("%s %v was not reviewed", other.tool, other.args)
		}
	}
	mu.Lock()
	if outputReviews < 9 || gates == 0 {
		t.Errorf("outputs reviewed %d times, gates %d: skipped polls must keep both", outputReviews, gates)
	}
	// Once any output needed human review, polls are reviewed again.
	verdict = strings.Replace(low, `"injection":"low"`, `"injection":"high"`, 1)
	mu.Unlock()
	call("ReadTerminal", map[string]any{"session_id": term})
	mu.Lock()
	verdict = low
	mu.Unlock()
	if !reviewedBy("ReadTerminal", map[string]any{"session_id": term}) {
		t.Fatal("tainted run skipped the review of a poll")
	}
}
