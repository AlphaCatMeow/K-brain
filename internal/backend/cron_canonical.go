package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// executeCronPromptCanonical runs scheduled prompts through the same session and
// executor used by HTTP runs. The task owns one durable conversation so history,
// memory, MCP, hooks, questions, checkpoints, and model switches remain visible.
func (s *Server) executeCronPromptCanonical(ctx context.Context, task *CronTask, cwd string, run *CronRunRecord) (bool, string) {
	if task == nil || task.SelectedModel == nil {
		return false, "Prompt cron task has no backend model."
	}
	selected := protocol.ModelRef{Provider: strings.TrimSpace(task.SelectedModel.CustomProviderID), Model: strings.TrimSpace(task.SelectedModel.Model)}
	if selected.Model == "" || strings.TrimSpace(task.Prompt) == "" {
		return false, "Prompt cron task model and prompt are required."
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = s.defaultCWD
	}
	if strings.TrimSpace(cwd) == "" {
		return false, "Prompt cron task has no working directory."
	}

	var (
		id  string
		err error
	)
	if strings.TrimSpace(task.SessionID) != "" {
		id = strings.TrimSpace(task.SessionID)
	} else {
		id, err = s.store.Create(cwd, selected.Model, selected.Provider)
		if err != nil {
			return false, err.Error()
		}
		if err := s.store.SetTitle(id, task.Name); err != nil {
			_ = s.store.Delete(id)
			return false, err.Error()
		}
		if err := s.cron.store.bindSession(task, id); err != nil {
			_ = s.store.Delete(id)
			return false, err.Error()
		}
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		return false, err.Error()
	}
	in := protocol.PromptRequest{
		ConversationID:  id,
		ClientRequestID: "cron-" + run.ID,
		Prompt:          strings.TrimSpace(task.Prompt),
		Model:           &selected,
		Options: &protocol.RunOptions{
			Mode:           "agent",
			Search:         "disabled",
			ApprovalPolicy: "ask",
			Reasoning:      strings.TrimSpace(task.Reasoning),
		},
		HookPolicy:  "backend",
		HookScopeID: id,
	}
	accepted, status, err := s.startCanonicalRun(withUnattendedRun(ctx), rt, in)
	if err != nil {
		return false, err.Error()
	}
	if status != 202 && status != 200 {
		return false, "scheduled prompt was not accepted"
	}
	run.SessionID, run.RunID = id, accepted.RunID
	if err := s.cron.store.record(*run, false); err != nil {
		rt.mu.Lock()
		if rt.cancel != nil {
			rt.cancel()
		}
		rt.mu.Unlock()
		_ = waitForCanonicalRun(rt, accepted.RunID, ctx)
		return false, err.Error()
	}
	if err := waitForCanonicalRun(rt, accepted.RunID, ctx); err != nil {
		return false, err.Error()
	}
	if ctx.Err() != nil {
		return false, ctx.Err().Error()
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for i := len(rt.events) - 1; i >= 0; i-- {
		event := rt.events[i]
		if event.RunID != accepted.RunID || event.Type != protocol.EventAssistantMessage {
			continue
		}
		var message protocol.Message
		if err := json.Unmarshal(event.Payload, &message); err != nil {
			return false, err.Error()
		}
		if text := messageText(message); text != "" {
			return true, text
		}
		break
	}
	return false, "Auto Prompt request returned an empty conclusion."
}

func waitForCanonicalRun(rt *runtimeSession, runID string, ctx context.Context) error {
	cancelled := ctx.Done()
	for {
		rt.mu.Lock()
		done := rt.runID == runID && rt.runDone
		for _, record := range rt.runs {
			if record.RunID == runID && record.Terminal {
				done = true
				break
			}
		}
		if done && rt.runtimeErr != nil {
			err := rt.runtimeErr
			rt.mu.Unlock()
			return err
		}
		for _, event := range rt.events {
			if event.RunID != runID || !done {
				continue
			}
			switch event.Type {
			case protocol.EventRunCompleted:
				rt.mu.Unlock()
				return nil
			case protocol.EventRunFailed:
				var terminal protocol.RunTerminal
				_ = json.Unmarshal(event.Payload, &terminal)
				rt.mu.Unlock()
				if terminal.Error != "" {
					return errors.New(terminal.Error)
				}
				return errors.New("scheduled prompt failed")
			case protocol.EventRunCancelled:
				rt.mu.Unlock()
				return context.Canceled
			}
		}
		changed := rt.changed
		rt.mu.Unlock()
		select {
		case <-changed:
		case <-cancelled:
			// Keep ownership until executeRun has saved and released the session.
			cancelled = nil
		}
	}
}

func messageText(message protocol.Message) string {
	var parts []string
	for _, block := range message.Content {
		if block.Type == protocol.ContentText && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}
