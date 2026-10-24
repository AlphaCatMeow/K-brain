package memoryruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// busyThenPlanClient answers like a provider that is busy for a while: the
// first failures calls return a stream error, then it submits a plan.
type busyThenPlanClient struct {
	fixtureClient
	mu       sync.Mutex
	failures int
	calls    int
}

func (c *busyThenPlanClient) Clone() ai.Client { return c }

func (c *busyThenPlanClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	busy := c.calls <= c.failures
	c.mu.Unlock()
	if busy {
		return ai.Message{}, ai.Usage{}, errors.New("API stream error: server_busy: Our servers are currently busy.")
	}
	onCall("1", "SubmitMemoryPlan", `{"decisions":[{"action":"write","slug":"user-shell","scope":"global","type":"user","description":"Uses Git Bash","body":"Give shell commands for bash."}]}`)
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

func fastExtractionRetries(t *testing.T) {
	t.Helper()
	previous := extractionRetryDelays
	extractionRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { extractionRetryDelays = previous })
}

// A provider that answers server_busy for a few requests must not cost the
// turn's memory: the extractor keeps trying before it gives up.
func TestExtractionRetriesBusyProvider(t *testing.T) {
	fastExtractionRetries(t)
	client := &busyThenPlanClient{failures: 3}
	rt, store := testRuntime(t, client)
	rt.Completed(context.Background(), extractionTurn("我用的是 Windows + Git Bash，给命令的时候都按 bash 写。"))
	if !waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "user-shell", Scope: "global"})
		return err == nil
	}) {
		client.mu.Lock()
		calls := client.calls
		client.mu.Unlock()
		t.Fatalf("extractor gave up on a busy provider after %d calls", calls)
	}
}

// Hosts drop the backend's stderr, so extraction failures must reach the
// k-brain.log diagnostics file.
func TestExtractionFailureReachesDiagnosticsLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	fastExtractionRetries(t)
	client := &busyThenPlanClient{failures: 100}
	rt, _ := testRuntime(t, client)
	rt.Completed(context.Background(), extractionTurn("I prefer pnpm"))
	path := filepath.Join(home, "k-brain.log")
	if !waitFor(t, func() bool {
		data, _ := os.ReadFile(path)
		return strings.Contains(string(data), "memory extraction")
	}) {
		t.Fatal("extraction failure was not written to k-brain.log")
	}
}

// A rejected key or exhausted quota does not recover by waiting.
func TestExtractionDoesNotRetryPermanentModelErrors(t *testing.T) {
	fastExtractionRetries(t)
	client := &countingErrorClient{err: &ai.HTTPError{Status: "403 Forbidden", StatusCode: 403, Body: "quota exhausted"}}
	rt, _ := testRuntime(t, client)
	rt.Completed(context.Background(), extractionTurn("I prefer pnpm"))
	time.Sleep(100 * time.Millisecond)
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("permanent model error was retried: %d calls", calls)
	}
}

type countingErrorClient struct {
	fixtureClient
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingErrorClient) Clone() ai.Client { return c }

func (c *countingErrorClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return ai.Message{}, ai.Usage{}, c.err
}
