package backend

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestGoalOldQuestionDoesNotPauseUpdatedGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"GOAL_NEEDS_INPUT — which title for A?", "GOAL_MET B is done"}}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "goal A", Run: &protocol.PromptRequest{ClientRequestID: "review-stale"}})
	waitTurns(t, client, 1)
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update", Goal: "goal B"}); status != http.StatusOK {
		t.Fatalf("update: %d", status)
	}
	client.mu.Lock()
	close(client.block)
	client.block = nil
	client.mu.Unlock()
	state := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" || s.Status == "met" })
	if state.Status != "met" {
		t.Fatalf("old question paused updated goal: %+v; turns=%q", state, client.userTurns())
	}
	if turns := client.userTurns(); len(turns) != 2 || !strings.Contains(turns[1], "goal B") {
		t.Fatalf("new goal was never evaluated: %q", turns)
	}
}
