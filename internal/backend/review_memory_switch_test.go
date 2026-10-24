package backend

// Independent review tests for the memory master switch (8ac064b). They reuse the
// author's memorySwitchClient and the shared backend helpers; every fixture isolates
// LIVEAGENT_HOME and the data root under t.TempDir().

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

type reviewMemoryFixture struct {
	url     string
	client  *memorySwitchClient
	extract *scriptedClient
	close   func()
}

// newReviewMemoryFixture builds a backend over root. The extraction model is a real
// ai.Client that records every extraction request and submits no plan.
func newReviewMemoryFixture(t *testing.T, root string, cfg *config.Config) *reviewMemoryFixture {
	t.Helper()
	configPath := filepath.Join(root, "config.json")
	if cfg == nil {
		loaded, err := config.LoadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg = loaded
	}
	f := &reviewMemoryFixture{client: &memorySwitchClient{scriptedClient: scriptedClient{response: "noted"}}, extract: &scriptedClient{response: "nothing durable"}}
	settings := NewSettingsStore(cfg, func(c *config.Config) error { return c.SaveFile(configPath) })
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	memoryStore, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Write(memory.WriteArgs{Slug: "prefers-go", Scope: "global", MemoryType: "user", Description: "User prefers Go for services", Body: "Go for backend services."}); err != nil && !strings.Contains(err.Error(), "exist") {
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
				return f.extract, "extract", nil
			}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	f.url = httpServer.URL
	closed := false
	f.close = func() {
		if closed {
			return
		}
		closed = true
		httpServer.Close()
		_ = server.Close()
		_ = store.Close()
	}
	t.Cleanup(f.close)
	return f
}

func reviewFixtureConfig() *config.Config {
	return &config.Config{
		DefaultModel: "fixture-model", DefaultProvider: "fixture",
		Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: "http://fixture.invalid"}},
		Models:    map[string]config.Model{"fixture-model": {ID: "fixture-model", Providers: []string{"fixture"}}},
	}
}

func (f *reviewMemoryFixture) setEnabled(t *testing.T, enabled bool) {
	t.Helper()
	if code := doJSON(t, http.MethodPut, f.url+"/v1/settings", map[string]any{"memory": map[string]any{"enabled": enabled}}, nil); code != http.StatusOK {
		t.Fatalf("memory switch save status %d", code)
	}
}

func (f *reviewMemoryFixture) turn(t *testing.T, sessionID, requestID, prompt string, after int64) int64 {
	t.Helper()
	runRequest(t, f.url, sessionID, requestID, prompt)
	events := waitForRun(t, f.url, sessionID, after)
	return events[len(events)-1].Seq
}

func (f *reviewMemoryFixture) extractionRequests() [][]ai.Message {
	f.extract.mu.Lock()
	defer f.extract.mu.Unlock()
	return append([][]ai.Message(nil), f.extract.requests...)
}

// 需求："关闭期间的回合在重新打开后也不能被补提取"。重新打开后的第一次隐藏提取把关闭期间
// 用户说的话原样发给了提取模型（作为 earlier-user-messages）。
func TestReviewMemorySwitchOffPeriodTurnsReachExtractorAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	f.setEnabled(t, false)
	seq := f.turn(t, sess.ID, "off-1", "OFFSECRET-7731 I always deploy on Fridays", 0)
	time.Sleep(50 * time.Millisecond)
	if got := f.extractionRequests(); len(got) != 0 {
		t.Fatalf("extractor ran while memory is off: %d requests", len(got))
	}

	f.setEnabled(t, true)
	f.turn(t, sess.ID, "on-1", "I prefer short commit messages", seq)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(f.extractionRequests()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	requests := f.extractionRequests()
	if len(requests) == 0 {
		t.Fatal("extractor never ran after memory was turned back on")
	}
	for _, request := range requests {
		for _, message := range request {
			if strings.Contains(message.TextContent(), "OFFSECRET-7731") {
				t.Fatalf("a user message sent while memory was off reached the memory extractor after re-enabling:\n%s", message.TextContent())
			}
		}
	}
}

// 关闭时按名字过滤 remember/forget/memory_manager 也过滤掉了客户端自带的同名 client tool：
// 校验阶段接受（202），模型请求里却没有这个工具。开启时同一个 client tool 正常提供。
func TestReviewMemorySwitchOffDropsClientToolNamedRemember(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)
	run := func(requestID string, after int64) ([]string, int64) {
		code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
			ConversationID: sess.ID, ClientRequestID: requestID, Prompt: "hello there friend",
			Options: &protocol.RunOptions{ClientTools: []protocol.ClientTool{{Name: "remember", Description: "Pin a note in the client's sidebar", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}},
		}, nil)
		if code != http.StatusAccepted {
			t.Fatalf("run %s with client tool \"remember\" status %d", requestID, code)
		}
		events := waitForRun(t, f.url, sess.ID, after)
		names, _ := f.client.lastRequest(t)
		return names, events[len(events)-1].Seq
	}
	names, seq := run("on", 0)
	if !slices.Contains(names, "remember") {
		t.Fatalf("baseline: client tool \"remember\" not offered with memory on: %v", names)
	}
	f.setEnabled(t, false)
	names, _ = run("off", seq)
	if !slices.Contains(names, "remember") {
		t.Fatalf("memory off silently dropped the accepted client tool \"remember\": %v", names)
	}
}

// 对抗输入：空对象 / null / 非布尔 / 未知字段被拒绝，只改 memory 的 PUT 不动其他设置。
func TestReviewMemorySwitchSettingsAdversarialInput(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	var before settingsProjection
	if code := doJSON(t, http.MethodGet, f.url+"/v1/settings", nil, &before); code != http.StatusOK || !before.Memory.Enabled {
		t.Fatalf("GET settings %d %+v", code, before.Memory)
	}
	for _, body := range []string{`{"memory":{}}`, `{"memory":{"enabled":null}}`, `{"memory":{"enabled":"false"}}`, `{"memory":{"enabled":false,"extra":1}}`, `{"memory":false}`} {
		req, _ := http.NewRequest(http.MethodPut, f.url+"/v1/settings", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %s status %d, want 400", body, resp.StatusCode)
		}
	}
	var after settingsProjection
	doJSON(t, http.MethodGet, f.url+"/v1/settings", nil, &after)
	if !after.Memory.Enabled {
		t.Fatal("rejected PUT changed memory.enabled")
	}
	f.setEnabled(t, false)
	doJSON(t, http.MethodGet, f.url+"/v1/settings", nil, &after)
	before.Memory.Enabled = false
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("memory-only PUT changed other settings:\nbefore %s\nafter  %s", b, a)
	}
	reloaded, err := config.LoadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Memory.IsEnabled() || reloaded.DefaultModel != "fixture-model" || reloaded.DefaultProvider != "fixture" {
		t.Fatalf("persisted config after memory-only PUT: memory=%v defaultModel=%q defaultProvider=%q", reloaded.Memory.IsEnabled(), reloaded.DefaultModel, reloaded.DefaultProvider)
	}
}

// 重启后从磁盘恢复会话：关闭状态的撤回只发一次，重新打开后能再次注入，提取器在关闭期间不跑。
func TestReviewMemorySwitchRetractionSurvivesReload(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	first := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	sess := createTestSession(t, first.url)
	seq := first.turn(t, sess.ID, "on-1", "hello there friend", 0)
	if _, context := first.client.lastRequest(t); !strings.Contains(context, "prefers-go") {
		t.Fatalf("baseline turn did not inject memory: %q", context)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(first.extractionRequests()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	first.setEnabled(t, false)
	first.close()

	second := newReviewMemoryFixture(t, root, nil)
	seq = second.turn(t, sess.ID, "off-1", "second turn after restart", seq)
	if _, context := second.client.lastRequest(t); strings.Contains(context, "prefers-go") || !strings.Contains(context, "Memory is turned off") {
		t.Fatalf("reloaded session with memory off must retract: %q", context)
	}
	seq = second.turn(t, sess.ID, "off-2", "third turn still off", seq)
	if _, context := second.client.lastRequest(t); strings.Contains(context, "Memory is turned off") || strings.Contains(context, "prefers-go") {
		t.Fatalf("retraction should be sent once and memory stays out: %q", context)
	}
	time.Sleep(50 * time.Millisecond)
	if got := second.extractionRequests(); len(got) != 0 {
		t.Fatalf("extractor ran on a reloaded session while memory is off: %d", len(got))
	}
	second.setEnabled(t, true)
	second.turn(t, sess.ID, "on-2", "fourth turn back on", seq)
	if _, context := second.client.lastRequest(t); !strings.Contains(context, "prefers-go") {
		t.Fatalf("memory not re-injected after re-enable on a reloaded session: %q", context)
	}
}

// 并发：多个会话持续跑回合，同时反复 PUT 开关。用 -race 跑。每个回合要么完整开、要么
// 完整关：同一请求里不能既有记忆工具又有撤回，或没有工具却列出记忆条目。
func TestReviewMemorySwitchConcurrentTogglesAndRuns(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	const sessions, turns = 4, 6
	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = createTestSession(t, f.url).ID
	}
	var wg sync.WaitGroup
	errs := make(chan error, sessions*turns+64)
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		enabled := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			raw, _ := json.Marshal(map[string]any{"memory": map[string]any{"enabled": enabled}})
			req, _ := http.NewRequest(http.MethodPut, f.url+"/v1/settings", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("toggle status %d", resp.StatusCode)
				return
			}
			enabled = !enabled
			time.Sleep(2 * time.Millisecond)
		}
	}()
	var runners sync.WaitGroup
	for _, id := range ids {
		runners.Add(1)
		go func(id string) {
			defer runners.Done()
			after := int64(0)
			for turn := 0; turn < turns; turn++ {
				raw, _ := json.Marshal(protocol.PromptRequest{ConversationID: id, ClientRequestID: fmt.Sprintf("r-%d", turn), Prompt: fmt.Sprintf("turn %d of %s", turn, id)})
				resp, err := http.Post(f.url+"/v1/sessions/"+id+"/runs", "application/json", bytes.NewReader(raw))
				if err != nil {
					errs <- err
					return
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusAccepted {
					errs <- fmt.Errorf("run status %d", resp.StatusCode)
					return
				}
				for {
					resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", f.url, id, after))
					if err != nil {
						errs <- err
						return
					}
					var last protocol.Event
					buf := new(bytes.Buffer)
					_, _ = buf.ReadFrom(resp.Body)
					_ = resp.Body.Close()
					done := false
					for _, line := range strings.Split(buf.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &last); err != nil {
							errs <- err
							return
						}
						after = last.Seq
						if last.Type == protocol.EventRunCompleted || last.Type == protocol.EventRunFailed || last.Type == protocol.EventRunCancelled {
							done = true
						}
					}
					if done {
						break
					}
				}
			}
		}(id)
	}
	runners.Wait()
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	for i, names := range f.client.tools {
		ctx := f.client.contexts[i]
		on := hasMemoryTool(names)
		if on && strings.Contains(ctx, "Memory is turned off") {
			t.Errorf("request %d mixes memory tools with the off notice", i)
		}
		if !on && strings.Contains(ctx, "prefers-go") {
			t.Errorf("request %d lists memory entries without memory tools", i)
		}
	}
}
