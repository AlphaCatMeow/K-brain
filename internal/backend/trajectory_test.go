package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestTrajectoryActualToolRunHTTPAndRecovery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "tool" {
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "trajectory command handled"}, "finish_reason": "stop"}}})
		} else {
			args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("printf 'trajectory-real-tool-output\\n' # request-%d", len(request.Messages))})
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "trajectory-call", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
		}
		writeToolHistorySSE(w, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 3, "prompt_tokens_details": map[string]any{"cached_tokens": 4}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := ai.New(upstream.URL, "fixture-key")
	client.MaxRetries = 0
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	backend, httpServer := newToolHistoryServer(t, store, dir, client)
	sess := createTestSession(t, httpServer.URL)
	for i := 0; i < 3; i++ {
		accepted := runRequest(t, httpServer.URL, sess.ID, fmt.Sprintf("trajectory-%d", i), "execute the trajectory command")
		decision := "allow_once"
		if i == 1 {
			decision = "reject"
		}
		events := readTrajectoryRun(t, httpServer.URL, sess.ID, accepted, decision)
		if !hasToolHistoryEvent(events, protocol.EventToolResult) || !hasToolHistoryEvent(events, protocol.EventTextDelta) {
			t.Fatalf("missing runtime events: %+v", events)
		}
		result := toolHistoryToolResult(t, events)
		if i != 1 && !strings.Contains(result.Output, "trajectory-real-tool-output") {
			t.Fatalf("actual tool output: %s", result.Output)
		}
	}
	path := "/v1/sessions/" + sess.ID + "/trajectory"
	var window trajectoryWindow
	trajectoryGET(t, httpServer.URL+path, 200, &window)
	if window.TotalSegmentCount != 3 || window.ReturnedSegmentCount != 3 || window.Truncated {
		t.Fatalf("window: %+v", window)
	}
	if !strings.Contains(window.EventsJSON, "trajectory-real-tool-output") || !strings.Contains(window.EventsJSON, `"err":true`) {
		t.Fatalf("missing tool facts: %s", window.EventsJSON)
	}
	var stats map[string]any
	trajectoryGET(t, httpServer.URL+path+"/stats", 200, &stats)
	if stats["toolCallCount"] != float64(3) || stats["errorCount"] != float64(1) {
		t.Fatalf("counts: %+v", stats)
	}
	usage := stats["usage"].(map[string]any)
	if usage["input_tokens"] != float64(72) || usage["output_tokens"] != float64(18) || usage["cached_tokens"] != float64(24) {
		t.Fatalf("usage: %+v", usage)
	}
	var tail, earlier trajectoryWindow
	trajectoryGET(t, httpServer.URL+path+"?max_segments=1", 200, &tail)
	trajectoryGET(t, httpServer.URL+path+"?max_segments=2&before_segment_index=2", 200, &earlier)
	if tail.OldestSegmentIndex != 2 || !tail.HasMoreBefore || earlier.OldestSegmentIndex != 0 || earlier.HasMoreBefore {
		t.Fatal("invalid cursors")
	}
	if !reflect.DeepEqual(append(earlier.RawEvents, tail.RawEvents...), window.RawEvents) {
		t.Fatal("pagination lost or duplicated raw events")
	}
	var redacted trajectoryWindow
	trajectoryGET(t, httpServer.URL+path+"?redact_tool_content=true", 200, &redacted)
	b, _ := json.Marshal(redacted)
	if strings.Contains(string(b), "printf") || strings.Contains(string(b), "trajectory-real-tool-output") || !redacted.Redacted {
		t.Fatalf("tool content leaked: %s", b)
	}
	var rawAgain trajectoryWindow
	trajectoryGET(t, httpServer.URL+path, 200, &rawAgain)
	if !reflect.DeepEqual(rawAgain, window) {
		t.Fatal("redaction mutated journal")
	}
	for _, q := range []string{"?max_segments=0", "?before_segment_index=-1", "?before_segment_index=bad", "?redact_tool_content=bad"} {
		trajectoryGET(t, httpServer.URL+path+q, 400, nil)
	}
	trajectoryGET(t, httpServer.URL+"/v1/sessions/nonexistent/trajectory", 404, nil)
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := New(Options{Store: store, EventDir: dir, Token: "owner", Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return nil, fmt.Errorf("provider must not be instantiated")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	coldHTTP := httptest.NewServer(cold)
	defer coldHTTP.Close()
	trajectoryGET(t, coldHTTP.URL+path, 401, nil)
	req, _ := http.NewRequest(http.MethodGet, coldHTTP.URL+path, nil)
	req.Header.Set("Authorization", "Bearer owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var restored trajectoryWindow
	if err := json.NewDecoder(resp.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !reflect.DeepEqual(restored, window) {
		t.Fatal("cold journal read differs from runtime snapshot")
	}
	if evidence := os.Getenv("KBRAIN_TRAJECTORY_EVIDENCE"); evidence != "" {
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{"window": window, "stats": stats, "tail": tail, "earlier": earlier, "redacted": redacted} {
			data, _ := json.MarshalIndent(value, "", "  ")
			if err := os.WriteFile(filepath.Join(evidence, "trajectory-runtime-"+name+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if gui := os.Getenv("KBRAIN_TRAJECTORY_GUI_DIR"); gui != "" {
		cmd := exec.Command("node", "--test", "test/trajectory/kbrain-trajectory.test.mjs")
		cmd.Dir = gui
		cmd.Env = append(os.Environ(), "KBRAIN_TRAJECTORY_HTTP_URL="+coldHTTP.URL)
		output, err := cmd.CombinedOutput()
		t.Logf("live HTTP GUI verification:\n%s", output)
		if err != nil {
			t.Fatalf("GUI trajectory verification: %v", err)
		}
	}
	t.Logf("actual Agent -> HTTP provider -> bash/permission -> durable events -> HTTP: session=%s events=%d usage=%v", sess.ID, len(window.RawEvents), usage)
}

func trajectoryGET(t *testing.T, url string, status int, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: status %d: %s", url, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrajectoryCorruptJournalIsNotEmptySuccess(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	backend, httpServer := newTestServer(t, store, dir, &scriptedClient{})
	sess := createTestSession(t, httpServer.URL)
	backend.mu.Lock()
	delete(backend.sessions, sess.ID)
	backend.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, sess.ID+".jsonl"), []byte("{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	trajectoryGET(t, httpServer.URL+"/v1/sessions/"+sess.ID+"/trajectory", 500, nil)
}

func readTrajectoryRun(t *testing.T, base, id string, accepted protocol.RunAccepted, decision string) []protocol.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", base, id, accepted.AcceptedSeq-1), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []protocol.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Type == protocol.EventPermissionRequest {
			var p protocol.PermissionRequest
			_ = json.Unmarshal(event.Payload, &p)
			result := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/permissions/"+p.PermissionID, protocol.PermissionDecisionRequest{ConversationID: id, RunID: accepted.RunID, Decision: protocol.PermissionDecision{PermissionID: p.PermissionID, Decision: decision}}, nil)
			result.Body.Close()
			if result.StatusCode != 200 {
				t.Fatalf("permission status %d", result.StatusCode)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestTrajectoryProjectionPreservesFailureUsageAndRawUnknown(t *testing.T) {
	events := []protocol.Event{
		{Seq: 1, RunID: "r", Type: protocol.EventUsage, Payload: json.RawMessage(`{"input_tokens":8,"output_tokens":2,"cache_write_tokens":3}`)},
		{Seq: 3, RunID: "r", Type: "future.diagnostic", Payload: json.RawMessage(`{"opaque":"retained"}`)},
		{Seq: 4, RunID: "r", Type: protocol.EventRunFailed, Payload: json.RawMessage(`{"state":"failed","error":"provider failure"}`)},
	}
	runs, truncated := trajectoryRuns(events)
	if !truncated || len(runs) != 1 || runs[0].Usage.CacheWriteTokens != 3 {
		t.Fatalf("runs: %+v truncated=%v", runs, truncated)
	}
	if !reflect.DeepEqual(runs[0].Events, events) {
		t.Fatal("raw unknown event lost")
	}
	projected := projectTrajectoryRun(events, 1)
	if len(projected) != 2 || projected[0]["st"] != "error" || projected[0]["err"] != "provider failure" {
		t.Fatalf("projection: %+v", projected)
	}
	usage := projected[0]["u"].(map[string]int)
	if usage["totalTokens"] != 10 || usage["cacheWrite"] != 3 {
		t.Fatalf("usage: %+v", usage)
	}
}
