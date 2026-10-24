package memoryruntime

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// Completed and startPending write c.inflight after releasing r.mu, while
// Close reads it under r.mu from another goroutine (backend model switch,
// idle reap or shutdown runs Close on its own goroutine). Run with -race: the
// detector reports the unsynchronised write. When Close wins the window it
// also reads nil, cancels the extraction, and holdUnlessClosed then skips the
// re-hold, so the message is never judged.
func TestCloseRacingCompletedSeesInflight(t *testing.T) {
	remember := "记住：以后提交信息一律用英文写"
	for i := 0; i < 20; i++ {
		client := &journalClient{dailyText: remember, bullet: "commit-in-english", block: true, started: make(chan struct{})}
		store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
		if err != nil {
			t.Fatal(err)
		}
		rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: time.Millisecond, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		u1 := ai.Message{ID: "u1", Role: "user", Content: remember}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { // the run goroutine finishing a turn
			defer wg.Done()
			rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: u1, History: []ai.Message{u1}})
		}()
		go func() { // the backend closing the runtime as soon as the claim is running
			defer wg.Done()
			for {
				rt.mu.Lock()
				c := rt.claims["s"]
				running := c != nil && c.cancel != nil
				rt.mu.Unlock()
				if running {
					break
				}
				time.Sleep(time.Microsecond)
			}
			_ = rt.Close()
		}()
		wg.Wait()
		settle(t, rt, "s")
		held, err := store.TakeUnjudged("s")
		if err != nil {
			t.Fatal(err)
		}
		if len(held) != 1 {
			t.Fatalf("iteration %d: Close did not hold the running extraction's message; held = %v", i, held)
		}
	}
}
