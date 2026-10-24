package backend

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// abandonedChatClient blocks its first request until the run is cancelled
// (stop button), then answers normally. One instance is shared by every agent
// the factory builds, the way one provider serves the rebuilt agent.
type abandonedChatClient struct {
	scriptedClient
	started chan struct{}
	once    sync.Once
	n       int
}

func (c *abandonedChatClient) Clone() ai.Client { return c }
func (c *abandonedChatClient) Stream(ctx context.Context, _ ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.n++
	n := c.n
	c.mu.Unlock()
	if n == 1 {
		c.once.Do(func() { close(c.started) })
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	return ai.Message{Role: "assistant", Content: "ok", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

// abandonedExtractionClient records every extraction prompt and submits nothing.
type abandonedExtractionClient struct {
	scriptedClient
	prompts []string
}

func (c *abandonedExtractionClient) Clone() ai.Client { return c }
func (c *abandonedExtractionClient) Stream(_ context.Context, request ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, request.Messages[len(request.Messages)-1].TextContent())
	c.mu.Unlock()
	return ai.Message{Role: "assistant"}, ai.Usage{}, nil
}

// judgedIn reports whether text is in a prompt's <latest-user-message> block,
// the only part the extractor is told to judge.
func (c *abandonedExtractionClient) judgedIn(text string) (bool, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.prompts {
		if i := strings.Index(p, "<latest-user-message>"); i >= 0 && strings.Contains(p[i:], text) {
			return true, nil
		}
		// Baseline 1f27ade has no blocks: every listed user turn is judged.
		if !strings.Contains(p, "<latest-user-message>") && strings.Contains(p, text) {
			return true, nil
		}
	}
	return false, append([]string(nil), c.prompts...)
}

func newAbandonedRebuildFixture(t *testing.T) (*Server, *runtimeSession, *abandonedChatClient, *abandonedExtractionClient) {
	t.Helper()
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat := &abandonedChatClient{started: make(chan struct{})}
	extraction := &abandonedExtractionClient{}
	var server *Server
	server, err = New(Options{
		Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root,
		Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(chat, model.Model, 4096, "system"), nil
		},
		MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			return memoryruntime.New(memoryruntime.Config{Store: server.memoryStore, ExtractionModel: "extract", ResolveModel: func(context.Context, string) (ai.Client, string, error) {
				return extraction, "extract", nil
			}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	id, err := store.Create(root, "old", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := server.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return server, rt, chat, extraction
}

// stopTurn runs text and cancels it before the first reply, then persists the
// transcript as executeRun does after every run (server.go rt.recorder.Save).
func stopTurn(t *testing.T, rt *runtimeSession, chat *abandonedChatClient, text string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ag := rt.agent
	go func() { _, err := ag.TurnParts(ctx, text, nil, agent.Events{}); done <- err }()
	<-chat.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("fixture: stopped turn err = %v", err)
	}
	if err := rt.recorder.Save(); err != nil {
		t.Fatal(err)
	}
}

func assertRememberJudged(t *testing.T, rt *runtimeSession, extraction *abandonedExtractionClient, remember string) {
	t.Helper()
	found := false
	for _, m := range rt.agent.MessagesSnapshot() {
		if m.Role == "user" && m.TextContent() == remember {
			found = true
		}
	}
	if !found {
		t.Fatal("fixture: stopped message is not in the rebuilt agent's History")
	}
	if _, err := rt.agent.TurnParts(context.Background(), "好的，继续看 internal/memory", nil, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := extraction.judgedIn(remember); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, prompts := extraction.judgedIn(remember)
	t.Fatalf("stopped turn's remember was never judged after the agent was rebuilt; %d extraction prompts: %q", len(prompts), prompts)
}

// Stop a turn, then send the next message with another model: run_start.go
// switchModelLocked -> rebuildAgentLocked builds a new agent and memory
// runtime. memoryAbandoned lived on the old agent, so the next completed turn
// reports no Abandoned and the stopped message is shown only as
// already-handled context. 1f27ade judged it (last four user turns) and
// bd95f38 judged it at stop time.
func TestStoppedTurnIsJudgedAfterModelSwitch(t *testing.T) {
	server, rt, chat, extraction := newAbandonedRebuildFixture(t)
	remember := "记住：以后提交信息一律用英文写。先帮我跑一下全部测试"
	stopTurn(t, rt, chat, remember)
	rt.mu.Lock()
	err := server.switchModelLocked(rt, protocol.ModelRef{Provider: "fixture", Model: "new"})
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	assertRememberJudged(t, rt, extraction, remember)
}

// Same loss when the idle runtime is reaped (runtime_cache.go, 5 minutes) and
// the session is loaded again before the user's next message.
func TestStoppedTurnIsJudgedAfterIdleReap(t *testing.T) {
	server, rt, chat, extraction := newAbandonedRebuildFixture(t)
	remember := "记住：以后提交信息一律用英文写。先帮我跑一下全部测试"
	stopTurn(t, rt, chat, remember)
	server.releaseRuntime(rt)
	server.reapRuntimes(time.Now().Add(runtimeIdleTTL + time.Minute))
	reloaded, err := server.loadRuntimeByID(rt.id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == rt && reloaded.agent == nil {
		t.Fatal("fixture: reaped runtime was revived without an agent")
	}
	assertRememberJudged(t, reloaded, extraction, remember)
}
