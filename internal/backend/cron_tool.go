package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const cronToolSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["create","read","update","delete","list_logs"]},"task_id":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":500},"name":{"type":"string"},"description":{"type":"string"},"cron":{"type":"string","description":"Six fields: second minute hour day month weekday"},"type":{"type":"string","enum":["bash","http","prompt"]},"enabled":{"type":"boolean"},"remaining_executions":{"type":["integer","null"],"minimum":0},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"script":{"type":"string"},"requests":{"type":"array","items":{"type":"object","properties":{"url":{"type":"string"},"method":{"type":"string"},"headers":{"type":"object","additionalProperties":{"type":"string"}},"body":{}},"required":["url"]}},"prompt":{"type":"string"},"workdir":{"type":"string"}},"required":["action"],"additionalProperties":false}`

func (m *cronManager) attachTool(ag *agent.Agent, model protocol.ModelRef) {
	ag.Tools = append(ag.Tools, tools.Tool{
		Def: ai.NewTool("CronTaskManager", "Manage persistent scheduled tasks in Settings -> Cron. Use create, read, update, delete, or list_logs. Bash, HTTP and prompt jobs execute through K-brain. Prompt creation inherits the current model and medium reasoning. Bash/prompt tasks pin the current workspace. Manual runs do not consume remaining executions.", cronToolSchema),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			var args map[string]any
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			action, _ := args["action"].(string)
			id, _ := args["task_id"].(string)
			id = strings.TrimSpace(id)
			encode := func(value any) (string, error) { b, err := json.Marshal(value); return string(b), err }
			if action == "read" {
				snapshot := m.store.snapshot()
				if id == "" {
					return encode(snapshot)
				}
				for _, task := range snapshot.Tasks {
					if task.ID == id {
						return encode(task)
					}
				}
				return "", errors.New("cron task not found")
			}
			if action == "list_logs" {
				limit := 100
				if value, ok := args["limit"].(float64); ok {
					limit = int(value)
				}
				runs, err := m.store.runs(id, limit)
				if err != nil {
					return "", err
				}
				return encode(runs)
			}
			if action != "create" && action != "update" && action != "delete" {
				return "", fmt.Errorf("unsupported cron action %q", action)
			}
			if action != "create" && id == "" {
				return "", errors.New("task_id is required")
			}
			if err := tools.Authorize(ctx, "CronTaskManager", action+" "+id); err != nil {
				return "", err
			}
			fields := map[string]any{}
			for _, key := range []string{"name", "description", "cron", "type", "enabled", "script", "requests", "prompt", "workdir"} {
				if value, ok := args[key]; ok {
					fields[key] = value
				}
			}
			if value, ok := args["remaining_executions"]; ok {
				fields["remainingExecutions"] = value
			}
			if value, ok := args["timeout_seconds"]; ok {
				fields["timeoutSeconds"] = value
			}
			if action == "update" && len(fields) == 0 {
				return "", errors.New("update requires a field to change")
			}
			if value, exists := fields["workdir"]; exists {
				if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
					fields["workdir"] = ag.WorkingDir
				}
			} else if action == "create" {
				fields["workdir"] = ag.WorkingDir
			}
			if action == "create" {
				if _, ok := fields["enabled"]; !ok {
					fields["enabled"] = true
				}
				if fields["type"] == "prompt" {
					fields["selectedModel"] = CronModelRef{CustomProviderID: model.Provider, Model: model.Model}
					fields["reasoning"] = "medium"
				}
			}
			op := CronOperation{Op: action, ID: id, Item: fields, Patch: fields}
			for attempt := 0; attempt < 3; attempt++ {
				response, err := m.store.apply(CronApplyInput{BaseRevision: m.store.snapshot().Revision, Ops: []CronOperation{op}})
				if err != nil {
					return "", err
				}
				if response.Status == "ok" {
					return encode(response.Cron)
				}
			}
			return "", errors.New("cron state changed concurrently; retry")
		},
	})
}
