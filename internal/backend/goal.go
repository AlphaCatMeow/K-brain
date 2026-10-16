package backend

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

const maxGoalBytes = 16 * 1024

// goalRuntime is the in-memory goal loop of one session. The goal text is durable in
// session metadata; status and round are not, so a reloaded goal comes back paused and
// needs an explicit resume, matching the TUI.
type goalRuntime struct {
	loaded bool
	text   string
	status string
	round  int
	epoch  int
	reason string
	// started is when the goal was set or last resumed.
	started time.Time
	// next is the round decided for the run that just completed; the run worker starts
	// it once the session is idle again.
	next *goalContinuation
}

type goalContinuation struct {
	afterRunID      string
	clientRequestID string
	prompt          string
}

func (s *Server) goalMaxRoundsLocked(rt *runtimeSession) int {
	global := 0
	if s.settings != nil {
		if cfg := s.settings.Snapshot(); cfg != nil {
			global = cfg.GoalMaxRounds
		}
	}
	dir := ""
	if rt.agent != nil {
		dir = rt.agent.WorkingDir
	}
	return goalloop.MaxRounds(dir, global)
}

func (s *Server) ensureGoalLoadedLocked(rt *runtimeSession) {
	if rt.goal.loaded {
		return
	}
	rt.goal.loaded = true
	rt.goal.status = "none"
	if meta, _, err := s.store.Load(rt.id); err == nil && strings.TrimSpace(meta.Goal) != "" {
		rt.goal.text = meta.Goal
		rt.goal.status, rt.goal.reason = "paused", "restored"
	}
}

func (s *Server) goalStateLocked(rt *runtimeSession) protocol.GoalState {
	state := protocol.GoalState{
		Version: protocol.Version, ConversationID: rt.id, Goal: rt.goal.text,
		Status: rt.goal.status, Round: rt.goal.round, MaxRounds: s.goalMaxRoundsLocked(rt), Reason: rt.goal.reason,
	}
	if !rt.goal.started.IsZero() && rt.goal.text != "" {
		state.StartedAt = rt.goal.started.UTC().Format(time.RFC3339)
	}
	if next := rt.goal.next; next != nil {
		state.Status = "continuing"
		state.NextClientRequestID, state.NextPrompt = next.clientRequestID, next.prompt
	}
	return state
}

// goalRoundPrompt is the visible user text of a goal-check round; the full goal-check
// instructions travel as a request-only snapshot so the transcript stays readable.
func goalRoundPrompt(goal string, round, max int) string {
	return fmt.Sprintf("[goal %d/%d] %s", round, max, goal)
}

func (s *Server) handleGoal(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	if r.Method == http.MethodGet {
		rt.mu.Lock()
		s.ensureGoalLoadedLocked(rt)
		state := s.goalStateLocked(rt)
		rt.mu.Unlock()
		writeJSON(w, http.StatusOK, state)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in protocol.GoalRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	switch in.Action {
	case "clear":
		s.clearGoal(w, rt)
	case "pause":
		s.pauseGoal(w, rt)
	case "update":
		s.updateGoal(w, rt, in.Goal)
	case "set", "resume":
		s.startGoal(w, rt, in)
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be set, resume, pause, update or clear")
	}
}

// pauseGoal stops the running round, if any, and drops any pending continuation; the
// goal text stays durable so resume can pick it up.
func (s *Server) pauseGoal(w http.ResponseWriter, rt *runtimeSession) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.goal.text == "" {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no goal to pause")
		return
	}
	// Mark paused first so the cancelled round's evaluation does not continue it.
	rt.goal.status, rt.goal.reason, rt.goal.next = "paused", "user", nil
	var cancel context.CancelFunc
	if rt.cancel != nil && !rt.runDone {
		cancel = rt.cancel
	}
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}

// updateGoal replaces the goal text. A running goal keeps going and later rounds use
// the new text; a paused goal stays paused until resumed.
func (s *Server) updateGoal(w http.ResponseWriter, rt *runtimeSession, raw string) {
	goal := strings.TrimSpace(raw)
	if goal == "" || len(goal) > maxGoalBytes {
		writeJSONError(w, http.StatusBadRequest, "goal is required and must be within the size limit")
		return
	}
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.goal.text == "" {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no goal to update")
		return
	}
	if err := s.store.SetGoal(rt.id, goal); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rt.goal.text = goal
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) clearGoal(w http.ResponseWriter, rt *runtimeSession) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if err := s.store.SetGoal(rt.id, ""); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A goal round still running is stopped too; a plain user run is left alone.
	var cancel context.CancelFunc
	if (rt.goal.status == "active" || rt.goal.next != nil) && rt.cancel != nil && !rt.runDone {
		cancel = rt.cancel
	}
	rt.goal = goalRuntime{loaded: true, status: "cleared", epoch: rt.goal.epoch + 1}
	state := s.goalStateLocked(rt)
	rt.goal.status = "none"
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}
func (s *Server) startGoal(w http.ResponseWriter, rt *runtimeSession, in protocol.GoalRequest) {
	if in.Run == nil || strings.TrimSpace(in.Run.ClientRequestID) == "" {
		writeJSONError(w, http.StatusBadRequest, "run.client_request_id is required")
		return
	}
	goal := strings.TrimSpace(in.Goal)
	if in.Action == "set" && goal == "" {
		writeJSONError(w, http.StatusBadRequest, "goal is required")
		return
	}
	if len(goal) > maxGoalBytes {
		writeJSONError(w, http.StatusBadRequest, "goal is too long")
		return
	}
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.activeLocked() {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "session already has a running turn")
		return
	}
	if in.Action == "resume" {
		goal = rt.goal.text
		if goal == "" {
			rt.mu.Unlock()
			writeJSONError(w, http.StatusConflict, "no goal to resume")
			return
		}
	} else if err := s.store.SetGoal(rt.id, goal); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	previous := rt.goal
	rt.goal = goalRuntime{loaded: true, text: goal, status: "active", epoch: previous.epoch + 1, started: time.Now()}
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()

	run := *in.Run
	run.ConversationID, run.PromptCommand, run.Content = rt.id, false, nil
	// History keeps what the user typed; the model gets the goal itself (first round, as
	// in the TUI) or the goal check (resume) as request-only text.
	if in.Action == "set" {
		run.Prompt = "/goal " + goal
		run.Snapshot = "The user set this session goal with /goal. Work toward it now:\n" + goal
	} else {
		run.Prompt, run.Snapshot = "/goal resume", goalloop.ContinuePrompt(goal)
	}
	// Publish before the run starts so this event precedes the decision of its first round.
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	accepted, status, err := s.startCanonicalRun(context.Background(), rt, run)
	if err != nil {
		rt.mu.Lock()
		if in.Action == "set" {
			_ = s.store.SetGoal(rt.id, previous.text)
		}
		rt.goal = previous
		rt.goal.epoch++
		reverted := s.goalStateLocked(rt)
		rt.mu.Unlock()
		_, _ = s.publish(rt, protocol.EventGoalUpdated, reverted, "")
		writeJSONError(w, status, err.Error())
		return
	}
	state.Run = &accepted
	writeJSON(w, http.StatusOK, state)
}

// evaluateGoal runs inside executeRun before the terminal event, so a client streaming
// the run sees the decision first. outcome is completed, cancelled or failed.
func (s *Server) evaluateGoal(rt *runtimeSession, runID, outcome string) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.goal.status != "active" {
		rt.mu.Unlock()
		return
	}
	switch {
	case outcome != "completed":
		// A stopped or failed round pauses the goal; only an explicit resume restarts it.
		rt.goal.status, rt.goal.reason = "paused", outcome
	case goalMetBy(rt):
		_ = s.store.SetGoal(rt.id, "")
		rt.goal.status, rt.goal.reason = "met", ""
	case rt.goal.round >= s.goalMaxRoundsLocked(rt):
		rt.goal.status, rt.goal.reason = "paused", "max_rounds"
	default:
		rt.goal.round++
		rt.goal.next = &goalContinuation{
			afterRunID:      runID,
			clientRequestID: fmt.Sprintf("goal-%s-%d-%d", rt.id, rt.goal.epoch, rt.goal.round),
			prompt:          goalRoundPrompt(rt.goal.text, rt.goal.round, s.goalMaxRoundsLocked(rt)),
		}
	}
	state := s.goalStateLocked(rt)
	if rt.goal.status == "met" {
		// Met clears the durable goal; the event still carries the text for the notice.
		rt.goal.text = ""
	}
	rt.mu.Unlock()
	_, _ = rt.publish(protocol.EventGoalUpdated, state, "")
}

func goalMetBy(rt *runtimeSession) bool {
	if rt.agent == nil {
		return false
	}
	messages := rt.agent.MessagesSnapshot()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != protocol.RoleAssistant {
			continue
		}
		return messages[i].StopReason != ai.StopReasonLength && goalloop.Met(messages[i].TextContent())
	}
	return false
}

// continueGoal starts the round decided by evaluateGoal once the finished run has
// released the session. A clear or a new goal in between drops it; a failed start
// pauses the goal.
func (s *Server) continueGoal(rt *runtimeSession, runID string, previous protocol.PromptRequest) {
	rt.mu.Lock()
	next := rt.goal.next
	if next == nil || next.afterRunID != runID {
		rt.mu.Unlock()
		return
	}
	rt.goal.next = nil
	if rt.goal.status != "active" {
		rt.mu.Unlock()
		return
	}
	goal := rt.goal.text
	rt.mu.Unlock()

	run := protocol.PromptRequest{
		ConversationID: rt.id, ClientRequestID: next.clientRequestID, Prompt: next.prompt,
		Options: previous.Options, HookPolicy: previous.HookPolicy, HookScopeID: previous.HookScopeID,
		Snapshot: goalloop.ContinuePrompt(goal),
	}
	if _, _, err := s.startCanonicalRun(context.Background(), rt, run); err != nil {
		rt.mu.Lock()
		if rt.goal.status == "active" {
			rt.goal.status, rt.goal.reason = "paused", err.Error()
		}
		state := s.goalStateLocked(rt)
		rt.mu.Unlock()
		_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	}
}
