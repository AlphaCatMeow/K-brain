package backend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestComputerSessionGrantKeepsPendingBatchAndInstallPrompts(t *testing.T) {
	for _, tool := range []string{"run_actions", "replay_trajectory", "install_ffmpeg", "install_extension"} {
		t.Run(tool, func(t *testing.T) {
			f := newReviewComputerFixture(t, "ask", nil)
			accepted := f.start(protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "normal"}, []string{reviewClick(1), `{"action":"call","tool":"` + tool + `","arguments":{}}`})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			events := f.events(ctx)
			var pending []protocol.PermissionRequest
			for len(pending) < 2 {
				select {
				case event, ok := <-events:
					if !ok {
						t.Fatal("event stream closed before prompts")
					}
					if event.Type == protocol.EventPermissionRequest {
						var request protocol.PermissionRequest
						if err := json.Unmarshal(event.Payload, &request); err != nil {
							t.Fatal(err)
						}
						pending = append(pending, request)
					}
				case <-ctx.Done():
					t.Fatal("missing prompts")
				}
			}
			for _, request := range pending {
				if request.Rule == "call:click" {
					f.decide(accepted.RunID, request, "allow_once")
				}
			}
			for {
				select {
				case event, ok := <-events:
					if !ok {
						t.Fatal("event stream closed before click result")
					}
					if isTerminalEvent(event.Type) {
						t.Fatal("run ended without a decision on the excluded tool")
					}
					if event.Type != protocol.EventToolResult {
						continue
					}
					var result protocol.ToolResultEvent
					if err := json.Unmarshal(event.Payload, &result); err != nil {
						t.Fatal(err)
					}
					if result.ToolResult.ID != "computer-0" {
						t.Fatal("excluded tool executed without approval")
					}
				case <-ctx.Done():
					t.Fatal("missing click result")
				}
				break
			}
			f.server.mu.Lock()
			rt := f.server.sessions[f.session.ID]
			f.server.mu.Unlock()
			for _, request := range pending {
				if request.Rule != "call:"+tool {
					continue
				}
				rt.mu.Lock()
				waiter := rt.permissions[request.PermissionID]
				rt.mu.Unlock()
				if waiter == nil || len(waiter.result) != 0 {
					t.Fatal("session grant settled the excluded prompt")
				}
				f.decide(accepted.RunID, request, "reject")
			}
			for event := range events {
				if !isTerminalEvent(event.Type) {
					continue
				}
				if event.Type != protocol.EventRunCompleted || f.executedCount() != 1 {
					t.Fatalf("ended %s with %d executions", event.Type, f.executedCount())
				}
				return
			}
			t.Fatal("missing terminal event")
		})
	}
}
