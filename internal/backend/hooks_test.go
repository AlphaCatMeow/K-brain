package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHookStoreRevisionAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	store, err := NewHookStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := map[string]any{"id": "h1", "name": "first", "event": "agent_start", "enabled": true, "type": "command", "script": "true"}
	response, err := store.Apply(HooksApplyInput{BaseRevision: 1, Ops: []HookOperation{{Op: "create", Item: item}}})
	if err != nil || response.Status != "ok" {
		t.Fatalf("apply=%+v err=%v", response, err)
	}
	conflict, err := store.Apply(HooksApplyInput{BaseRevision: 1, Ops: []HookOperation{{Op: "delete", ID: "h1"}}})
	if err != nil || conflict.Status != "conflict" || len(conflict.Hooks.Hooks) != 1 {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
	reloaded, err := NewHookStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Snapshot().Hooks) != 1 || reloaded.Snapshot().Hooks[0].ID != "h1" {
		t.Fatal("hook was not persisted")
	}
	mode, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && mode.Mode().Perm() != 0o600) {
		t.Fatalf("hook file mode=%v err=%v", mode.Mode(), err)
	}
}

func TestBackendHookRunnerCommandAndHTTPAllEventsAbortAndFailure(t *testing.T) {
	type requestRecord struct {
		path  string
		event string
		body  string
	}
	requests := make(chan requestRecord, 3)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read %s body: %v", r.URL.Path, err)
		}
		requests <- requestRecord{
			path:  r.URL.Path,
			event: r.Header.Get("X-LiveAgent-Hook-Event"),
			body:  string(data),
		}
		if r.URL.Path == "/fail" {
			http.Error(w, "fixture failure", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer httpServer.Close()
	path := filepath.Join(t.TempDir(), "hooks.json")
	store, err := NewHookStore(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "command-events")
	command := "printf '%s\\n' \"$LIVEAGENT_HOOK_EVENT\" >> \"" + marker + "\""
	if runtime.GOOS == "windows" {
		command = "Add-Content -LiteralPath '" + strings.ReplaceAll(marker, "'", "''") + "' $env:LIVEAGENT_HOOK_EVENT"
	}
	items := make([]BackendHook, 0, len(backendHookEvents))
	for event := range backendHookEvents {
		items = append(items, BackendHook{ID: event, Name: event, Event: event, Enabled: true, Type: HookCommand, Script: command})
	}
	items = append(items, BackendHook{
		ID:      "http",
		Name:    "http",
		Event:   "agent_end",
		Enabled: true,
		Type:    HookHTTP,
		Requests: []HookRequest{
			{ID: "no-body", URL: httpServer.URL + "/no-body", Method: "POST"},
			{ID: "explicit-body", URL: httpServer.URL + "/explicit-body", Method: "POST", Body: map[string]any{"custom": "value"}},
		},
	})
	items = append(items, BackendHook{ID: "failed", Name: "failed", Event: "turn_end", Enabled: true, Type: HookHTTP, Requests: []HookRequest{{ID: "bad", URL: httpServer.URL + "/fail", Method: "POST"}}})
	if _, err := store.Apply(HooksApplyInput{BaseRevision: 1, Ops: func() []HookOperation {
		out := make([]HookOperation, len(items))
		for i := range items {
			data, _ := json.Marshal(items[i])
			var obj map[string]any
			_ = json.Unmarshal(data, &obj)
			out[i] = HookOperation{Op: "create", Item: obj}
		}
		return out
	}()}); err != nil {
		t.Fatal(err)
	}
	runner := NewBackendHookRunner(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	warns := 0
	dispatch := runner.Scope(ctx, "c1", "r1", t.TempDir(), func(_ BackendHook, _ string, _ error) { warns++ })
	for _, event := range []string{"agent_start", "turn_start", "message_start", "tool_execution_start", "tool_execution_end", "message_end", "turn_end", "agent_end"} {
		dispatch(event)
	}
	if warns != 1 {
		t.Fatalf("warnings=%d, want one failed hook", warns)
	}
	cancel()
	dispatch("agent_start")
	commandData, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range strings.Fields(string(commandData)) {
		seen[event] = true
	}
	for event := range backendHookEvents {
		if !seen[event] {
			t.Fatalf("command fixture events=%v missing %s", seen, event)
		}
	}
	got := map[string]requestRecord{}
	for range 3 {
		request := <-requests
		got[request.path] = request
	}
	if len(got) != 3 {
		t.Fatalf("http executions=%v, want successful no-body and explicit-body plus failed request", got)
	}
	if got["/no-body"].event != "agent_end" || got["/no-body"].body != "" {
		t.Fatalf("no-body request=%+v, want an empty body and agent_end header", got["/no-body"])
	}
	var explicitBody map[string]any
	if err := json.Unmarshal([]byte(got["/explicit-body"].body), &explicitBody); err != nil {
		t.Fatalf("explicit body=%q is not JSON: %v", got["/explicit-body"].body, err)
	}
	if got["/explicit-body"].event != "agent_end" || explicitBody["custom"] != "value" {
		t.Fatalf("explicit-body request=%+v, decoded=%v", got["/explicit-body"], explicitBody)
	}
	if got["/fail"].event != "turn_end" || got["/fail"].body != "" {
		t.Fatalf("failed request=%+v, want turn_end header and empty body", got["/fail"])
	}
}

func TestBackendHookRunnerCancelStopsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses shell")
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	store, err := NewHookStore(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	started := marker + ".started"
	item := BackendHook{ID: "abort", Name: "abort", Event: "agent_start", Enabled: true, Type: HookCommand, Script: "touch " + started + "; sleep 2; touch " + marker, TimeoutMS: 600000}
	data, _ := json.Marshal(item)
	var obj map[string]any
	_ = json.Unmarshal(data, &obj)
	if _, err := store.Apply(HooksApplyInput{BaseRevision: 1, Ops: []HookOperation{{Op: "create", Item: obj}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { NewBackendHookRunner(store).Scope(ctx, "c", "r", t.TempDir(), nil)("agent_start"); close(done) }()
	deadline := time.After(time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("command did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("cancelled command reached the post-cancel marker")
	}
}

func TestBackendHookRunnerCancelStopsHTTP(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	requestDone := make(chan error, 1)
	go func() {
		requestDone <- runHookRequest(ctx, 10*time.Minute, HookRequest{ID: "cancel", URL: server.URL, Method: "POST"}, hookEnvelope{Event: "agent_end"})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP hook did not start")
	}
	cancel()
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("cancelled HTTP hook returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled HTTP hook did not return")
	}
}
