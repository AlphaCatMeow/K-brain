package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func (s *Server) trajectoryDetails(w http.ResponseWriter, r *http.Request, id, kind string) {
	w.Header().Set("Cache-Control", "no-store")
	events, err := s.trajectorySnapshot(id)
	if err != nil {
		status := 500
		if errors.Is(err, session.ErrNotFound) {
			status = 404
		}
		writeJSONError(w, status, err.Error())
		return
	}
	if kind == "sections" {
		ids := r.URL.Query()["section_id"]
		if len(ids) > 64 {
			writeJSONError(w, 400, "at most 64 section ids")
			return
		}
		referenced := map[string]bool{}
		for _, e := range events {
			if e.Type == "trajectory.request.started" {
				p := trajectoryRequestPayload(e)
				if refs, ok := p["sections"].([]any); ok {
					for _, ref := range refs {
						if id, ok := ref.(string); ok {
							referenced[id] = true
						}
					}
				}
			}
		}
		out := []trajectorySection{}
		seen := map[string]bool{}
		for _, sectionID := range ids {
			if !validSectionID(sectionID) {
				writeJSONError(w, 400, "invalid section id")
				return
			}
			if !referenced[sectionID] || seen[sectionID] {
				continue
			}
			seen[sectionID] = true
			section, err := s.readTrajectorySection(id, sectionID)
			if err != nil {
				status := 500
				if errors.Is(err, os.ErrNotExist) {
					status = 404
				}
				writeJSONError(w, status, err.Error())
				return
			}
			out = append(out, section)
		}
		writeJSON(w, 200, out)
		return
	}
	ids := r.URL.Query()["run_id"]
	if len(ids) > 128 {
		writeJSONError(w, 400, "at most 128 run ids")
		return
	}
	runs := buildTrajectorySubagents(events)
	out := []*trajectoryChild{}
	for _, childID := range ids {
		if child := runs[childID]; child != nil {
			if len(child.Steps) == 0 {
				messages, loadErr := s.store.SubagentTranscript(id, childID)
				if loadErr != nil {
					writeJSONError(w, 500, loadErr.Error())
					return
				}
				appendLegacyChildSteps(child, messages)
			}
			out = append(out, child)
		}
	}
	writeJSON(w, 200, out)
}

type trajectoryChildTool struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	IsError   bool   `json:"isError"`
	StartedAt *int64 `json:"startedAt"`
	EndedAt   *int64 `json:"endedAt"`
}
type trajectoryChildStep struct {
	Step      int                    `json:"step"`
	StartedAt *int64                 `json:"startedAt"`
	EndedAt   *int64                 `json:"endedAt"`
	Tools     []*trajectoryChildTool `json:"tools"`
}
type trajectoryChild struct {
	RunID     string                 `json:"runId"`
	AgentID   string                 `json:"agentId"`
	Name      string                 `json:"name,omitempty"`
	Status    string                 `json:"status"`
	StartedAt *int64                 `json:"startedAt"`
	EndedAt   *int64                 `json:"endedAt"`
	Steps     []*trajectoryChildStep `json:"steps"`
}

func buildTrajectorySubagents(events []protocol.Event) map[string]*trajectoryChild {
	out := map[string]*trajectoryChild{}
	requests := map[string]*trajectoryChildStep{}
	tools := map[string]*trajectoryChildTool{}
	ensure := func(id string) *trajectoryChild {
		if out[id] == nil {
			out[id] = &trajectoryChild{RunID: id, AgentID: id, Status: "running", Steps: []*trajectoryChildStep{}}
		}
		return out[id]
	}
	for _, e := range events {
		at := e.CreatedAt.UnixMilli()
		p := trajectoryRequestPayload(e)
		if strings.HasPrefix(e.Type, "subagent.") {
			var event protocol.SubagentEvent
			if json.Unmarshal(e.Payload, &event) != nil || event.Subagent.ID == "" {
				continue
			}
			sub := event.Subagent
			child := ensure(sub.ID)
			if sub.Description != "" {
				child.Name = sub.Description
			}
			if sub.StartedAt != nil {
				v := sub.StartedAt.UnixMilli()
				child.StartedAt = &v
			}
			if sub.EndedAt != nil {
				v := sub.EndedAt.UnixMilli()
				child.EndedAt = &v
			}
			switch sub.Status {
			case "done", "completed":
				child.Status = "complete"
			case "error", "failed":
				child.Status = "error"
			case "cancelled":
				child.Status = "aborted"
			}
			continue
		}
		id := trajectoryString(p, "task_id")
		if id == "" {
			continue
		}
		child := ensure(id)
		switch e.Type {
		case "trajectory.request.started":
			step := &trajectoryChildStep{Step: len(child.Steps) + 1, StartedAt: &at, Tools: []*trajectoryChildTool{}}
			child.Steps = append(child.Steps, step)
			requests[trajectoryString(p, "request_id")] = step
		case "trajectory.request.completed":
			if step := requests[trajectoryString(p, "request_id")]; step != nil {
				step.EndedAt = &at
			}
		case "trajectory.child.tool_start":
			if len(child.Steps) == 0 {
				child.Steps = append(child.Steps, &trajectoryChildStep{Step: 1, Tools: []*trajectoryChildTool{}})
			}
			tool := &trajectoryChildTool{CallID: trajectoryString(p, "id"), Name: trajectoryString(p, "name"), StartedAt: &at}
			child.Steps[len(child.Steps)-1].Tools = append(child.Steps[len(child.Steps)-1].Tools, tool)
			tools[id+"\x00"+tool.CallID] = tool
		case "trajectory.child.tool_end":
			if tool := tools[id+"\x00"+trajectoryString(p, "id")]; tool != nil {
				tool.EndedAt = &at
				tool.IsError = p["failed"] == true || p["cancelled"] == true
			}
		}
	}
	return out
}

func appendLegacyChildSteps(child *trajectoryChild, messages []ai.Message) {
	tools := map[string]*trajectoryChildTool{}
	for _, message := range messages {
		var at *int64
		if message.SentAt != nil {
			v := message.SentAt.UnixMilli()
			at = &v
		}
		if message.Role == "assistant" {
			step := &trajectoryChildStep{Step: len(child.Steps) + 1, EndedAt: at, Tools: []*trajectoryChildTool{}}
			for _, call := range message.ToolCalls {
				tool := &trajectoryChildTool{CallID: call.ID, Name: call.Function.Name}
				step.Tools = append(step.Tools, tool)
				tools[call.ID] = tool
			}
			child.Steps = append(child.Steps, step)
		}
		if message.Role == "tool" {
			if tool := tools[message.ToolCallID]; tool != nil {
				tool.EndedAt = at
				tool.IsError = message.StopReason == ai.StopReasonError
			}
		}
	}
}
