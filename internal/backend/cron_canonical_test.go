package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestCronCanonicalHistoryEventsRestart(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{response: "scheduled answer"}
	options := Options{Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, m.Model, 4096, ""), nil
	}}
	service, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	defer func() { server.Close(); service.Close() }()
	createCronPromptForTest(t, server.URL, root, "durable")
	var id string
	var cursor int64
	for attempt := 0; attempt < 2; attempt++ {
		if code := doCronJSON(t, server.URL+"/v1/cron/durable/run-now", http.MethodPost, nil, nil); code != 202 {
			t.Fatalf("run status=%d", code)
		}
		runs := waitCronRuns(t, server.URL, "durable", func(r []CronRunRecord) bool { return len(r) == attempt+1 && r[0].State == "done" })
		if runs[0].SessionID == "" || runs[0].RunID == "" {
			t.Fatalf("missing canonical run association: %+v", runs[0])
		}
		if !runs[0].Success || runs[0].Output != "scheduled answer" {
			t.Fatalf("run=%+v", runs[0])
		}
		var snapshot CronSnapshot
		if code := doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &snapshot); code != 200 {
			t.Fatal(code)
		}
		if snapshot.Tasks[0].SessionID == "" {
			t.Fatal("no durable session binding")
		}
		if attempt == 0 {
			id = snapshot.Tasks[0].SessionID
		} else if id != snapshot.Tasks[0].SessionID {
			t.Fatal("session changed after restart")
		}
		var history protocol.Session
		if code := doCronJSON(t, server.URL+"/v1/sessions/"+id, http.MethodGet, nil, &history); code != 200 {
			t.Fatal(code)
		}
		if len(history.Messages) != 2*(attempt+1) {
			t.Fatalf("history=%+v", history.Messages)
		}
		response, err := http.Get(server.URL + "/v1/sessions/" + id + "/events?after_seq=" + strconv.FormatInt(cursor, 10))
		if err != nil {
			t.Fatal(err)
		}
		events := readSSE(t, response)
		counts := map[string]int{}
		for _, e := range events {
			cursor = e.Seq
			counts[e.Type]++
			if e.ConversationID != id || e.RunID == "" {
				t.Fatalf("identity=%+v", e)
			}
		}
		for _, kind := range []string{protocol.EventRunAccepted, protocol.EventUserMessage, protocol.EventAssistantMessage, protocol.EventRunCompleted} {
			if counts[kind] != 1 {
				t.Fatalf("%s=%d", kind, counts[kind])
			}
		}
		var checkpoints []map[string]any
		if code := doCronJSON(t, server.URL+"/v1/sessions/"+id+"/checkpoints", http.MethodGet, nil, &checkpoints); code != 200 || len(checkpoints) != attempt+1 {
			t.Fatalf("checkpoints=%d %+v", code, checkpoints)
		}
		if attempt == 0 {
			server.Close()
			service.Close()
			service, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			server = httptest.NewServer(service)
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	found := false
	for _, m := range client.requests[1] {
		if m.Role == "assistant" && m.Content == "scheduled answer" {
			found = true
		}
	}
	if !found {
		t.Fatal("restarted prompt omitted previous assistant history")
	}
}

func createCronPromptForTest(t *testing.T, base, cwd, id string) {
	t.Helper()
	create := CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": id, "name": id, "cron": "0 0 0 1 1 0", "enabled": false, "type": "prompt", "prompt": "scheduled prompt", "selectedModel": CronModelRef{CustomProviderID: "fixture", Model: "model"}, "workdir": cwd, "timeoutSeconds": 5}}}}
	if code := doCronJSON(t, base+"/v1/cron", http.MethodPut, create, nil); code != 200 {
		t.Fatalf("create=%d", code)
	}
}

func TestCronCanonicalUnattendedQuestionDoesNotWait(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(Options{Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&questionClient{}, m.Model, 4096, ""), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(service)
	defer server.Close()
	createCronPromptForTest(t, server.URL, root, "question")
	doCronJSON(t, server.URL+"/v1/cron/question/run-now", http.MethodPost, nil, nil)
	runs := waitCronRuns(t, server.URL, "question", func(r []CronRunRecord) bool { return len(r) == 1 && r[0].State == "done" })
	if runs[0].Success || !strings.Contains(runs[0].Output, "unattended") {
		t.Fatalf("expected explicit unattended failure: %+v", runs[0])
	}
	var snapshot CronSnapshot
	doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &snapshot)
	response, err := http.Get(server.URL + "/v1/sessions/" + snapshot.Tasks[0].SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, e := range readSSE(t, response) {
		if e.Type == protocol.EventQuestionRequested {
			t.Fatal("unattended run waited for an answer")
		}
		if e.Type == protocol.EventRunFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("missing canonical failed terminal")
	}
}

func TestCronCanonicalCancelPersistsTerminalBeforeNextRun(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{wait: true, response: "after cancel"}
	service, err := New(Options{Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, m.Model, 4096, ""), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(service)
	defer server.Close()
	createCronPromptForTest(t, server.URL, root, "cancel")
	if code := doCronJSON(t, server.URL+"/v1/cron/cancel/run-now", http.MethodPost, nil, nil); code != 202 {
		t.Fatal(code)
	}
	runs := waitCronRuns(t, server.URL, "cancel", func(r []CronRunRecord) bool { return len(r) == 1 && r[0].RunID != "" })
	if code := doCronJSON(t, server.URL+"/v1/cron/cancel/cancel", http.MethodPost, nil, nil); code != 200 {
		t.Fatal(code)
	}
	waitCronRuns(t, server.URL, "cancel", func(r []CronRunRecord) bool { return len(r) == 1 && cronRunCancelled(r[0]) })
	response, err := http.Get(server.URL + "/v1/sessions/" + runs[0].SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := 0
	for _, event := range readSSE(t, response) {
		if event.Type == protocol.EventRunCancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("cancel terminals=%d", cancelled)
	}
	client.mu.Lock()
	client.wait = false
	client.mu.Unlock()
	if code := doCronJSON(t, server.URL+"/v1/cron/cancel/run-now", http.MethodPost, nil, nil); code != 202 {
		t.Fatal(code)
	}
	next := waitCronRuns(t, server.URL, "cancel", func(r []CronRunRecord) bool { return len(r) == 2 && r[0].State == "done" })
	if !next[0].Success || next[0].SessionID != runs[0].SessionID {
		t.Fatalf("continuation=%+v", next[0])
	}
}

func cronRunCancelled(run CronRunRecord) bool {
	return run.TerminationReason == "cancelled" && run.State == "expired"
}

type cronPersistenceBlockingClient struct {
	*scriptedClient
	entered chan struct{}
	once    sync.Once
}

func newCronPersistenceBlockingClient() *cronPersistenceBlockingClient {
	return &cronPersistenceBlockingClient{
		scriptedClient: &scriptedClient{response: "persist run"},
		entered:        make(chan struct{}),
	}
}

func (c *cronPersistenceBlockingClient) Stream(ctx context.Context, request ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	c.models = append(c.models, request.Model)
	c.requests = append(c.requests, append([]ai.Message(nil), request.Messages...))
	c.mu.Unlock()
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return ai.Message{}, ai.Usage{}, ctx.Err()
}

func (c *cronPersistenceBlockingClient) waitUntilEntered(t *testing.T) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cron agent did not enter the client wait state")
	}
}

func newCronPersistenceFailureServer(t *testing.T, store *session.Store, eventDir string, client *cronPersistenceBlockingClient) (*Server, *httptest.Server) {
	t.Helper()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName = selected.Model
		ag.Provider = selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{
		Store:      store,
		Factory:    factory,
		EventDir:   eventDir,
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return backendServer, httpServer
}

func TestCronCanonicalCompletionPersistenceFailureReleasesWorker(t *testing.T) {
	for _, suffix := range []string{".jsonl", ".runs.json"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			store, err := session.Open(filepath.Join(root, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			events := filepath.Join(root, "events")
			client := newCronPersistenceBlockingClient()
			service, server := newCronPersistenceFailureServer(t, store, events, client)
			createCronPromptForTest(t, server.URL, root, "persist")
			if code := doCronJSON(t, server.URL+"/v1/cron/persist/run-now", http.MethodPost, nil, nil); code != 202 {
				t.Fatal(code)
			}
			runs := waitCronRuns(t, server.URL, "persist", func(r []CronRunRecord) bool { return len(r) == 1 && r[0].RunID != "" })
			client.waitUntilEntered(t)
			path := filepath.Join(events, runs[0].SessionID+suffix)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if code := doCronJSON(t, server.URL+"/v1/cron/persist/cancel", http.MethodPost, nil, nil); code != 200 {
				t.Fatal(code)
			}
			finished := waitCronRuns(t, server.URL, "persist", func(r []CronRunRecord) bool { return len(r) == 1 && cronRunCancelled(r[0]) })
			if finished[0].Success || finished[0].TerminationReason != "cancelled" || !strings.Contains(finished[0].Output, "persist run") {
				t.Fatalf("run=%+v", finished[0])
			}
			rt, err := service.loadRuntimeByID(runs[0].SessionID)
			if err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			active, quarantined := rt.activeLocked(), rt.runtimeErr != nil
			rt.mu.Unlock()
			if active || !quarantined {
				t.Fatalf("active=%v quarantined=%v", active, quarantined)
			}
		})
	}
}

var _ ai.Client = (*questionClient)(nil)
