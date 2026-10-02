package memoryruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

type fixtureClient struct {
	mu     sync.Mutex
	calls  int
	submit bool
}

func (c *fixtureClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *fixtureClient) Clone() ai.Client                               { return c }
func (c *fixtureClient) SetCacheKey(string)                             {}
func (c *fixtureClient) Endpoint() string                               { return "fixture" }
func (c *fixtureClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *fixtureClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	submit := c.submit
	c.mu.Unlock()
	if submit {
		b, _ := json.Marshal(map[string]any{"decisions": []any{map[string]any{"op": "upsert", "slug": "preference", "scope": "global", "memoryType": "user", "description": "Preference", "body": "Use concise answers", "evidence": map[string]any{"confidence": "high", "sourceQuote": "Use concise"}}}})
		onCall("1", "SubmitMemoryPlan", string(b))
	}
	return ai.Message{Role: "assistant", Content: "ok"}, ai.Usage{}, nil
}

func testRuntime(t *testing.T, client ai.Client) (*Runtime, *memory.Store) {
	t.Helper()
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: time.Millisecond, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return rt, store
}
func TestInjectUsesCanonicalOverview(t *testing.T) {
	rt, store := testRuntime(t, &fixtureClient{})
	desc, body := "User preference", "Use concise answers"
	if _, err := store.Write(memory.WriteArgs{Slug: "preference", Scope: "global", MemoryType: "user", Description: desc, Body: body}); err != nil {
		t.Fatal(err)
	}
	got, err := rt.Inject(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !contains(got, "preference") {
		t.Fatalf("injection=%q", got)
	}
}
func TestCompletedUsesRetryAndAtomicBatch(t *testing.T) {
	client := &fixtureClient{submit: true}
	rt, store := testRuntime(t, client)
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: ai.Message{ID: "u1", Role: "user", Content: "I prefer concise answers"}, History: []ai.Message{{Role: "user", Content: "I prefer concise answers"}}})
	time.Sleep(30 * time.Millisecond)
	got, err := store.Read(memory.ReadArgs{Slug: "preference", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Use concise answers" {
		t.Fatalf("body=%q", got.Body)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
func TestOrganizerPropagatesRunSaveErrors(t *testing.T) {
	rt, store := testRuntime(t, &fixtureClient{})
	if _, err := store.Write(memory.WriteArgs{Slug: "one", Scope: "global", MemoryType: "reference", Description: "one", Body: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), ".kbrain-state.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RunOrganizer(context.Background(), "", OrganizerManual); err == nil {
		t.Fatal("expected persisted state error")
	}
}

func TestOrganizerManualIsReviewable(t *testing.T) {
	rt, store := testRuntime(t, &fixtureClient{})
	desc, body := "same", "same"
	for _, slug := range []string{"one", "two"} {
		if _, err := store.Write(memory.WriteArgs{Slug: slug, Scope: "global", MemoryType: "reference", Description: desc, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := rt.RunOrganizer(context.Background(), "", OrganizerManual)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending_review" && run.Status != "completed" {
		t.Fatalf("status=%s", run.Status)
	}
	history := rt.OrganizerHistory()
	if len(history) == 0 {
		t.Fatal("missing history")
	}
}

func TestOrganizerScheduledNoopCompletes(t *testing.T) {
	rt, _ := testRuntime(t, &fixtureClient{})
	run, err := rt.RunOrganizer(context.Background(), "", OrganizerScheduled)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Phase != "complete" {
		t.Fatalf("scheduled noop=%+v", run)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
