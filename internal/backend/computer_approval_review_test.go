package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// reviewComputerClient returns every call of a turn in one assistant message, so the
// agent executes those calls concurrently, then finishes the run.
type reviewComputerClient struct {
	scriptedClient
	lock   sync.Mutex
	turns  [][]string
	next   int
	issued int
}

func (c *reviewComputerClient) Stream(_ context.Context, _ ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.next < len(c.turns) {
		message := ai.Message{Role: "assistant"}
		for _, args := range c.turns[c.next] {
			call := ai.ToolCall{ID: fmt.Sprintf("computer-%d", c.issued), Type: "function"}
			call.Function.Name, call.Function.Arguments = "computer_exec", args
			message.ToolCalls = append(message.ToolCalls, call)
			c.issued++
		}
		c.next++
		return message, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *reviewComputerClient) queue(turns ...[]string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.turns = append(c.turns, turns...)
}

// reviewComputerFixture mirrors newComputerApprovalFixture but exposes the server and
// lets a test observe and answer prompts in its own order.
type reviewComputerFixture struct {
	t        *testing.T
	server   *Server
	base     string
	session  protocol.Session
	client   *reviewComputerClient
	lock     sync.Mutex
	executed []string
	runs     int
	lastSeq  int64
}

func newReviewComputerFixture(t *testing.T, policy string, settings *config.Config) *reviewComputerFixture {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := &reviewComputerFixture{t: t, client: &reviewComputerClient{}}
	server, err := New(Options{Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		a := agent.New(f.client, "fixture-model", 100, "fixture", agent.WithComputerConfig(config.ComputerConfig{ApprovalPolicy: policy}))
		a.Tools = []tools.Tool{{Def: tools.ComputerExec().Def, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := tools.Authorize(ctx, "computer_exec", string(raw)); err != nil {
				return "", err
			}
			f.lock.Lock()
			f.executed = append(f.executed, string(raw))
			f.lock.Unlock()
			return "observed", nil
		}}}
		return a, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	if settings != nil {
		server.settings = NewSettingsStore(settings, nil)
	}
	f.server = server
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	f.base = httpServer.URL
	f.session = createTestSession(t, httpServer.URL)
	return f
}

func (f *reviewComputerFixture) executedCount() int {
	f.lock.Lock()
	defer f.lock.Unlock()
	return len(f.executed)
}

func (f *reviewComputerFixture) start(options protocol.RunOptions, turns ...[]string) protocol.RunAccepted {
	f.t.Helper()
	f.client.queue(turns...)
	f.runs++
	var accepted protocol.RunAccepted
	resp := postJSON(f.t, http.DefaultClient, f.base+"/v1/sessions/"+f.session.ID+"/runs", protocol.PromptRequest{ConversationID: f.session.ID, ClientRequestID: fmt.Sprintf("review-%d", f.runs), Prompt: "use the computer", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		f.t.Fatalf("run: %d", resp.StatusCode)
	}
	return accepted
}

// events streams the session's events after lastSeq until ctx ends or the stream closes.
func (f *reviewComputerFixture) events(ctx context.Context) <-chan protocol.Event {
	f.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", f.base, f.session.ID, f.lastSeq), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	out := make(chan protocol.Event, 64)
	go func() {
		defer close(out)
		defer stream.Body.Close()
		scanner := bufio.NewScanner(stream.Body)
		scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event protocol.Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				return
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (f *reviewComputerFixture) decide(runID string, request protocol.PermissionRequest, decision string) {
	f.t.Helper()
	resp := postJSON(f.t, http.DefaultClient, f.base+"/v1/sessions/"+f.session.ID+"/permissions/"+request.PermissionID, protocol.PermissionDecisionRequest{
		ConversationID: f.session.ID,
		RunID:          runID,
		Decision:       protocol.PermissionDecision{PermissionID: request.PermissionID, Decision: decision},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("permission status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func isTerminalEvent(typ string) bool {
	return typ == protocol.EventRunCompleted || typ == protocol.EventRunFailed || typ == protocol.EventRunCancelled
}

// run executes the queued turns as one run, answering every prompt with answer, and
// returns the prompts in order plus the number of computer calls that executed.
func (f *reviewComputerFixture) run(options protocol.RunOptions, answer func(protocol.PermissionRequest) string, turns ...[]string) ([]protocol.PermissionRequest, int) {
	f.t.Helper()
	before := f.executedCount()
	accepted := f.start(options, turns...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var requests []protocol.PermissionRequest
	terminal := ""
	for event := range f.events(ctx) {
		f.lastSeq = event.Seq
		if event.RunID != accepted.RunID {
			continue
		}
		if isTerminalEvent(event.Type) {
			terminal = event.Type
			break
		}
		if event.Type != protocol.EventPermissionRequest {
			continue
		}
		var permission protocol.PermissionRequest
		if err := json.Unmarshal(event.Payload, &permission); err != nil {
			f.t.Fatal(err)
		}
		requests = append(requests, permission)
		f.decide(accepted.RunID, permission, answer(permission))
	}
	if terminal != protocol.EventRunCompleted {
		f.t.Fatalf("run ended with %q", terminal)
	}
	return requests, f.executedCount() - before
}

// grantRecorded reads the in-memory session grant of the loaded runtime.
func (f *reviewComputerFixture) grantRecorded() bool {
	f.t.Helper()
	f.server.mu.Lock()
	rt := f.server.sessions[f.session.ID]
	f.server.mu.Unlock()
	if rt == nil {
		f.t.Fatal("runtime is not loaded")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.computerAllowAll
}

func reviewClick(x int) string {
	return fmt.Sprintf(`{"action":"call","tool":"click","arguments":{"x":%d}}`, x)
}

// docs/computer-use-cua.md: "A run that sets tools.policies.computer_exec to ask itself is
// asked for every action, including read-only ones". That must hold for every permission
// mode; the "always" mode is exactly the case a user picks when they want the backend grant
// everywhere except where they opted into asking.
func TestReviewComputerExplicitRunAskHonoredInEveryPermissionMode(t *testing.T) {
	for _, backend := range []string{"ask", "allow"} {
		for _, mode := range []string{"normal", "auto_edit", "full_access", "always"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				f := newReviewComputerFixture(t, backend, nil)
				explicitAsk := protocol.RunOptions{
					Mode: "agent", ApprovalPolicy: "ask", PermissionMode: mode,
					Tools: &protocol.ToolSelection{Policies: map[string]string{"computer_exec": "ask"}},
				}
				requests, executed := f.run(explicitAsk, allowOnce, []string{`{"action":"discover"}`}, []string{reviewClick(1)})
				if executed != 2 {
					t.Fatalf("executed %d computer calls, want 2", executed)
				}
				if got := promptRules(requests); got != "discover/ call:click/" {
					t.Fatalf("explicit computer_exec=ask under backend %s and permission mode %s prompted %q, want every action asked once", backend, mode, got)
				}
				if f.grantRecorded() {
					t.Fatal("explicit ask approvals became a session grant")
				}
			})
		}
	}
}

// Two Cua calls issued in one model turn prompt concurrently. Each prompt needs its own
// permission_id: waitPermission keys rt.permissions by newRunID(), which is only the wall
// clock, so two prompts raised in the same clock tick share one id, the second waiter
// overwrites the first in the map, one decision resolves only the survivor and the other
// call waits until the run is cancelled. ToolCallID cannot help a client here either,
// because the decision endpoint is addressed by permission_id alone.
func TestReviewComputerConcurrentPromptsAreIndependentlyAnswerable(t *testing.T) {
	f := newReviewComputerFixture(t, "ask", nil)
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "normal"}
	accepted := f.start(options, []string{reviewClick(1), reviewClick(2)})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	events := f.events(ctx)
	var pending []protocol.PermissionRequest
	for len(pending) < 2 {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before both prompts arrived")
			}
			f.lastSeq = event.Seq
			if event.RunID != accepted.RunID {
				continue
			}
			if isTerminalEvent(event.Type) {
				t.Fatalf("run ended with %q before both prompts arrived", event.Type)
			}
			if event.Type == protocol.EventPermissionRequest {
				var permission protocol.PermissionRequest
				if err := json.Unmarshal(event.Payload, &permission); err != nil {
					t.Fatal(err)
				}
				pending = append(pending, permission)
			}
		case <-ctx.Done():
			t.Fatalf("only %d prompts arrived", len(pending))
		}
	}
	for _, request := range pending {
		if request.Scope != "session" || request.Options[0].Label != "Allow for this session" || !strings.HasPrefix(request.ToolCallID, "computer-") {
			t.Fatalf("prompt %+v does not offer the session grant for its tool call", request)
		}
	}
	if pending[0].ToolCallID == pending[1].ToolCallID {
		t.Fatalf("both prompts claim tool call %q", pending[0].ToolCallID)
	}
	waitTerminal := func(limit time.Duration) string {
		deadline := time.After(limit)
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return "stream closed"
				}
				f.lastSeq = event.Seq
				if event.RunID == accepted.RunID && isTerminalEvent(event.Type) {
					return event.Type
				}
			case <-deadline:
				return ""
			}
		}
	}
	if pending[0].PermissionID == pending[1].PermissionID {
		t.Fatalf("concurrent prompts for tool calls %s and %s share permission_id %s", pending[0].ToolCallID, pending[1].ToolCallID, pending[0].PermissionID)
	}
	// One session grant also settles the other prompt that is already waiting.
	f.decide(accepted.RunID, pending[0], "allow_once")
	if terminal := waitTerminal(10 * time.Second); terminal != protocol.EventRunCompleted || f.executedCount() != 2 {
		t.Fatalf("run ended with %q after %d computer calls", terminal, f.executedCount())
	}
	second := postJSON(t, http.DefaultClient, f.base+"/v1/sessions/"+f.session.ID+"/permissions/"+pending[1].PermissionID, protocol.PermissionDecisionRequest{ConversationID: f.session.ID, RunID: accepted.RunID, Decision: protocol.PermissionDecision{PermissionID: pending[1].PermissionID, Decision: "reject"}}, nil)
	second.Body.Close()
	if second.StatusCode != http.StatusNotFound {
		t.Fatalf("second prompt was still pending after the session grant: HTTP %d", second.StatusCode)
	}
}

// docs/computer-use-cua.md: the grant lasts "for the rest of the loaded session". Once the
// runtime reaper unloads the session, the next load must ask again.
func TestReviewComputerGrantEndsWhenRuntimeUnloads(t *testing.T) {
	f := newReviewComputerFixture(t, "ask", nil)
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "normal"}
	if requests, executed := f.run(options, allowOnce, []string{reviewClick(1)}); executed != 1 || promptRules(requests) != "call:click/session" {
		t.Fatalf("first run executed %d with prompts %q", executed, promptRules(requests))
	}
	if requests, executed := f.run(options, allowOnce, []string{reviewClick(2)}); executed != 1 || len(requests) != 0 {
		t.Fatalf("grant not applied: executed %d with prompts %q", executed, promptRules(requests))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.server.reapRuntimes(time.Now().Add(runtimeIdleTTL))
		f.server.mu.Lock()
		_, loaded := f.server.sessions[f.session.ID]
		f.server.mu.Unlock()
		if !loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle runtime was not unloaded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requests, executed := f.run(options, allowOnce, []string{reviewClick(3)}); executed != 1 || promptRules(requests) != "call:click/session" {
		t.Fatalf("grant survived the runtime unload: executed %d with prompts %q", executed, promptRules(requests))
	}
}

// An AI reviewer's manual-review verdict (auto review) prompts once per call through the review hook;
// approving it never records the session grant, and each prompt names its tool call.
func TestReviewComputerManualReviewApprovalGrantsNothing(t *testing.T) {
	highRisk := strings.Replace(reviewLow, `"risk":"low"`, `"risk":"high"`, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": highRisk}}}})
	}))
	defer provider.Close()
	cfg := &config.Config{
		AIApproval: config.AIApprovalConfig{Mode: "auto", Provider: "fixture", Model: "small"},
		Providers:  map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}},
		Models:     map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}},
	}
	f := newReviewComputerFixture(t, "ask", cfg)
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "auto_review"}
	requests, executed := f.run(options, allowOnce, []string{reviewClick(1)}, []string{reviewClick(2)})
	if executed != 2 || promptRules(requests) != "call:click/ call:click/" {
		t.Fatalf("manual review executed %d with prompts %q", executed, promptRules(requests))
	}
	for i, request := range requests {
		if request.Options[0].Label != "Allow once" || !strings.Contains(request.Description, "AI review") {
			t.Fatalf("manual review prompt %+v", request)
		}
		if want := fmt.Sprintf("computer-%d", i); request.ToolCallID != want {
			t.Fatalf("prompt %d tied to %q, want %q", i, request.ToolCallID, want)
		}
	}
	if f.grantRecorded() {
		t.Fatal("manual review approvals became a session grant")
	}
}

// Classification is conservative: anything that is not a well-formed call keyed by a
// trimmed tool name gets no rule, so it prompts every time and grants nothing.
func TestReviewComputerApprovalRuleAdversarialInputs(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 500) + `1` + strings.Repeat(`}`, 500)
	for _, tc := range []struct {
		command, rule          string
		readOnly, rememberable bool
	}{
		{`{"action":"call","tool":"run_actions "}`, "call:run_actions", false, false},
		{`{"action":"call","tool":"\t replay_trajectory\n"}`, "call:replay_trajectory", false, false},
		{`{"action":"call","tool":"install_ffmpeg"}`, "call:install_ffmpeg", false, false},
		// Case is preserved: cua.Client.Describe/Call match advertised names exactly, so a
		// miscased name never reaches the driver even though it is classified rememberable.
		{`{"action":"call","tool":"Run_Actions"}`, "call:Run_Actions", false, true},
		{`{"action":"call","tool":"click","arguments":` + deep + `}`, "call:click", false, true},
		{`{"action":"call","tool":"click","arguments":{"big":"` + strings.Repeat("x", 1<<20) + `"}}`, "call:click", false, true},
		// Duplicate keys: last one wins, the same way the tool's own decoder reads them.
		{`{"action":"call","tool":"click","action":"discover"}`, "discover", true, false},
		{`{"tool":"click"}`, "", false, false},
		{`{"action":"call ","tool":"click"}`, "", false, false},
		{`{"action":"Call","tool":"click"}`, "", false, false},
		{`{"action":"call","tool":123}`, "", false, false},
		{`{"action":"call","tool":"   "}`, "", false, false},
		{``, "", false, false},
		{`null`, "", false, false},
		{`[]`, "", false, false},
		{`{"action":"discover"`, "", false, false},
	} {
		rule, readOnly, rememberable := computerApprovalRule(tc.command)
		if rule != tc.rule || readOnly != tc.readOnly || rememberable != tc.rememberable {
			t.Errorf("%.80s: got %q/%v/%v, want %q/%v/%v", tc.command, rule, readOnly, rememberable, tc.rule, tc.readOnly, tc.rememberable)
		}
	}
}
