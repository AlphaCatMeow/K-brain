package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// replyQueue answers each model request with the next scripted reply and records the
// last user message the model saw, including request-only snapshots.
type replyQueue struct {
	scriptedClient
	mu      sync.Mutex
	replies []string
	seen    []string
	block   chan struct{}
}

func (c *replyQueue) Clone() ai.Client { return c }

func (c *replyQueue) Stream(ctx context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			c.seen = append(c.seen, request.Messages[i].Content)
			break
		}
	}
	reply := "still working"
	if len(c.replies) > 0 {
		reply, c.replies = c.replies[0], c.replies[1:]
	}
	block := c.block
	c.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ai.Message{}, ai.Usage{}, ctx.Err()
		}
	}
	if onText != nil {
		onText(reply)
	}
	return ai.Message{Role: "assistant", Content: reply, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *replyQueue) userTurns() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func isolateUserDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func newGoalServer(t *testing.T, client ai.Client) (*session.Store, string) {
	t.Helper()
	isolateUserDir(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, newGoalServerWithStore(t, store, client)
}

func newGoalServerWithStore(t *testing.T, store *session.Store, client ai.Client) string {
	t.Helper()
	factory := func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider, ag.WorkingDir = selected.Model, selected.Provider, cwd
		return ag, nil
	}
	backendServer, err := New(Options{
		Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir(),
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return httpServer.URL
}

// readSSEUntil collects events from a live stream until ctx ends it.
func readSSEUntil(ctx context.Context, resp *http.Response) []protocol.Event {
	defer resp.Body.Close()
	var events []protocol.Event
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for ctx.Err() == nil && scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func postGoal(t *testing.T, base, id string, in protocol.GoalRequest) (int, protocol.GoalState) {
	t.Helper()
	var out protocol.GoalState
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/goal", in, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func firstUserMessage(t *testing.T, store *session.Store, id string) string {
	t.Helper()
	_, msgs, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range msgs {
		if message.Role == "user" {
			return message.Content
		}
	}
	return ""
}

func getGoal(t *testing.T, base, id string) protocol.GoalState {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + id + "/goal")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out protocol.GoalState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitGoal(t *testing.T, base, id string, done func(protocol.GoalState) bool) protocol.GoalState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := getGoal(t, base, id)
		if done(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("goal did not settle: %+v", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// goalEvents replays the session log; the stream closes after each run's terminal
// event, so it reconnects from the last sequence until nothing new arrives.
func goalEvents(t *testing.T, base, id string) []protocol.GoalState {
	t.Helper()
	var all []protocol.Event
	after := int64(0)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+id+"/events?after_seq="+strconv.FormatInt(after, 10), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		batch := readSSEUntil(ctx, resp)
		cancel()
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		after = batch[len(batch)-1].Seq
	}
	var out []protocol.GoalState
	for _, event := range all {
		if event.Type != protocol.EventGoalUpdated {
			continue
		}
		var state protocol.GoalState
		_ = json.Unmarshal(event.Payload, &state)
		out = append(out, state)
	}
	return out
}
func TestGoalRunsUntilMetAndKeepsInstructionsOutOfHistory(t *testing.T) {
	client := &replyQueue{replies: []string{"first pass", "more work", "GOAL_MET — shipped and verified"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)

	status, state := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship the feature", Run: &protocol.PromptRequest{ClientRequestID: "goal-1"}})
	if status != http.StatusOK || state.Status != "active" || state.Run == nil || state.Run.RunID == "" {
		t.Fatalf("set: %d %+v", status, state)
	}
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	if final.Goal != "" {
		t.Fatalf("met goal must be cleared: %+v", final)
	}
	turns := client.userTurns()
	if len(turns) != 3 || !strings.HasPrefix(turns[0], "/goal ship the feature") || !strings.Contains(turns[0], "Work toward it now") {
		t.Fatalf("model turns: %q", turns)
	}
	if first := firstUserMessage(t, store, sess.ID); first != "/goal ship the feature" {
		t.Fatalf("history must keep the typed command: %q", first)
	}
	for _, turn := range turns[1:] {
		if !strings.Contains(turn, "[goal check]") || !strings.Contains(turn, "GOAL_MET") {
			t.Fatalf("continuation must carry the goal check: %q", turn)
		}
	}
	meta, msgs, err := store.Load(sess.ID)
	if err != nil || meta.Goal != "" {
		t.Fatalf("durable goal: %q %v", meta.Goal, err)
	}
	for _, message := range msgs {
		if message.Role == "user" && strings.Contains(message.Content, "[goal check]") {
			t.Fatalf("history must keep only the visible round text: %q", message.Content)
		}
	}
	var statuses []string
	for _, event := range goalEvents(t, base, sess.ID) {
		statuses = append(statuses, event.Status)
		if event.Status == "continuing" && (!strings.HasPrefix(event.NextClientRequestID, "goal-"+sess.ID+"-") || event.NextPrompt == "") {
			t.Fatalf("continuing event must name the next round: %+v", event)
		}
	}
	if strings.Join(statuses, ",") != "active,continuing,continuing,met" {
		t.Fatalf("goal events: %v", statuses)
	}
}

func TestGoalPausesAtRoundLimitAndResumes(t *testing.T) {
	client := &replyQueue{}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := config.SetProjectGoalMaxRounds(sess.CWD, 2); err != nil {
		t.Fatal(err)
	}
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "never done", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
	paused := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "max_rounds" || paused.Round != 2 || paused.MaxRounds != 2 || paused.Goal != "never done" {
		t.Fatalf("paused: %+v", paused)
	}
	if n := len(client.userTurns()); n != 3 {
		t.Fatalf("goal + 2 checks expected, got %d", n)
	}

	client.mu.Lock()
	client.replies = []string{"GOAL_MET done"}
	client.mu.Unlock()
	status, resumed := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "g-2"}})
	if status != http.StatusOK || resumed.Status != "active" || resumed.Round != 0 {
		t.Fatalf("resume: %d %+v", status, resumed)
	}
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if last := turns[len(turns)-1]; !strings.HasPrefix(last, "/goal resume") || !strings.Contains(last, "never done") || !strings.Contains(last, "[goal check]") {
		t.Fatalf("resume round: %q", last)
	}
}

func TestGoalStopPausesAndClearDropsGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	_, state := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "s-1"}})
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "other", Run: &protocol.PromptRequest{ClientRequestID: "s-x"}}); status != http.StatusConflict {
		t.Fatalf("set while running: %d", status)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs/"+state.Run.RunID+"/cancel", map[string]any{}, nil)
	resp.Body.Close()
	paused := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "cancelled" {
		t.Fatalf("stop must pause, not continue: %+v", paused)
	}
	time.Sleep(100 * time.Millisecond)
	// The stop may land before the first model call; either way no round follows it.
	if n := len(client.userTurns()); n > 1 {
		t.Fatalf("no continuation after stop, got %d turns", n)
	}
	if status, cleared := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"}); status != http.StatusOK || cleared.Status != "cleared" {
		t.Fatalf("clear: %d %+v", status, cleared)
	}
	if got := getGoal(t, base, sess.ID); got.Status != "none" || got.Goal != "" {
		t.Fatalf("after clear: %+v", got)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "" {
		t.Fatalf("clear must drop the durable goal: %q", meta.Goal)
	}
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "s-2"}}); status != http.StatusConflict {
		t.Fatalf("resume without goal: %d", status)
	}
}

func TestGoalRestoredFromMetadataIsPaused(t *testing.T) {
	client := &replyQueue{}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := store.SetGoal(sess.ID, "from the TUI"); err != nil {
		t.Fatal(err)
	}
	// A backend that loads the session after the metadata write sees a paused goal.
	got := getGoal(t, newGoalServerWithStore(t, store, client), sess.ID)
	if got.Status != "paused" || got.Reason != "restored" || got.Goal != "from the TUI" {
		t.Fatalf("restored: %+v", got)
	}
}

func TestGoalRejectsBadRequests(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, in := range []protocol.GoalRequest{
		{Action: "nope"},
		{Action: "set", Goal: "x"},
		{Action: "set", Run: &protocol.PromptRequest{ClientRequestID: "r"}},
		{Action: "set", Goal: strings.Repeat("g", maxGoalBytes+1), Run: &protocol.PromptRequest{ClientRequestID: "r"}},
	} {
		if status, _ := postGoal(t, base, sess.ID, in); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", in.Action, status)
		}
	}
}

func TestGoalPauseStopsRoundAndUpdateKeepsPausedGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	_, started := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "p-1"}})
	if started.StartedAt == "" {
		t.Fatalf("started_at missing: %+v", started)
	}
	status, paused := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "pause"})
	if status != http.StatusOK || paused.Status != "paused" || paused.Reason != "user" {
		t.Fatalf("pause: %d %+v", status, paused)
	}
	// The cancelled round must not flip the goal back or start another round.
	time.Sleep(150 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "paused" || got.Reason != "user" {
		t.Fatalf("after pause: %+v", got)
	}
	if n := len(client.userTurns()); n > 1 {
		t.Fatalf("no continuation after pause, got %d turns", n)
	}

	status, updated := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update", Goal: "  shorter task  "})
	if status != http.StatusOK || updated.Goal != "shorter task" || updated.Status != "paused" {
		t.Fatalf("update: %d %+v", status, updated)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "shorter task" {
		t.Fatalf("durable goal after update: %q", meta.Goal)
	}
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update"}); status != http.StatusBadRequest {
		t.Fatalf("empty update: %d", status)
	}

	close(client.block)
	client.mu.Lock()
	client.block = nil
	client.replies = []string{"GOAL_MET done"}
	client.mu.Unlock()
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "p-2"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if last := turns[len(turns)-1]; !strings.Contains(last, "shorter task") {
		t.Fatalf("resume must use the updated goal: %q", last)
	}
}

func TestGoalPauseAndUpdateNeedAGoal(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, action := range []string{"pause", "update"} {
		if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: action, Goal: "x"}); status != http.StatusConflict {
			t.Fatalf("%s without goal: %d", action, status)
		}
	}
}

func TestGoalClearStopsRunningRound(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "c-1"}})
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"}); status != http.StatusOK {
		t.Fatalf("clear status %d", status)
	}
	// The blocked round only returns once its context is cancelled.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/goal", protocol.GoalRequest{Action: "set", Goal: "next", Run: &protocol.PromptRequest{ClientRequestID: "c-2"}}, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clear must stop the running round; last status %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
