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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendCLIExecutableIntegration(t *testing.T) {
	fixture := t.TempDir()
	configPath := filepath.Join(fixture, "config.json")
	sessionDir := filepath.Join(fixture, "sessions")
	upstreamCalls := atomic.Int64{}
	chatCalls := atomic.Int64{}
	memoryCalls := atomic.Int64{}
	compactionCalls := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		hasMemoryPlanTool := false
		for _, tool := range request.Tools {
			if tool.Function.Name == "SubmitMemoryPlan" {
				hasMemoryPlanTool = true
				break
			}
		}
		streaming := strings.Contains(string(body), `"stream":true`)
		kind := "chat"
		switch {
		case !streaming:
			// Compaction summarisation is neither a chat turn nor memory extraction.
			kind = "compaction"
			compactionCalls.Add(1)
		case hasMemoryPlanTool:
			kind = "memory extraction"
			memoryCalls.Add(1)
		default:
			chatCalls.Add(1)
		}
		upstreamCalls.Add(1)
		t.Logf("fixture upstream request kind=%s tools=%d", kind, len(request.Tools))
		if !streaming {
			// Compaction summaries use a non-streaming request and expect a JSON body.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"fixture summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		if hasMemoryPlanTool {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"memory-call\",\"type\":\"function\",\"function\":{\"name\":\"SubmitMemoryPlan\",\"arguments\":\"{\\\"decisions\\\":[]}\"}}]}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture response\"}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		}
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	t.Setenv("LIVEAGENT_HOME", filepath.Join(fixture, "liveagent-home"))

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
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/kn")
	build.Dir = repoRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kn: %v\n%s", err, output)
	}

	listen := freeListenAddress(t)
	t.Logf("backend start #1: binary=%s listen=%s config=%s session_dir=%s", binary, listen, configPath, sessionDir)
	first := startBackendProcess(t, binary, listen, configPath, sessionDir)
	base := "http://" + listen
	waitForBackend(t, first, base)
	t.Logf("backend start #1 ready: base=%s output=%q", base, first.output.String())

	sessionID := createExecutableSession(t, base, fixture)
	t.Logf("session created: id=%s cwd=%s", sessionID, fixture)
	session := getExecutableSession(t, base, sessionID)
	if session.MessageCount != 0 || len(session.Messages) != 0 {
		t.Fatalf("new session was not empty: %+v", session)
	}
	accepted := runExecutablePrompt(t, base, sessionID, "request-1", "first prompt")
	events := readExecutableSSE(t, base, sessionID, accepted.AcceptedSeq-1)
	logExecutableSSE(t, "prompt #1", events)
	assertExecutableResponse(t, events, "fixture response")
	waitForAtomicCount(t, &memoryCalls, 1)
	if got := memoryCalls.Load(); got != 1 {
		t.Fatalf("memory extraction calls after first prompt = %d, want 1", got)
	}

	if got := chatCalls.Load(); got != 1 {
		t.Fatalf("chat upstream calls after first prompt = %d, want 1", got)
	}
	if got := upstreamCalls.Load(); got != chatCalls.Load()+memoryCalls.Load() {
		t.Fatalf("upstream call accounting after first prompt = %d, chat=%d memory=%d", got, chatCalls.Load(), memoryCalls.Load())
	}
	t.Logf("prompt #1 terminal observed: session=%s accepted_seq=%d chat_calls=%d memory_calls=%d upstream_calls=%d", sessionID, accepted.AcceptedSeq, chatCalls.Load(), memoryCalls.Load(), upstreamCalls.Load())
	stopBackendProcess(t, first)
	t.Logf("backend start #1 stopped: output=%q", first.output.String())

	t.Logf("backend start #2: binary=%s listen=%s config=%s session_dir=%s", binary, listen, configPath, sessionDir)
	second := startBackendProcess(t, binary, listen, configPath, sessionDir)
	waitForBackend(t, second, base)
	t.Logf("backend start #2 ready: base=%s output=%q", base, second.output.String())
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
	logExecutableSSE(t, "prompt #2", events)
	assertExecutableResponse(t, events, "fixture response")
	waitForAtomicCount(t, &memoryCalls, 2)
	if got := chatCalls.Load(); got != 2 {
		t.Fatalf("chat upstream calls after restart = %d, want 2", got)
	}
	if got, want := upstreamCalls.Load(), chatCalls.Load()+memoryCalls.Load()+compactionCalls.Load(); got != want {
		t.Fatalf("upstream call accounting after restart = %d, want %d (chat=%d memory=%d compaction=%d)", got, want, chatCalls.Load(), memoryCalls.Load(), compactionCalls.Load())
	}
	t.Logf("prompt #2 terminal observed: session=%s accepted_seq=%d chat_calls=%d memory_calls=%d upstream_calls=%d", sessionID, accepted.AcceptedSeq, chatCalls.Load(), memoryCalls.Load(), upstreamCalls.Load())
}

func TestBackendCLIParentStdioLifecycle(t *testing.T) {
	fixture := t.TempDir()
	configPath := filepath.Join(fixture, "config.json")
	sessionDir := filepath.Join(fixture, "sessions")
	if err := os.WriteFile(configPath, []byte(`{
  "defaultModel": "fixture-model",
  "providers": {
    "fixture": {
      "api": "openai-completions",
      "baseUrl": "http://127.0.0.1:1",
      "apiKey": "fixture-key",
      "models": [{"id": "fixture-model", "contextWindow": 4096, "maxTokens": 256}]
    }
  }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(fixture, "kn")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/kn")
	build.Dir = repoRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kn: %v\n%s", err, output)
	}

	process := startBackendProcessWithEnv(t, binary, ":0", configPath, sessionDir, []string{"K_BRAIN_BACKEND_TOKEN=parent-token"}, "-parent-stdio")
	t.Cleanup(func() { stopBackendProcess(t, process) })
	ready := waitForBackendReady(t, process)
	host, port, err := net.SplitHostPort(ready)
	if err != nil {
		t.Fatalf("ready address %q: %v", ready, err)
	}
	if host != "127.0.0.1" || port == "0" {
		t.Fatalf("ready address = %q, want dynamic loopback address", ready)
	}
	base := "http://127.0.0.1:" + port
	unauthorized := doExecutableRequest(t, http.MethodGet, base+"/v1/health", nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		unauthorized.Body.Close()
		t.Fatalf("health without token status = %d, want %d", unauthorized.StatusCode, http.StatusUnauthorized)
	}
	unauthorized.Body.Close()
	waitForBackendHealthWithToken(t, process, base, "parent-token")
	if process.cmd.ProcessState != nil {
		t.Fatalf("backend exited while parent stdin remained open")
	}

	if err := process.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitBackendProcess(t, process, true)
	waitForPortClosed(t, ready)
}

func TestBackendCLIDefaultModeIgnoresStdinEOF(t *testing.T) {
	fixture := t.TempDir()
	configPath := filepath.Join(fixture, "config.json")
	sessionDir := filepath.Join(fixture, "sessions")
	if err := os.WriteFile(configPath, []byte(`{
  "defaultModel": "fixture-model",
  "providers": {
    "fixture": {
      "api": "openai-completions",
      "baseUrl": "http://127.0.0.1:1",
      "apiKey": "fixture-key",
      "models": [{"id": "fixture-model", "contextWindow": 4096, "maxTokens": 256}]
    }
  }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(fixture, "kn")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/kn")
	build.Dir = repoRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kn: %v\n%s", err, output)
	}
	process := startBackendProcess(t, binary, ":0", configPath, sessionDir)
	t.Cleanup(func() { stopBackendProcess(t, process) })
	ready := waitForBackendReady(t, process)
	_, port, err := net.SplitHostPort(ready)
	if err != nil {
		t.Fatalf("ready address %q: %v", ready, err)
	}
	base := "http://127.0.0.1:" + port
	waitForBackend(t, process, base)
	if err := process.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if process.cmd.ProcessState != nil {
		t.Fatalf("default backend exited after stdin EOF: %s", process.output.String())
	}
	waitForBackend(t, process, base)
	stopBackendProcess(t, process)
}

func waitForPortClosed(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("port %s remained open", address)
}

type backendProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	output *lockedBuffer
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	return startBackendProcessWithArgs(t, binary, listen, configPath, sessionDir)
}

func startBackendProcessWithArgs(t *testing.T, binary, listen, configPath, sessionDir string, args ...string) *backendProcess {
	t.Helper()
	return startBackendProcessWithEnv(t, binary, listen, configPath, sessionDir, nil, args...)
}

func startBackendProcessWithEnv(t *testing.T, binary, listen, configPath, sessionDir string, env []string, args ...string) *backendProcess {
	t.Helper()
	output := new(lockedBuffer)
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmdArgs := append([]string{"backend", "-listen", listen, "-config", configPath, "-session-dir", sessionDir}, args...)
	cmd := exec.Command(binary, cmdArgs...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = stdinReader
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		t.Fatalf("start backend: %v", err)
	}
	_ = stdinReader.Close()
	return &backendProcess{cmd: cmd, stdin: stdinWriter, output: output}
}

func waitForBackend(t *testing.T, process *backendProcess, base string) {
	t.Helper()
	waitForBackendHealth(t, process, base, "")
}

func waitForBackendHealthWithToken(t *testing.T, process *backendProcess, base, token string) {
	t.Helper()
	waitForBackendHealth(t, process, base, token)
}

func waitForBackendHealth(t *testing.T, process *backendProcess, base, token string) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if process.cmd.ProcessState != nil {
			t.Fatalf("backend exited before startup: %s", process.output.String())
		}
		request, err := http.NewRequest(http.MethodGet, base+"/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(request)
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

func waitForBackendReady(t *testing.T, process *backendProcess) string {
	t.Helper()
	const prefix = "k-brain backend listening on http://"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(process.output.String(), "\n") {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
		if process.cmd.ProcessState != nil {
			t.Fatalf("backend exited before startup: %s", process.output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("backend did not print ready line: %s", process.output.String())
	return ""
}

func stopBackendProcess(t *testing.T, process *backendProcess) {
	t.Helper()
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	_ = process.stdin.Close()
	if process.cmd.ProcessState != nil {
		return
	}
	if runtime.GOOS == "windows" {
		// Windows has no interrupt signal, so terminate the whole tree: the backend
		// spawns helpers, and a surviving one would keep using the same listen address.
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(process.cmd.Process.Pid), "/T", "/F")
		_ = kill.Run()
		_, _ = process.cmd.Process.Wait()
		return
	}
	if err := process.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("stop backend: %v", err)
	}
	waitBackendProcess(t, process, true)
}

func waitBackendProcess(t *testing.T, process *backendProcess, wantSuccess bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- process.cmd.Wait() }()
	select {
	case err := <-done:
		if wantSuccess && err != nil {
			t.Fatalf("backend exited with error: %v\n%s", err, process.output.String())
		}
		if !wantSuccess && err == nil {
			t.Fatalf("backend exited successfully, want failure")
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

func waitForAtomicCount(t *testing.T, counter *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("counter did not reach %d: got %d", want, counter.Load())
}

func logExecutableSSE(t *testing.T, label string, events []map[string]any) {
	t.Helper()
	terminal := make([]string, 0, 2)
	for _, event := range events {
		typeName, _ := event["type"].(string)
		if strings.HasPrefix(typeName, "run.") || typeName == "assistant.text.delta" {
			terminal = append(terminal, typeName)
		}
	}
	t.Logf("SSE %s: events=%d terminal=%v", label, len(events), terminal)
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
