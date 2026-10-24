package memoryruntime

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	previous := logf
	logf = func(format string, args ...any) { log.New(buf, "", 0).Printf(format, args...) }
	t.Cleanup(func() { logf = previous })
	return buf
}

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func extractionTurn(text string) agent.MemoryTurn {
	return agent.MemoryTurn{SessionID: "s", User: ai.Message{ID: "u1", Role: "user", Content: text}, History: []ai.Message{{Role: "user", Content: text}}}
}

// The commit says real models hand back "evidence" as a plain string. A
// decision that is otherwise complete (slug/scope/type/description/body) must
// not be thrown away only because that extra field is a string.
func TestWriteWithStringEvidenceIsKept(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"user-reply-language","scope":"global","type":"user","description":"Reply in Chinese","body":"Answer in Chinese.","evidence":"以后回答我都用中文"}]}`}
	rt, store := testRuntime(t, client)
	rt.Completed(context.Background(), extractionTurn("以后回答我都用中文，记住"))
	ok := waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "user-reply-language", Scope: "global"})
		return err == nil
	})
	if !ok {
		client.mu.Lock()
		calls := client.calls
		client.mu.Unlock()
		t.Fatalf("complete write decision with string evidence was dropped (model calls=%d)", calls)
	}
}

// "write" is advertised as "new memory". Mapped to upsert, it silently replaces
// the body of a memory the user already reviewed and keeps it marked reviewed,
// so the panel shows model-written text as user-approved.
func TestExtractorWriteDoesNotRewriteReviewedMemoryAsReviewed(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"user-reply-language","scope":"global","type":"user","description":"Reply in English","body":"Answer in English.","confidence":"high","source_quote":"answer in english for this one"}]}`}
	rt, store := testRuntime(t, client)
	reviewed := false
	if _, err := store.Write(memory.WriteArgs{Slug: "user-reply-language", Scope: "global", MemoryType: "user", Description: "Reply in Chinese", Body: "Answer in Chinese.", Actor: "user", Unreviewed: &reviewed}); err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), extractionTurn("answer in english for this one"))
	waitFor(t, func() bool {
		got, err := store.Read(memory.ReadArgs{Slug: "user-reply-language", Scope: "global"})
		return err == nil && got.Body != "Answer in Chinese."
	})
	got, err := store.Read(memory.ReadArgs{Slug: "user-reply-language", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Answer in Chinese." && !got.Meta.Unreviewed {
		t.Fatalf("reviewed memory body replaced by extractor and still marked reviewed: body=%q unreviewed=%v", got.Body, got.Meta.Unreviewed)
	}
}

// Provider errors (ai.HTTPError) carry the raw upstream body. The backend
// deliberately never surfaces them (backend/text.go writeTextError); the new
// extraction logging prints them verbatim to stderr.
func TestExtractionLogDoesNotPrintProviderErrorBody(t *testing.T) {
	buf := captureLog(t)
	client := &errorClient{err: &ai.HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: "Incorrect API key provided: sk-proj-AbCdEf123456"}}
	store, err := memory.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) { return client, "fixture", nil }, ExtractionModel: "fixture", Throttle: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), extractionTurn("I prefer pnpm"))
	waitFor(t, func() bool { return strings.Count(buf.String(), "memory extraction") >= 2 })
	if strings.Contains(buf.String(), "sk-proj-AbCdEf123456") {
		t.Fatalf("provider error body logged:\n%s", buf.String())
	}
}

type errorClient struct {
	fixtureClient
	err error
}

func (c *errorClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	return ai.Message{}, ai.Usage{}, c.err
}

func (c *errorClient) Clone() ai.Client { return c }

func TestExtractorUpdateMarksReviewedMemoryUnreviewed(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"update","slug":"user-reply-language","scope":"global","body":"Answer in English.","mode":"replace"}]}`}
	rt, store := testRuntime(t, client)
	reviewed := false
	if _, err := store.Write(memory.WriteArgs{Slug: "user-reply-language", Scope: "global", MemoryType: "user", Description: "Reply language", Body: "Answer in Chinese.", Actor: "user", Unreviewed: &reviewed}); err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), extractionTurn("answer in english"))
	ok := waitFor(t, func() bool {
		got, err := store.Read(memory.ReadArgs{Slug: "user-reply-language", Scope: "global"})
		return err == nil && got.Body == "Answer in English." && got.Meta.Unreviewed
	})
	if !ok {
		got, _ := store.Read(memory.ReadArgs{Slug: "user-reply-language", Scope: "global"})
		t.Fatalf("body=%q unreviewed=%v", got.Body, got.Meta.Unreviewed)
	}
}
