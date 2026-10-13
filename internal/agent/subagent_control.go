package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const maxSubagentMessageBytes = 64 << 10
const maxPendingSubagentMessages = 32

func validateSubagentMessage(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("subagent message is required")
	}
	if len(text) > maxSubagentMessageBytes {
		return fmt.Errorf("subagent message exceeds %d bytes", maxSubagentMessageBytes)
	}
	return nil
}

// Control tools stay on the parent; IDs are resolved only in its task registry.
func taskControlTools(parent *Agent) []tools.Tool {
	return []tools.Tool{
		{
			Def: ai.NewTool("subagent_status", "Inspect your background subagents. Omit id for a bounded task summary, or provide id for its original task report. Follow-up reports are returned by subagent_followup and retained in the child transcript. Completion is already delivered automatically; do not repeatedly poll this tool.", `{"type":"object","properties":{"id":{"type":"string","description":"Optional owned subagent ID"}}}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				var in struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return "", err
				}
				type summary struct {
					ID          string     `json:"id"`
					Description string     `json:"description"`
					Status      TaskStatus `json:"status"`
					FollowingUp bool       `json:"following_up"`
					CanFollowup bool       `json:"can_followup"`
					Report      string     `json:"report,omitempty"`
				}
				r := parent.Tasks()
				r.mu.Lock()
				defer r.mu.Unlock()
				view := func(t *BackgroundTask, report bool) summary {
					ready := false
					if t.Done != nil {
						select {
						case <-t.Done:
							ready = true
						default:
						}
					}
					s := summary{ID: t.ID, Description: shortTaskDescription(t.Description), Status: t.Status, FollowingUp: t.FollowingUp, CanFollowup: ready && t.sub != nil && t.Status != TaskRunning && !t.FollowingUp}
					if report {
						s.Report = capReport(t.Report)
					}
					return s
				}
				if in.ID != "" {
					t, ok := r.tasks[in.ID]
					if !ok {
						return "", fmt.Errorf("unknown subagent %q", in.ID)
					}
					data, err := json.Marshal(view(t, true))
					return string(data), err
				}
				tasks := make([]summary, 0, min(len(r.tasks), 100))
				// Keep list output bounded even in long-lived parent sessions.
				ids := make([]string, 0, len(r.tasks))
				for id := range r.tasks {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					if len(tasks) == 100 {
						break
					}
					tasks = append(tasks, view(r.tasks[id], false))
				}
				data, err := json.Marshal(struct {
					Tasks     []summary `json:"tasks"`
					Total     int       `json:"total"`
					Truncated bool      `json:"truncated"`
				}{tasks, len(r.tasks), len(tasks) < len(r.tasks)})
				return string(data), err
			},
		},
		{
			Def: ai.NewTool("subagent_cancel", "Request cancellation of an owned background subagent or its active follow-up. Cancellation is asynchronous; this does not claim cleanup has finished.", `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				var in struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return "", err
				}
				if _, ok := parent.Tasks().Get(in.ID); !ok {
					return "", fmt.Errorf("unknown subagent %q", in.ID)
				}
				if !parent.Tasks().Cancel(in.ID) {
					return "Subagent is already inactive; no cancellation requested.", nil
				}
				return "Cancellation requested; completion and cleanup may still be in progress.", nil
			},
		},
		{
			Def: ai.NewTool("subagent_followup", "Continue a completed owned subagent using its retained conversation, model and workspace. Waits for the new report. For a running subagent use subagent_steer instead. Restored summaries without a live context cannot resume.", `{"type":"object","properties":{"id":{"type":"string"},"message":{"type":"string","description":"Follow-up instructions, at most 64 KiB"}},"required":["id","message"]}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct {
					ID      string `json:"id"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return "", err
				}
				out, err := parent.FollowupTask(ctx, in.ID, in.Message, Events{})
				return capReport(out), err
			},
		},
	}
}

func shortTaskDescription(s string) string {
	if len(s) <= 256 {
		return s
	}
	end := 256
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
