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

func TestComputerApprovalRule(t *testing.T) {
	for _, tc := range []struct {
		command, rule          string
		readOnly, rememberable bool
	}{
		{`{"action":"discover"}`, "discover", true, false},
		{`{"action":"describe","tool":"click"}`, "describe", true, false},
		{`{"action":"guide","guide":"SKILL.md"}`, "guide", true, false},
		{`{"action":"call","tool":" click ","arguments":{}}`, "call:click", false, true},
		{`{"action":"call","tool":"run_actions"}`, "call:run_actions", false, false},
		{`{"action":"call","tool":"replay_trajectory"}`, "call:replay_trajectory", false, false},
		{`{"action":"call","tool":"install_extension"}`, "call:install_extension", false, false},
		{`{"action":"call"}`, "", false, false},
		{`{"action":"launch"}`, "", false, false},
		{`click(1, 2)`, "", false, false},
	} {
		rule, readOnly, rememberable := computerApprovalRule(tc.command)
		if rule != tc.rule || readOnly != tc.readOnly || rememberable != tc.rememberable {
			t.Errorf("%s: got %q/%v/%v", tc.command, rule, readOnly, rememberable)
		}
	}
}

// computerSequenceClient issues one computer_exec call per model turn, then finishes.
type computerSequenceClient struct {
	scriptedClient
	lock  sync.Mutex
	calls []string
	next  int
}

func (c *computerSequenceClient) Stream(_ context.Context, _ ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.next < len(c.calls) {
		call := ai.ToolCall{ID: fmt.Sprintf("computer-%d", c.next), Type: "function"}
		call.Function.Name, call.Function.Arguments = "computer_exec", c.calls[c.next]
		c.next++
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *computerSequenceClient) queue(calls ...string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.calls = append(c.calls, calls...)
}

type computerApprovalFixture struct {
	t        *testing.T
	base     string
	session  protocol.Session
	client   *computerSequenceClient
	lock     sync.Mutex
	executed []string
	lastSeq  int64
}

func newComputerApprovalFixture(t *testing.T, policy string) *computerApprovalFixture {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := &computerApprovalFixture{t: t, client: &computerSequenceClient{}}
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
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	f.base = httpServer.URL
	f.session = createTestSession(t, httpServer.URL)
	return f
}

// run executes one turn and answers each permission request with answer(request).
// It returns the permission requests in order and how many computer calls ran.
func (f *computerApprovalFixture) run(options protocol.RunOptions, answer func(protocol.PermissionRequest) string, calls ...string) ([]protocol.PermissionRequest, int) {
	t := f.t
	t.Helper()
	f.client.queue(calls...)
	f.lock.Lock()
	before := len(f.executed)
	f.lock.Unlock()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, f.base+"/v1/sessions/"+f.session.ID+"/runs", protocol.PromptRequest{ConversationID: f.session.ID, ClientRequestID: fmt.Sprintf("computer-%d", f.lastSeq), Prompt: "use the computer", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run: %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", f.base, f.session.ID, f.lastSeq), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	var requests []protocol.PermissionRequest
	terminal := ""
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() && terminal == "" {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		f.lastSeq = event.Seq
		if event.RunID != accepted.RunID {
			continue
		}
		switch event.Type {
		case protocol.EventPermissionRequest:
			var permission protocol.PermissionRequest
			if err := json.Unmarshal(event.Payload, &permission); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, permission)
			decision := postJSON(t, http.DefaultClient, f.base+"/v1/sessions/"+f.session.ID+"/permissions/"+permission.PermissionID, protocol.PermissionDecisionRequest{
				ConversationID: f.session.ID,
				RunID:          accepted.RunID,
				Decision:       protocol.PermissionDecision{PermissionID: permission.PermissionID, Decision: answer(permission)},
			}, nil)
			if decision.StatusCode != http.StatusOK {
				t.Fatalf("permission status %d", decision.StatusCode)
			}
			decision.Body.Close()
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			terminal = event.Type
		}
	}
	if terminal != protocol.EventRunCompleted {
		t.Fatalf("run ended with %q", terminal)
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	return requests, len(f.executed) - before
}

func allowOnce(protocol.PermissionRequest) string { return "allow_once" }

func promptRules(requests []protocol.PermissionRequest) string {
	var rules []string
	for _, request := range requests {
		rules = append(rules, fmt.Sprintf("%s/%s", request.Rule, request.Scope))
	}
	return strings.Join(rules, " ")
}

func TestComputerFirstApprovalAllowsSessionComputerUse(t *testing.T) {
	f := newComputerApprovalFixture(t, "ask")
	// LiveAgent's most permissive mode must not bypass the backend "ask" policy.
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", PermissionMode: "always"}
	requests, executed := f.run(options, allowOnce,
		`{"action":"discover"}`,
		`{"action":"describe","tool":"click"}`,
		`{"action":"call","tool":"click","arguments":{"x":1}}`,
		`{"action":"call","tool":"type_text","arguments":{"text":"a"}}`,
		`{"action":"call","tool":"list_windows","arguments":{}}`,
		`{"action":"call","tool":"run_actions","arguments":{}}`,
		`{"action":"call","tool":"replay_trajectory","arguments":{}}`,
		`{"action":"call","tool":"click","arguments":{"x":2}}`,
	)
	if executed != 8 {
		t.Fatalf("executed %d computer calls, want 8", executed)
	}
	// Only the first call and the always-ask tools prompt; every prompt names the session scope.
	want := "call:click/session call:run_actions/session call:replay_trajectory/session"
	if got := promptRules(requests); got != want {
		t.Fatalf("permission prompts %q, want %q", got, want)
	}
	for _, request := range requests {
		if len(request.Options) != 2 || request.Options[0].ID != "allow_once" || request.Options[0].Label != "Allow for this session" {
			t.Fatalf("options %+v", request.Options)
		}
	}

	// The grant outlives the run; batch and install tools keep asking.
	requests, executed = f.run(options, allowOnce,
		`{"action":"call","tool":"scroll","arguments":{}}`,
		`{"action":"call","tool":"install_extension","arguments":{}}`,
	)
	if executed != 2 || promptRules(requests) != "call:install_extension/session" {
		t.Fatalf("second run executed %d with prompts %q", executed, promptRules(requests))
	}
}

func TestComputerRejectionGrantsNothing(t *testing.T) {
	f := newComputerApprovalFixture(t, "ask")
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask"}
	reject := func(protocol.PermissionRequest) string { return "reject" }
	requests, executed := f.run(options, reject,
		`{"action":"call","tool":"click","arguments":{}}`,
		`{"action":"call","tool":"list_windows","arguments":{}}`,
	)
	if executed != 0 || promptRules(requests) != "call:click/session call:list_windows/session" {
		t.Fatalf("executed %d with prompts %q", executed, promptRules(requests))
	}
}

func TestComputerNonCallPromptsCarryNoSessionScope(t *testing.T) {
	f := newComputerApprovalFixture(t, "ask")
	// A legacy pseudocode command has no Cua call rule, so approving it grants nothing more.
	requests, executed := f.run(protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask"}, allowOnce, `click(1, 2)`, `click(3, 4)`)
	if executed != 2 || len(requests) != 2 || requests[0].Scope != "" || requests[0].Options[0].Label != "Allow once" {
		t.Fatalf("executed %d with prompts %+v", executed, requests)
	}
}

func TestComputerBackendPolicyAllowAndDenyBoundaries(t *testing.T) {
	never := func(request protocol.PermissionRequest) string {
		t.Errorf("unexpected permission prompt %+v", request)
		return "reject"
	}
	call := `{"action":"call","tool":"click","arguments":{}}`

	// LiveAgent's default run options: normal permission mode, no per-tool policy.
	allow := newComputerApprovalFixture(t, "allow")
	if _, executed := allow.run(protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "normal"}, never, call); executed != 1 {
		t.Fatalf("backend allow policy still blocked computer use: executed %d", executed)
	}
	runDeny := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", Tools: &protocol.ToolSelection{Policies: map[string]string{"computer_exec": "deny"}}}
	if _, executed := allow.run(runDeny, never, call, `{"action":"discover"}`); executed != 0 {
		t.Fatalf("run-level deny bypassed: executed %d", executed)
	}
	plan := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", PlanModeEnabled: true}
	if _, executed := allow.run(plan, never, call); executed != 0 {
		t.Fatalf("plan mode bypassed: executed %d", executed)
	}

	ask := newComputerApprovalFixture(t, "ask")
	if _, executed := ask.run(plan, never, `{"action":"discover"}`); executed != 0 {
		t.Fatalf("plan mode allowed a read-only computer action: executed %d", executed)
	}

	deny := newComputerApprovalFixture(t, "deny")
	if _, executed := deny.run(protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", PermissionMode: "always"}, never, `{"action":"discover"}`, call); executed != 0 {
		t.Fatalf("backend deny policy bypassed: executed %d", executed)
	}
}
