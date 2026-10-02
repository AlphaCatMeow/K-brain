package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestCronProductionHTTPPersistenceRunNowAndInvalidWorkdir(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	factory := func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronTestClient{}, "model", 128, ""), nil
	}
	backend, err := New(Options{Store: sessions, Factory: factory, EventDir: filepath.Join(root, "events"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	defer server.Close()
	defer backend.Close()
	create := map[string]any{"baseRevision": 0, "ops": []any{map[string]any{"op": "create", "item": map[string]any{"id": "invalid", "name": "invalid", "cron": "0 0 0 1 1 0", "enabled": true, "type": "bash", "script": "printf nope", "workdir": filepath.Join(root, "gone")}}}}
	status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, create, nil)
	if status != http.StatusOK {
		t.Fatalf("create status = %d", status)
	}
	var run CronRunNowResponse
	if status := doCronJSON(t, server.URL+"/v1/cron/invalid/run-now", http.MethodPost, nil, &run); status != http.StatusAccepted || run.StartedAt == 0 {
		t.Fatalf("run now status=%d run=%+v", status, run)
	}
	runs := waitCronRuns(t, server.URL, "invalid", func(runs []CronRunRecord) bool { return len(runs) > 0 && runs[0].State == "done" })
	if len(runs) != 1 || runs[0].Success {
		t.Fatalf("runs = %+v", runs)
	}
	backend.Close()
	backend2, err := New(Options{Store: sessions, Factory: factory, EventDir: filepath.Join(root, "events2"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	defer backend2.Close()
	if got := backend2.cron.store.snapshot(); len(got.Tasks) != 1 || got.Tasks[0].ID != "invalid" {
		t.Fatalf("restarted snapshot = %+v", got)
	}
}

func TestCronProductionHTTPBashRunNowAndCancel(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	factory := func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronTestClient{}, "model", 128, ""), nil
	}
	backend, err := New(Options{Store: sessions, Factory: factory, EventDir: filepath.Join(root, "events"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server := httptest.NewServer(backend)
	defer server.Close()
	create := map[string]any{"baseRevision": 0, "ops": []any{map[string]any{"op": "create", "item": map[string]any{"id": "bash", "name": "bash", "cron": "0 0 0 1 1 0", "enabled": true, "type": "bash", "script": "printf cron-ok", "workdir": root, "timeoutSeconds": 5}}}}
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, create, nil); status != http.StatusOK {
		t.Fatalf("create status = %d", status)
	}
	if status := doCronJSON(t, server.URL+"/v1/cron/bash/run-now", http.MethodPost, nil, nil); status != http.StatusAccepted {
		t.Fatalf("run-now status = %d", status)
	}
	runs := waitCronRuns(t, server.URL, "bash", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "done" })
	if !runs[0].Success || !bytes.Contains([]byte(runs[0].Output), []byte("cron-ok")) {
		t.Fatalf("bash run = %+v", runs[0])
	}

	update := map[string]any{"baseRevision": 1, "ops": []any{map[string]any{"op": "update", "id": "bash", "patch": map[string]any{"script": "sleep 10", "timeoutSeconds": 30}}}}
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, update, nil); status != http.StatusOK {
		t.Fatalf("update status = %d", status)
	}
	if status := doCronJSON(t, server.URL+"/v1/cron/bash/run-now", http.MethodPost, nil, nil); status != http.StatusAccepted {
		t.Fatalf("second run-now status = %d", status)
	}
	waitCronRuns(t, server.URL, "bash", func(runs []CronRunRecord) bool { return len(runs) == 2 && runs[0].State == "leased" })
	if status := doCronJSON(t, server.URL+"/v1/cron/bash/cancel", http.MethodPost, nil, nil); status != http.StatusOK {
		t.Fatalf("cancel status = %d", status)
	}
	runs = waitCronRuns(t, server.URL, "bash", func(runs []CronRunRecord) bool { return len(runs) == 2 && runs[0].State == "expired" })
	if runs[0].Success || runs[0].TerminationReason != "cancelled" || !strings.HasPrefix(runs[0].Output, "Cron run cancelled by user.") {
		t.Fatalf("cancelled run = %+v", runs[0])
	}
}

func TestCronProductionHTTPBashExactExecutionCancel(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	backend, err := New(Options{Store: sessions, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronTestClient{}, "model", 128, ""), nil
	}, EventDir: filepath.Join(root, "events"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server := httptest.NewServer(backend)
	defer server.Close()
	create := map[string]any{"baseRevision": 0, "ops": []any{map[string]any{"op": "create", "item": map[string]any{"id": "exact", "name": "exact", "cron": "0 0 0 1 1 0", "enabled": true, "type": "bash", "script": "sleep 10", "workdir": root, "timeoutSeconds": 30}}}}
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, create, nil); status != http.StatusOK {
		t.Fatalf("create status = %d", status)
	}
	var accepted CronRunNowResponse
	if status := doCronJSON(t, server.URL+"/v1/cron/exact/run-now", http.MethodPost, nil, &accepted); status != http.StatusAccepted || accepted.ExecutionID == "" {
		t.Fatalf("run-now status=%d response=%+v", status, accepted)
	}
	waitCronRuns(t, server.URL, "exact", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "leased" })
	if status := doCronJSON(t, server.URL+"/v1/cron/exact/runs/wrong-execution/cancel", http.MethodPost, nil, nil); status != http.StatusConflict {
		t.Fatalf("wrong execution cancel status = %d", status)
	}
	if status := doCronJSON(t, server.URL+"/v1/cron/exact/runs/"+accepted.ExecutionID+"/cancel", http.MethodPost, nil, nil); status != http.StatusOK {
		t.Fatalf("exact execution cancel status = %d", status)
	}
	runs := waitCronRuns(t, server.URL, "exact", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "expired" })
	if runs[0].TerminationReason != "cancelled" {
		t.Fatalf("cancelled run = %+v", runs[0])
	}
}

func TestCronProductionHTTPRunNowAndExactExecutionCancel(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer upstream.Close()
	backend, err := New(Options{Store: sessions, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronTestClient{}, "model", 128, ""), nil
	}, EventDir: filepath.Join(root, "events"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server := httptest.NewServer(backend)
	defer server.Close()
	create := map[string]any{"baseRevision": 0, "ops": []any{map[string]any{"op": "create", "item": map[string]any{"id": "http-cancel", "name": "http-cancel", "cron": "0 0 0 1 1 0", "enabled": true, "type": "http", "timeoutSeconds": 30, "requests": []any{map[string]any{"url": upstream.URL, "method": "GET"}}}}}}
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, create, nil); status != http.StatusOK {
		t.Fatalf("create status = %d", status)
	}
	waitSignal := func(ch <-chan struct{}, label string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}
	var accepted CronRunNowResponse
	if status := doCronJSON(t, server.URL+"/v1/cron/http-cancel/run-now", http.MethodPost, nil, &accepted); status != http.StatusAccepted || accepted.ExecutionID == "" {
		t.Fatalf("run-now status=%d response=%+v", status, accepted)
	}
	waitSignal(started, "first upstream request")
	if status := doCronJSON(t, server.URL+"/v1/cron/http-cancel/runs/"+accepted.ExecutionID+"/cancel", http.MethodPost, nil, nil); status != http.StatusOK {
		t.Fatalf("cancel status = %d", status)
	}
	waitSignal(cancelled, "first upstream request context cancellation")
	runs := waitCronRuns(t, server.URL, "http-cancel", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "expired" })
	if runs[0].ID != accepted.ExecutionID || runs[0].TerminationReason != "cancelled" {
		t.Fatalf("cancelled HTTP run = %+v", runs[0])
	}

	var next CronRunNowResponse
	if status := doCronJSON(t, server.URL+"/v1/cron/http-cancel/run-now", http.MethodPost, nil, &next); status != http.StatusAccepted || next.ExecutionID == "" || next.ExecutionID == accepted.ExecutionID {
		t.Fatalf("second run-now status=%d response=%+v", status, next)
	}
	waitSignal(started, "second upstream request")
	runs = waitCronRuns(t, server.URL, "http-cancel", func(runs []CronRunRecord) bool {
		return len(runs) == 2 && runs[0].ID == next.ExecutionID && runs[0].State == "leased"
	})
	if status := doCronJSON(t, server.URL+"/v1/cron/http-cancel/runs/"+accepted.ExecutionID+"/cancel", http.MethodPost, nil, nil); status != http.StatusConflict {
		t.Fatalf("old execution cancel status = %d", status)
	}
	runs = waitCronRuns(t, server.URL, "http-cancel", func(runs []CronRunRecord) bool {
		return len(runs) == 2 && runs[0].ID == next.ExecutionID && runs[0].State == "leased"
	})
	if runs[0].TerminationReason != "" {
		t.Fatalf("old execution cancellation stopped new run: %+v", runs[0])
	}
	if status := doCronJSON(t, server.URL+"/v1/cron/http-cancel/runs/"+next.ExecutionID+"/cancel", http.MethodPost, nil, nil); status != http.StatusOK {
		t.Fatalf("second cancel status = %d", status)
	}
	waitSignal(cancelled, "second upstream request context cancellation")
	runs = waitCronRuns(t, server.URL, "http-cancel", func(runs []CronRunRecord) bool { return len(runs) == 2 && runs[0].State == "expired" })
	if runs[0].ID != next.ExecutionID || runs[0].TerminationReason != "cancelled" {
		t.Fatalf("second cancelled HTTP run = %+v", runs[0])
	}
}

func TestCronProductionHTTPBashRunNowTimesOut(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	factory := func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&cronTestClient{}, "model", 128, ""), nil
	}
	backend, err := New(Options{Store: sessions, Factory: factory, EventDir: filepath.Join(root, "events"), DefaultCWD: root, MemoryRoot: filepath.Join(root, "memory")})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server := httptest.NewServer(backend)
	defer server.Close()
	create := map[string]any{"baseRevision": 0, "ops": []any{map[string]any{"op": "create", "item": map[string]any{"id": "timeout", "name": "timeout", "cron": "0 0 0 1 1 0", "enabled": true, "type": "bash", "script": "sleep 2", "workdir": root, "timeoutSeconds": 1}}}}
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodPut, create, nil); status != http.StatusOK {
		t.Fatalf("create status = %d", status)
	}
	if status := doCronJSON(t, server.URL+"/v1/cron/timeout/run-now", http.MethodPost, nil, nil); status != http.StatusAccepted {
		t.Fatalf("run-now status = %d", status)
	}
	runs := waitCronRuns(t, server.URL, "timeout", func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "expired" })
	if runs[0].Success || runs[0].TerminationReason != "timeout" || !strings.HasPrefix(runs[0].Output, "Cron run timed out.") {
		t.Fatalf("timed out run = %+v", runs[0])
	}
}

func TestCronProductionScheduledBashPromptAndSharedTool(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	factory := func(_ context.Context, cwd string, model protocol.ModelRef) (*agent.Agent, error) {
		if cwd != root || model.Provider != "test-provider" || model.Model != "test-model" {
			return nil, fmt.Errorf("unexpected prompt context: %s %+v", cwd, model)
		}
		return agent.New(&cronTestClient{}, "test-model", 128, ""), nil
	}
	service, err := New(Options{Store: sessions, Factory: factory, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(service)
	defer server.Close()
	ag := agent.New(&cronTestClient{}, "test-model", 128, "")
	ag.WorkingDir = root
	service.cron.attachTool(ag, protocol.ModelRef{Provider: "test-provider", Model: "test-model"})
	var tool tools.Tool
	for _, candidate := range ag.Tools {
		if candidate.Def.Function.Name == "CronTaskManager" {
			tool = candidate
		}
	}
	if tool.Run == nil {
		t.Fatal("missing cron tool")
	}
	for _, input := range []string{
		`{"action":"create","name":"scheduled bash","cron":"* * * * * *","type":"bash","script":"printf scheduled-ok","remaining_executions":1}`,
		`{"action":"create","name":"scheduled prompt","cron":"* * * * * *","type":"prompt","prompt":"Reply done","remaining_executions":1}`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(input)); err != nil {
			t.Fatal(err)
		}
	}
	var snapshot CronSnapshot
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &snapshot); status != http.StatusOK || len(snapshot.Tasks) != 2 {
		t.Fatalf("tool/UI snapshot: %d %+v", status, snapshot)
	}
	for _, task := range snapshot.Tasks {
		runs := waitCronRuns(t, server.URL, task.ID, func(runs []CronRunRecord) bool { return len(runs) > 0 && runs[0].State == "done" })
		if !runs[0].Success {
			t.Fatalf("scheduled %s: %+v", task.Type, runs)
		}
		if task.Workdir != root {
			t.Fatalf("tool did not pin cwd: %+v", task)
		}
	}
	var after CronSnapshot
	if status := doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &after); status != http.StatusOK {
		t.Fatalf("post-run snapshot status = %d", status)
	}
	for _, task := range after.Tasks {
		if task.Type != "prompt" {
			continue
		}
		if task.SessionID == "" {
			t.Fatal("prompt cron task did not persist its canonical session id")
		}
		snap, err := sessions.HistorySnapshot(task.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Messages) != 2 || snap.Messages[0].Role != "user" || snap.Messages[1].Role != "assistant" {
			t.Fatalf("canonical prompt history = %+v", snap.Messages)
		}
		before := task.SessionID
		if status := doCronJSON(t, server.URL+"/v1/cron/"+task.ID+"/run-now", http.MethodPost, nil, nil); status != http.StatusAccepted {
			t.Fatalf("manual prompt run-now status = %d", status)
		}
		waitCronRuns(t, server.URL, task.ID, func(runs []CronRunRecord) bool { return len(runs) >= 2 && runs[0].State == "done" })
		latest, err := sessions.HistorySnapshot(before)
		if err != nil {
			t.Fatal(err)
		}
		if len(latest.Messages) != 4 {
			t.Fatalf("prompt task did not reuse session: got %d messages", len(latest.Messages))
		}
	}
	doCronJSON(t, server.URL+"/v1/cron", http.MethodGet, nil, &snapshot)
	for _, task := range snapshot.Tasks {
		if task.Enabled || task.RemainingExecutions == nil || *task.RemainingExecutions != 0 {
			t.Fatalf("scheduled count: %+v", task)
		}
	}
}

func waitCronRuns(t *testing.T, baseURL, taskID string, done func([]CronRunRecord) bool) []CronRunRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var response struct {
			Runs []CronRunRecord `json:"runs"`
		}
		if doCronJSON(t, baseURL+"/v1/cron/"+taskID+"/runs", http.MethodGet, nil, &response) == http.StatusOK && done(response.Runs) {
			return response.Runs
		}
		time.Sleep(20 * time.Millisecond)
	}
	var response struct {
		Runs []CronRunRecord `json:"runs"`
	}
	doCronJSON(t, baseURL+"/v1/cron/"+taskID+"/runs", http.MethodGet, nil, &response)
	t.Fatalf("timed out waiting for cron runs: %+v", response.Runs)
	return nil
}

func doCronJSON(t *testing.T, url, method string, body any, output any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

type cronTestClient struct{}

func (*cronTestClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *cronTestClient) Clone() ai.Client                             { return c }
func (*cronTestClient) SetCacheKey(string)                             {}
func (*cronTestClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "done", ai.Usage{}, nil
}
func (*cronTestClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	return ai.Message{Role: "assistant", Content: "done"}, ai.Usage{}, nil
}
func (*cronTestClient) Endpoint() string { return "" }
