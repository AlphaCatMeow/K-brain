package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestTerminalSSEWaitsForRunOwnershipRelease(t *testing.T) {
	for _, kind := range []string{protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := &runtimeSession{id: "session-one", runID: "run-one", cancel: cancel, changed: make(chan struct{}), events: []protocol.Event{{Seq: 1, ConversationID: "session-one", RunID: "run-one", Type: kind}}}
			svc := &Server{sessions: map[string]*runtimeSession{rt.id: rt}}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { svc.events(w, r, rt.id) }))
			defer httpServer.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body := make(chan string, 1)
			go func() { data, _ := io.ReadAll(response.Body); body <- string(data) }()
			select {
			case data := <-body:
				t.Fatalf("SSE exposed terminal state while the run still owned the session: %s", data)
			case <-time.After(50 * time.Millisecond):
			}
			rt.mu.Lock()
			rt.runDone = true
			rt.cancel = nil
			close(rt.changed)
			rt.changed = make(chan struct{})
			rt.mu.Unlock()
			select {
			case data := <-body:
				if !strings.Contains(data, kind) {
					t.Fatalf("terminal event missing: %s", data)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("terminal event was not released after run cleanup")
			}
		})
	}
}

type shutdownCompactClient struct {
	scriptedClient
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (c *shutdownCompactClient) Complete(ctx context.Context, _ ai.Request) (string, ai.Usage, error) {
	close(c.started)
	<-ctx.Done()
	close(c.cancelled)
	<-c.release
	return "", ai.Usage{}, ctx.Err()
}

func TestServerCloseWaitsForManualCompactionPersistence(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &shutdownCompactClient{scriptedClient: scriptedClient{response: "seed"}, started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	svc, err := New(Options{
		Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(),
		Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(client, model.Model, 1024, "system"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release.Do(func() { close(client.release) })
		_ = svc.Close()
	})
	httpServer := httptest.NewServer(svc)
	t.Cleanup(httpServer.Close)
	sess := createTestSession(t, httpServer.URL)
	seedCompactHistory(t, httpServer.URL, sess.ID)
	before := getSession(t, httpServer.URL, sess.ID)
	var accepted protocol.CompactAccepted
	response := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{ConversationID: sess.ID, ClientRequestID: "shutdown-compact", ExpectedRevision: before.Revision}, &accepted)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("compact status=%d", response.StatusCode)
	}
	waitMemoryLifecycleSignal(t, client.started, "compaction provider start")
	done := make(chan error, 1)
	go func() { done <- svc.Close() }()
	waitMemoryLifecycleSignal(t, client.cancelled, "compaction cancellation")
	select {
	case err := <-done:
		t.Fatalf("Close returned before compaction persisted its terminal state: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(client.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after compaction returned")
	}
	restored := &runtimeSession{id: sess.ID, runs: map[string]runRecord{}}
	if err := restored.loadJournal(svc.eventDir); err != nil {
		t.Fatal(err)
	}
	record := restored.runs["shutdown-compact"]
	if record.RunID != accepted.RunID || !record.Terminal || record.State != "cancelled" {
		t.Fatalf("persisted compaction record=%+v", record)
	}
	if !hasEvent(restored.events, protocol.EventRunCancelled) {
		t.Fatal("Close completed without a persisted compaction cancellation event")
	}
}

func TestServerCloseWaitsForCanonicalRunPersistence(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &memoryLifecycleClient{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	var release sync.Once
	svc, err := New(Options{
		Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(),
		Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(client, model.Model, 1024, "system"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release.Do(func() { close(client.release) })
		_ = svc.Close()
	})
	httpServer := httptest.NewServer(svc)
	t.Cleanup(httpServer.Close)
	sess := createTestSession(t, httpServer.URL)
	accepted := runRequest(t, httpServer.URL, sess.ID, "shutdown-run", "wait for cancellation")
	waitMemoryLifecycleSignal(t, client.started, "canonical provider start")
	rt, err := svc.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Close() }()
	waitMemoryLifecycleSignal(t, client.cancelled, "canonical provider cancellation")
	select {
	case err := <-done:
		t.Fatalf("Close returned before the canonical worker persisted its terminal state: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	response := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "after-close", Prompt: "must not start"}, nil)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closing run admission status=%d", response.StatusCode)
	}
	release.Do(func() { close(client.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after the canonical worker returned")
	}
	restored := &runtimeSession{id: sess.ID, runs: map[string]runRecord{}}
	if err := restored.loadJournal(svc.eventDir); err != nil {
		t.Fatal(err)
	}
	record := restored.runs["shutdown-run"]
	if record.RunID != accepted.RunID || !record.Terminal || record.State != "cancelled" {
		t.Fatalf("persisted shutdown record=%+v", record)
	}
	if !hasEvent(restored.events, protocol.EventRunCancelled) {
		t.Fatal("Close completed without a persisted cancellation event")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.terminalManager != nil || rt.processManager != nil || rt.memoryRuntime != nil {
		t.Fatal("Close retained host runtime ownership")
	}
}
