package memoryruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// corruptState makes the next state reads fail and returns a restore func.
func corruptState(t *testing.T, store *memory.Store) func() {
	t.Helper()
	path := filepath.Join(store.Root(), ".kbrain-state.json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.WriteFile(path, good, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// An extraction started while the epoch could not be read carries a nil
// epoch for its whole run. A WipeAll during that run (the model call, up to
// minutes with retries) is not seen at write-back: wipedSince(nil, 2) is
// false, so the pre-wipe message's plan is written into the wiped memory.
// HEAD~1 compared 0 with 2 and dropped it.
func TestNilStartEpochAppliesPlanAfterWipe(t *testing.T) {
	client := &gatedSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	if _, err := store.WipeAll(); err != nil { // epoch 1, state file exists
		t.Fatal(err)
	}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	restore := corruptState(t, store)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	restore()
	// The user wipes memory while u0 is being judged.
	if _, err := store.WipeAll(); err != nil { // epoch 2
		t.Fatal(err)
	}
	close(client.release)
	settle(t, rt, "s")
	list, err := store.List(memory.ListArgs{IncludeAllProjects: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 0 {
		t.Fatalf("plan for pre-wipe message u0 was written after WipeAll (entries=%d)", len(list.Entries))
	}
}

// A queue opened while the epoch could not be read is later back-filled with
// the current epoch. A WipeAll between the two Completed calls is invisible:
// the pre-wipe message u1 stays queued and is judged after the wipe.
func TestNilQueueEpochBackfillKeepsPreWipeMessage(t *testing.T) {
	client := &gatedSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	a := ai.Message{Role: "assistant", Content: "ok"}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	u1 := ai.Message{ID: "u1", Role: "user", Content: "记住：项目代号是 Falcon"}
	u2 := ai.Message{ID: "u2", Role: "user", Content: "记住：测试用 -race"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	restore := corruptState(t, store)
	// u1 completes during one failed read: the queue opens with a nil epoch.
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u0, a, u1}})
	restore()
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	// u2 is said after the wipe; the queue's nil epoch is back-filled.
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u2, History: []ai.Message{u0, a, u1, a, u2}})
	close(client.release)
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.requests) >= 2
	})
	settle(t, rt, "s")
	client.mu.Lock()
	var block string
	if n := len(client.requests); n >= 2 {
		block = client.requests[n-1].Messages[1].TextContent()
	}
	client.mu.Unlock()
	if i := strings.LastIndex(block, "<latest-user-message>"); i >= 0 {
		block = block[i:]
	}
	if strings.Contains(block, u1.Content) {
		t.Fatalf("pre-wipe message u1 was judged after WipeAll: %q", block)
	}
}

// Close keeps the running extraction's messages with their nil epoch;
// HoldUnjudged then skips the wipe check and persists pre-wipe u0 into the
// state WipeAll just cleared.
func TestCloseHoldsPreWipeMessageWithNilEpoch(t *testing.T) {
	client := &gatedSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	client.submit = true
	rt, store := testRuntime(t, client)
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	restore := corruptState(t, store)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	restore()
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	_ = rt.Close()
	close(client.release)
	held, err := store.TakeUnjudged("s")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range held {
		if h.ID == u0.ID {
			t.Fatalf("pre-wipe message u0 was held across WipeAll by Close: %+v", held)
		}
	}
}

// A failed extraction holds its messages with the start epoch; when that is
// nil a WipeAll during the run is not seen and pre-wipe u0 is held again.
func TestFailedExtractionHoldsPreWipeMessageWithNilEpoch(t *testing.T) {
	client := &gatedThenSubmitClient{started: make(chan struct{}), release: make(chan struct{})}
	rt, store := testRuntime(t, client)
	t.Cleanup(func() { _ = rt.Close() })
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	u0 := ai.Message{ID: "u0", Role: "user", Content: "记住：我喜欢简洁的回答"}
	restore := corruptState(t, store)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u0, History: []ai.Message{u0}})
	<-client.started
	restore()
	if _, err := store.WipeAll(); err != nil {
		t.Fatal(err)
	}
	close(client.release) // permanent 400: not judged, held for the next turn
	settle(t, rt, "s")
	held, err := store.TakeUnjudged("s")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range held {
		if h.ID == u0.ID {
			t.Fatalf("pre-wipe message u0 was held across WipeAll after a failed extraction: %+v", held)
		}
	}
}
