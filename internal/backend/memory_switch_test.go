package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// memorySwitchClient records what the conversation model was offered on every request.
type memorySwitchClient struct {
	scriptedClient
	mu       sync.Mutex
	tools    [][]string
	contexts []string
}

func (c *memorySwitchClient) Stream(ctx context.Context, request ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Function.Name)
	}
	last := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			last = request.Messages[i].Content
			break
		}
	}
	c.mu.Lock()
	c.tools = append(c.tools, names)
	c.contexts = append(c.contexts, last)
	c.mu.Unlock()
	return c.scriptedClient.Stream(ctx, request, onText, onThink, onTool)
}

func (c *memorySwitchClient) lastRequest(t *testing.T) ([]string, string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tools) == 0 {
		t.Fatal("conversation model was never called")
	}
	return c.tools[len(c.tools)-1], c.contexts[len(c.contexts)-1]
}

type memorySwitchFixture struct {
	url         string
	configPath  string
	client      *memorySwitchClient
	extractions atomic.Int64
}

func newMemorySwitchFixture(t *testing.T) *memorySwitchFixture {
	t.Helper()
	// Memory tools and saved-memory refresh resolve the data root from LIVEAGENT_HOME.
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := &memorySwitchFixture{configPath: filepath.Join(root, "config.json"), client: &memorySwitchClient{scriptedClient: scriptedClient{response: "noted"}}}
	cfg := &config.Config{Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: "http://fixture.invalid"}}, Models: map[string]config.Model{"fixture-model": {ID: "fixture-model", Providers: []string{"fixture"}}}}
	settings := NewSettingsStore(cfg, func(c *config.Config) error { return c.SaveFile(f.configPath) })
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	memoryStore, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Write(memory.WriteArgs{Slug: "prefers-go", Scope: "global", MemoryType: "user", Description: "User prefers Go for services", Body: "Go for backend services."}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store: store, EventDir: filepath.Join(root, "events"), MemoryStore: memoryStore, DefaultCWD: root, Settings: settings,
		Factory: func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
			ag := agent.New(f.client, selected.Model, 1024, "system", agent.WithMemoryEnabled(settings.Snapshot().Memory.IsEnabled()))
			ag.WorkingDir, ag.ModelName, ag.Provider = cwd, selected.Model, selected.Provider
			return ag, nil
		},
		MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			return memoryruntime.New(memoryruntime.Config{Store: memoryStore, ExtractionModel: "extract", Throttle: time.Millisecond, ResolveModel: func(context.Context, string) (ai.Client, string, error) {
				f.extractions.Add(1)
				return nil, "", errors.New("extraction fixture declines")
			}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Close()
	})
	f.url = httpServer.URL
	return f
}

func (f *memorySwitchFixture) setEnabled(t *testing.T, enabled bool) {
	t.Helper()
	var projection settingsProjection
	if code := doJSON(t, http.MethodPut, f.url+"/v1/settings", map[string]any{"memory": map[string]any{"enabled": enabled}}, &projection); code != http.StatusOK {
		t.Fatalf("memory switch save status %d", code)
	}
	if projection.Memory.Enabled != enabled {
		t.Fatalf("settings projection memory.enabled = %v, want %v", projection.Memory.Enabled, enabled)
	}
}

// turn runs one prompt to completion and reports whether hidden extraction started.
func (f *memorySwitchFixture) turn(t *testing.T, sessionID, requestID, prompt string, after int64) (int64, bool) {
	t.Helper()
	before := f.extractions.Load()
	runRequest(t, f.url, sessionID, requestID, prompt)
	events := waitForRun(t, f.url, sessionID, after)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && f.extractions.Load() == before {
		time.Sleep(10 * time.Millisecond)
	}
	return events[len(events)-1].Seq, f.extractions.Load() != before
}

func hasMemoryTool(names []string) bool {
	for _, name := range names {
		if name == "memory_manager" || name == "remember" || name == "forget" {
			return true
		}
	}
	return false
}

func TestMemorySwitchAppliesToOpenSessionOnNextTurn(t *testing.T) {
	f := newMemorySwitchFixture(t)
	var health struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if code := doJSON(t, http.MethodGet, f.url+"/v1/health", nil, &health); code != http.StatusOK || health.Capabilities["memory_switch"] != float64(1) {
		t.Fatalf("health capabilities %v (status %d) do not advertise memory_switch", health.Capabilities, code)
	}
	var projection settingsProjection
	if code := doJSON(t, http.MethodGet, f.url+"/v1/settings", nil, &projection); code != http.StatusOK || !projection.Memory.Enabled {
		t.Fatalf("memory must default to enabled, got %+v (status %d)", projection.Memory, code)
	}
	sess := createTestSession(t, f.url)

	seq, extracted := f.turn(t, sess.ID, "on-1", "I prefer Go for backend services", 0)
	tools, context := f.client.lastRequest(t)
	if !hasMemoryTool(tools) || !strings.Contains(context, "prefers-go") || !extracted {
		t.Fatalf("enabled baseline: tools=%v memory-in-context=%v extracted=%v", tools, strings.Contains(context, "prefers-go"), extracted)
	}

	f.setEnabled(t, false)
	reloaded, err := config.LoadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Memory.IsEnabled() {
		t.Fatal("disabled memory switch was not persisted to config")
	}
	seq, extracted = f.turn(t, sess.ID, "off-1", "Remember that I deploy on Fridays", seq)
	tools, context = f.client.lastRequest(t)
	if hasMemoryTool(tools) {
		t.Fatalf("memory tools still offered while memory is off: %v", tools)
	}
	if strings.Contains(context, "prefers-go") || !strings.Contains(context, "Memory is turned off") {
		t.Fatalf("disabled turn context must retract memory without listing entries: %q", context)
	}
	if extracted {
		t.Fatal("hidden extractor ran while memory is off")
	}
	var listed map[string]any
	if code := doJSON(t, http.MethodPost, f.url+"/v1/memory/manage", map[string]any{"command": "memory_list", "args": map[string]any{"scope": "global"}}, &listed); code != http.StatusOK {
		t.Fatalf("memory panel listing failed while memory is off: %d", code)
	}
	if raw, _ := json.Marshal(listed); !strings.Contains(string(raw), "prefers-go") {
		t.Fatalf("stored memory not visible while memory is off: %s", raw)
	}

	f.setEnabled(t, true)
	_, extracted = f.turn(t, sess.ID, "on-2", "I also prefer short commit messages", seq)
	tools, context = f.client.lastRequest(t)
	if !hasMemoryTool(tools) || !strings.Contains(context, "prefers-go") || !extracted {
		t.Fatalf("re-enabled: tools=%v memory-in-context=%v extracted=%v", tools, strings.Contains(context, "prefers-go"), extracted)
	}
}

func TestMemorySwitchOffInstructsFreshSessionNotToSaveMemory(t *testing.T) {
	f := newMemorySwitchFixture(t)
	f.setEnabled(t, false)
	sess := createTestSession(t, f.url)

	seq, _ := f.turn(t, sess.ID, "off-1", "Remember that all skills live in one shared folder", 0)
	_, context := f.client.lastRequest(t)
	for _, want := range []string{"Memory is turned off", "memory.md", "tell the user memory is turned off"} {
		if !strings.Contains(context, want) {
			t.Fatalf("a session started while memory is off must be told not to save memory (missing %q): %q", want, context)
		}
	}
	f.turn(t, sess.ID, "off-2", "And use pnpm for installs", seq)
	f.client.mu.Lock()
	requests := strings.Join(f.client.contexts, "\n")
	f.client.mu.Unlock()
	if n := strings.Count(requests, "Memory is turned off"); n != 1 {
		t.Fatalf("the off instruction should be sent once per session while off, got %d", n)
	}
}

func TestMemorySwitchDisabledIgnoresMemoryToolPolicies(t *testing.T) {
	f := newMemorySwitchFixture(t)
	f.setEnabled(t, false)
	sess := createTestSession(t, f.url)
	code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "policy", Prompt: "hello there friend",
		Options: &protocol.RunOptions{Tools: &protocol.ToolSelection{Policies: map[string]string{"memory_manager": "allow"}}},
	}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("run with a memory tool policy was rejected while memory is off: %d", code)
	}
	waitForRun(t, f.url, sess.ID, 0)
	if tools, _ := f.client.lastRequest(t); hasMemoryTool(tools) {
		t.Fatalf("memory tool policy re-enabled a memory tool: %v", tools)
	}
}
