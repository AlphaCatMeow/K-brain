package memoryruntime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func TestCompletedCoalescesWithinThrottle(t *testing.T) {
	client := &fixtureClient{submit: true}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	turn := agent.MemoryTurn{SessionID: "s", User: ai.Message{ID: "1", Role: "user", Content: "I prefer concise answers"}, History: []ai.Message{{Role: "user", Content: "I prefer concise answers"}}}
	rt.Completed(context.Background(), turn)
	turn.User.ID = "2"
	turn.User.Content = "I prefer markdown answers"
	rt.Completed(context.Background(), turn)
	time.Sleep(25 * time.Millisecond)
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("initial extraction calls=%d", calls)
	}
	time.Sleep(250 * time.Millisecond)
	client.mu.Lock()
	calls = client.calls
	client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("coalesced extraction calls=%d", calls)
	}
}

func TestPlanValidationRejectsUnknownAction(t *testing.T) {
	if err := validateDecision(memory.Decision{Op: "rewrite", Slug: "x"}); err == nil {
		t.Fatal("unknown plan action accepted")
	}
}
