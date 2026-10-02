package backend

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type memoryLifecycleClient struct {
	scriptedClient
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	finished  chan struct{}
}

func (c *memoryLifecycleClient) Stream(ctx context.Context, _ ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	defer close(c.finished)
	close(c.started)
	<-ctx.Done()
	close(c.cancelled)
	<-c.release
	return ai.Message{}, ai.Usage{}, ctx.Err()
}

type memoryLifecycleFixture struct {
	server   *Server
	rt       *runtimeSession
	runtimes []*memoryruntime.Runtime
	clients  []*memoryLifecycleClient
}

func newMemoryLifecycleFixture(t *testing.T) *memoryLifecycleFixture {
	t.Helper()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &memoryLifecycleFixture{}
	f.server, err = New(Options{
		Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root,
		Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&scriptedClient{response: "remembered"}, model.Model, 4096, "system"), nil
		},
		MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			client := &memoryLifecycleClient{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
			runtime, err := memoryruntime.New(memoryruntime.Config{Store: f.server.memoryStore, ExtractionModel: "extract", ResolveModel: func(context.Context, string) (ai.Client, string, error) {
				return client, "extract", nil
			}})
			if err != nil {
				return nil, err
			}
			f.runtimes = append(f.runtimes, runtime)
			f.clients = append(f.clients, client)
			return runtime, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.server.Close()
		for i, runtime := range f.runtimes {
			_ = runtime.Close()
			client := f.clients[i]
			close(client.release)
			select {
			case <-client.started:
				waitMemoryLifecycleSignal(t, client.finished, "extraction exit")
			default:
			}
		}
	})
	id, err := store.Create(root, "old", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.rt, err = f.server.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.agent.TurnParts(context.Background(), "I prefer concise answers", nil, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	waitMemoryLifecycleSignal(t, f.clients[0].started, "extraction start")
	return f
}

func waitMemoryLifecycleSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func memoryLifecycleOperation(t *testing.T, operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- operation() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle operation waited for the extraction provider to return")
	}
}

func TestMemoryLifecycleModelSwitch(t *testing.T) {
	f := newMemoryLifecycleFixture(t)
	memoryLifecycleOperation(t, func() error {
		f.rt.mu.Lock()
		defer f.rt.mu.Unlock()
		return f.server.switchModelLocked(f.rt, protocol.ModelRef{Provider: "fixture", Model: "new"})
	})
	waitMemoryLifecycleSignal(t, f.clients[0].cancelled, "old extraction cancellation")
	if f.rt.memoryRuntime != f.runtimes[1] {
		t.Fatal("replacement memory runtime was not installed")
	}
	if _, err := f.rt.agent.TurnParts(context.Background(), "I prefer Go for services", nil, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	waitMemoryLifecycleSignal(t, f.clients[1].started, "replacement extraction start")
}

func TestMemoryLifecycleUnchangedModelPreservesRuntime(t *testing.T) {
	f := newMemoryLifecycleFixture(t)
	f.rt.mu.Lock()
	err := f.server.switchModelLocked(f.rt, protocol.ModelRef{Provider: "fixture", Model: "old"})
	f.rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 1 || f.rt.memoryRuntime != f.runtimes[0] {
		t.Fatal("unchanged model replaced memory runtime")
	}
	select {
	case <-f.clients[0].cancelled:
		t.Fatal("unchanged model cancelled extraction")
	default:
	}
}

func TestMemoryLifecycleFailedSwitchClosesCandidateOnly(t *testing.T) {
	f := newMemoryLifecycleFixture(t)
	if err := f.server.store.Delete(f.rt.id); err != nil {
		t.Fatal(err)
	}
	f.rt.mu.Lock()
	err := f.server.switchModelLocked(f.rt, protocol.ModelRef{Provider: "fixture", Model: "new"})
	f.rt.mu.Unlock()
	if err == nil {
		t.Fatal("expected recorder initialization failure")
	}
	if f.rt.memoryRuntime != f.runtimes[0] {
		t.Fatal("failed switch replaced original runtime")
	}
	select {
	case <-f.clients[0].cancelled:
		t.Fatal("failed switch cancelled original extraction")
	default:
	}
	f.runtimes[1].Completed(context.Background(), agent.MemoryTurn{SessionID: "candidate", User: ai.Message{Role: "user", Content: "I prefer concise answers"}})
	select {
	case <-f.clients[1].started:
		t.Fatal("discarded candidate still accepts extraction")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMemoryLifecycleServerClose(t *testing.T) {
	f := newMemoryLifecycleFixture(t)
	memoryLifecycleOperation(t, f.server.Close)
	waitMemoryLifecycleSignal(t, f.clients[0].cancelled, "shutdown extraction cancellation")
	if f.rt.memoryRuntime != nil {
		t.Fatal("shutdown retained runtime ownership")
	}
	if err := f.server.Close(); err != nil {
		t.Fatal(err)
	}
	f.rt.mu.Lock()
	err := f.server.switchModelLocked(f.rt, protocol.ModelRef{Provider: "fixture", Model: "new"})
	f.rt.mu.Unlock()
	if err == nil || len(f.runtimes) != 1 {
		t.Fatal("closed server created a replacement runtime")
	}
}

func TestMemoryLifecycleDeleteSession(t *testing.T) {
	f := newMemoryLifecycleFixture(t)
	response := httptest.NewRecorder()
	memoryLifecycleOperation(t, func() error {
		f.server.deleteSession(response, httptest.NewRequest("DELETE", "/v1/sessions/"+f.rt.id, nil), f.rt.id)
		return nil
	})
	if response.Code != 200 {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	waitMemoryLifecycleSignal(t, f.clients[0].cancelled, "deleted session extraction cancellation")
	if f.rt.memoryRuntime != nil {
		t.Fatal("deleted session retained runtime ownership")
	}
}
