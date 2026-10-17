package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Concurrent set requests on one idle session: exactly one is accepted and the goal
// left behind is the accepted one, never a rejected request's rollback.
func TestConcurrentGoalSetKeepsAcceptedGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	_, base := newGoalServer(t, client)
	for attempt := 0; attempt < 20; attempt++ {
		sess := createTestSession(t, base)
		start := make(chan struct{})
		accepted := make(chan protocol.GoalState, 16)
		var workers sync.WaitGroup
		for i := 0; i < 16; i++ {
			workers.Add(1)
			go func(i int) {
				defer workers.Done()
				<-start
				in := protocol.GoalRequest{Action: "set", Goal: fmt.Sprintf("goal-%d", i), Run: &protocol.PromptRequest{ClientRequestID: fmt.Sprintf("request-%d", i)}}
				body, _ := json.Marshal(in)
				resp, err := http.Post(base+"/v1/sessions/"+sess.ID+"/goal", "application/json", strings.NewReader(string(body)))
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var state protocol.GoalState
					if json.NewDecoder(resp.Body).Decode(&state) == nil {
						accepted <- state
					}
				}
			}(i)
		}
		close(start)
		workers.Wait()
		close(accepted)
		var success []protocol.GoalState
		for state := range accepted {
			success = append(success, state)
		}
		final := getGoal(t, base, sess.ID)
		if len(success) != 1 || final.Status != "active" || final.Goal != success[0].Goal {
			t.Fatalf("attempt=%d accepted=%+v final=%+v", attempt, success, final)
		}
		postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"})
	}
}
