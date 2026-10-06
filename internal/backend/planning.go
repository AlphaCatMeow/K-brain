package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/planning"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func (s *Server) handlePlanning(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var input struct {
		Action string          `json:"action"`
		Input  json.RawMessage `json:"input"`
	}
	if decodeCronJSON(w, r, &input) != nil {
		return
	}
	result, err := s.planningAction(input.Action, input.Input)
	if err != nil {
		writeJSONError(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) planningAction(action string, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	switch action {
	case "cron.occurrences":
		return s.planningCron(raw)
	case "reminders.claim":
		return s.planning.Claim()
	case "reminders.finish":
		var in planning.Item
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.planning.Finish(in)
	case "timezone.get":
		return s.planning.TimeZoneSettings(), nil
	case "timezone":
		var in struct {
			TimeZone         string  `json:"timeZone"`
			Preference       *string `json:"preference"`
			ExpectedRevision *uint64 `json:"expectedRevision"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Preference != nil {
			in.TimeZone = *in.Preference
		}
		return s.planning.UpdateTimeZone(in.TimeZone, in.ExpectedRevision)
	case "query", "export":
		var q struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		}
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		if action == "export" {
			q.From = 0
			q.To = 0
		}
		return s.planning.Query(q.From, q.To)
	case "mutate":
		var m planning.Mutation
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return s.planning.Mutate(m)
	case "import":
		var snap planning.Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, err
		}
		return s.planning.Import(snap, "", nil)
	case "migrate":
		var in struct {
			Source        string            `json:"source"`
			Snapshot      planning.Snapshot `json:"snapshot"`
			Subscriptions []planning.Item   `json:"subscriptions"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Source == "" {
			return nil, errors.New("E:migration_source_required")
		}
		return s.planning.Import(in.Snapshot, in.Source, in.Subscriptions)
	case "subscription.create", "subscription.update", "subscription.refresh", "subscription.delete":
		var in planning.Item
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.planning.Subscription(action, in)
	default:
		return nil, errors.New("E:unknown_request")
	}
}
func (s *Server) attachPlanningTools(ag *agent.Agent) {
	for _, name := range []string{"PlanningQuery", "PlanningMutate"} {
		name := name
		schema := `{"type":"object","properties":{"from":{"type":"integer"},"to":{"type":"integer"}},"additionalProperties":false}`
		description := "Read persistent calendars, tasks, events and reminders shared with LiveAgent. Optional from/to are epoch milliseconds, maximum 366 days. Read current revisions before writing."
		if name == "PlanningMutate" {
			schema = `{"type":"object","properties":{"requestId":{"type":"string"},"action":{"type":"string"},"id":{"type":"string"},"expectedRevision":{"type":"integer"},"data":{"type":"object"}},"required":["requestId","action","data"],"additionalProperties":false}`
			description = "Modify shared Planning. Actions: calendar/group/todo/event create, update, delete; todo/event restore,purge; todo.move,todo.schedule; event.exception,event.split,event.restoreException; reminder.create,snooze,acknowledge. Use title for tasks/events, name/color for calendars/lists. event.create needs calendarId and time {kind:timed,startAt,endAt,timeZone} or {kind:allDay,startDate,endDateExclusive,timeZone}. Updates require id and exact expectedRevision. Stable requestId makes retries idempotent. On conflict reread; never overwrite blindly. Permanent purge requires explicit user intent."
		}
		ag.Tools = append(ag.Tools, tools.Tool{Def: ai.NewTool(name, description, schema), Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			action := "query"
			if name == "PlanningMutate" {
				if err := tools.Authorize(ctx, name, "modify persistent calendar and tasks"); err != nil {
					return "", err
				}
				action = "mutate"
			}
			value, err := s.planningAction(action, raw)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(value)
			return string(b), err
		}})
	}
}
