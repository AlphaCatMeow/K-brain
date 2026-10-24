package memoryruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// After at least one wipe (WipeEpoch >= 1), a single failed read of the state
// file while a post-wipe message is already queued makes epoch() return 0,
// which Completed treats as "a wipe happened": the queued post-wipe message is
// discarded and the new queue is pinned to epoch 0, so its plan is dropped as
// well once the read recovers.
func TestEpochReadFailureDoesNotDropPostWipeQueue(t *testing.T) {
	var logMu sync.Mutex
	var logs []string
	oldLogf := logf
	logf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	t.Cleanup(func() { logf = oldLogf })

	client := &gatedSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	if _, err := store.WipeAll(); err != nil { // WipeEpoch = 1
		t.Fatal(err)
	}
	statePath := filepath.Join(store.Root(), ".kbrain-state.json")
	good, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: "记住：项目用 Go 1.24"}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "记住：测试用 -race"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	// u1 is said after the wipe and queued at epoch 1.
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u0, a, u1}})
	// One transient read failure of the state file while u2 completes.
	if err := os.WriteFile(statePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u0, a, u1, a, u2}})
	if err := os.WriteFile(statePath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	close(client.release)
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.requests) >= 2
	})
	settle(t, rt, "s")

	client.mu.Lock()
	var last strings.Builder
	if n := len(client.requests); n >= 2 {
		for _, m := range client.requests[n-1].Messages {
			last.WriteString(m.TextContent())
		}
	}
	client.mu.Unlock()
	logMu.Lock()
	dropped := false
	for _, l := range logs {
		if strings.Contains(l, "plan dropped") {
			dropped = true
		}
	}
	logMu.Unlock()
	judgedBlock := last.String()
	if i := strings.LastIndex(judgedBlock, "<latest-user-message>"); i >= 0 {
		judgedBlock = judgedBlock[i:]
	}
	if !strings.Contains(judgedBlock, u1.Content) {
		t.Errorf("post-wipe message u1 was discarded from the queue after one failed epoch read")
	}
	if dropped {
		t.Errorf("plan for post-wipe messages was dropped as if memory had been wiped (queue pinned to epoch 0)")
	}
}
