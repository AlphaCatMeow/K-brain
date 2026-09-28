package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendCLIExecutableIntegration(t *testing.T) {
	fixture := t.TempDir()
	configPath := filepath.Join(fixture, "config.json")
	sessionDir := filepath.Join(fixture, "sessions")
	upstreamCalls := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture response\"}}]}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	config := fmt.Sprintf(`{
  "defaultModel": "fixture-model",
  "providers": {
    "fixture": {
      "api": "openai-completions",
      "baseUrl": %q,
      "apiKey": "fixture-key",
      "models": [{"id": "fixture-model", "contextWindow": 4096, "maxTokens": 256}]
    }
  }
}
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(fixture, "kn")
	build := exec.Command("go", "build", "-o", binary, "./cmd/kn")
	build.Dir = repoRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kn: %v\n%s", err, output)
	}

	listen := freeListenAddress(t)
	first := startBackendProcess(t, binary, listen, configPath, sessionDir)
	base := "http://" + listen
	waitForBackend(t, first, base)

	sessionID := createExecutableSession(t, base, fixture)
	session := getExecutableSession(t, base, sessionID)
	if session.MessageCount != 0 || len(session.Messages) != 0 {
		t.Fatalf("new session was not empty: %+v", session)
	}
	accepted := runExecutablePrompt(t, base, sessionID, "request-1", "first prompt")
	events := readExecutableSSE(t, base, sessionID, accepted.AcceptedSeq-1)
	assertExecutableResponse(t, events, "fixture response")

	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream calls after first prompt = %d, want 1", got)
	}
	stopBackendProcess(t, first)

	second := startBackendProcess(t, binary, listen, configPath, sessionDir)
	waitForBackend(t, second, base)
	defer stopBackendProcess(t, second)

	session = getExecutableSession(t, base, sessionID)
	if session.MessageCount != 2 || len(session.Messages) != 2 {
		t.Fatalf("persisted session has message count %d and %d messages, want 2 and 2", session.MessageCount, len(session.Messages))
	}
	if session.Messages[0].Role != "user" || session.Messages[0].Content[0].Text != "first prompt" {
		t.Fatalf("persisted user message = %+v", session.Messages[0])
	}
	if session.Messages[1].Role != "assistant" || session.Messages[1].Content[0].Text != "fixture response" {
		t.Fatalf("persisted assistant message = %+v", session.Messages[1])
	}

	accepted = runExecutablePrompt(t, base, sessionID, "request-2", "second prompt")
	events = readExecutableSSE(t, base, sessionID, accepted.AcceptedSeq-1)
	assertExecutableResponse(t, events, "fixture response")
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("upstream calls after restart = %d, want 2", got)
	}
}

type backendProcess struct {
	cmd    *exec.Cmd
	output *bytes.Buffer
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

func freeListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startBackendProcess(t *testing.T, binary, listen, configPath, sessionDir string) *backendProcess {
	t.Helper()
	output := new(bytes.Buffer)
	cmd := exec.Command(binary, "backend", "-listen", listen, "-config", configPath, "-session-dir", sessionDir)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start backend: %v", err)
	}
	return &backendProcess{cmd: cmd, output: output}
}

func waitForBackend(t *testing.T, process *backendProcess, base string) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if process.cmd.ProcessState != nil {
			t.Fatalf("backend exited before startup: %s", process.output.String())
		}
		resp, err := client.Get(base + "/v1/health")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte(`"status":"ok"`)) {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("backend did not become healthy: %s", process.output.String())
}

func stopBackendProcess(t *testing.T, process *backendProcess) {
	t.Helper()
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	if process.cmd.ProcessState != nil {
		return
	}
	if err := process.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("stop backend: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- process.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("backend exited with error: %v\n%s", err, process.output.String())
		}
	case <-ctx.Done():
		_ = process.cmd.Process.Kill()
		t.Fatalf("backend did not stop: %s", process.output.String())
	}
}

type executableSession struct {
	ID           string `json:"id"`
	MessageCount int    `json:"message_count"`
	Messages     []struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

type executableRun struct {
	RunID       string `json:"run_id"`
	AcceptedSeq int64  `json:"accepted_seq"`
}

func createExecutableSession(t *testing.T, base, cwd string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"cwd":   cwd,
		"model": map[string]string{"provider": "fixture", "model": "fixture-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewReader(payload)
	resp := doExecutableRequest(t, http.MethodPost, base+"/v1/sessions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session status %d: %s", resp.StatusCode, data)
	}
	var session executableSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatal("create session returned an empty id")
	}
	return session.ID
}

func getExecutableSession(t *testing.T, base, id string) executableSession {
	t.Helper()
	resp := doExecutableRequest(t, http.MethodGet, base+"/v1/sessions/"+id, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("get session status %d: %s", resp.StatusCode, data)
	}
	var session executableSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	return session
}

func runExecutablePrompt(t *testing.T, base, sessionID, requestID, prompt string) executableRun {
	t.Helper()
	payload := fmt.Sprintf(`{"conversation_id":%q,"client_request_id":%q,"prompt":%q}`, sessionID, requestID, prompt)
	resp := doExecutableRequest(t, http.MethodPost, base+"/v1/sessions/"+sessionID+"/runs", strings.NewReader(payload))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("run status %d: %s", resp.StatusCode, data)
	}
	var run executableRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.RunID == "" || run.AcceptedSeq == 0 {
		t.Fatalf("invalid run acceptance: %+v", run)
	}
	return run
}

func readExecutableSSE(t *testing.T, base, sessionID string, after int64) []map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", base, sessionID, after), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE status %d: %s", resp.StatusCode, data)
	}
	scanner := bufio.NewScanner(resp.Body)
	var events []map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func assertExecutableResponse(t *testing.T, events []map[string]any, want string) {
	t.Helper()
	var text string
	completed := false
	for _, event := range events {
		if event["type"] == "assistant.text.delta" {
			payload, ok := event["payload"].(map[string]any)
			if !ok {
				t.Fatalf("text event payload has type %T: %+v", event["payload"], event)
			}
			text += fmt.Sprint(payload["text"])
		}
		if event["type"] == "run.completed" {
			completed = true
		}
	}
	if text == "" || !strings.Contains(text, want) {
		t.Fatalf("SSE did not contain nonempty response %q: %+v", want, events)
	}
	if !completed {
		t.Fatalf("SSE did not contain run.completed: %+v", events)
	}
}

func doExecutableRequest(t *testing.T, method, url string, body io.Reader) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
