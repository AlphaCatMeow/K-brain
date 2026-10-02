package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// memoryStoreTool exposes the bounded Markdown memory manager to an agent.
func memoryStoreTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("memory_manager", "Manage durable LiveAgent-compatible memory for the current project. Use list, read, search, write, update, delete, or accept.", `{"type":"object","properties":{"operation":{"type":"string","enum":["list","read","search","write","update","delete","accept"]},"args":{"type":"object"}},"required":["operation"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				Operation string          `json:"operation"`
				Args      json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", err
			}
			if in.Operation == "" {
				return "", fmt.Errorf("operation is required")
			}
			command := "memory_" + in.Operation
			if command == "memory_manager" || !memoryManagerCommand(command) {
				return "", fmt.Errorf("unsupported memory operation %q", in.Operation)
			}
			var object map[string]any
			if len(in.Args) > 0 && string(in.Args) != "null" {
				if err := json.Unmarshal(in.Args, &object); err != nil {
					return "", err
				}
			} else {
				object = map[string]any{}
			}
			if _, supplied := object["includeAllProjects"]; supplied {
				return "", fmt.Errorf("includeAllProjects is unavailable to the agent memory manager")
			}
			if _, supplied := object["workdirHash"]; supplied {
				return "", fmt.Errorf("workdirHash is unavailable to the agent memory manager")
			}
			if _, supplied := object["workdir"]; supplied {
				return "", fmt.Errorf("workdir is bound to the current agent project")
			}
			object["workdir"] = a.WorkingDir
			encoded, err := json.Marshal(object)
			if err != nil {
				return "", err
			}
			store, err := memory.OpenStore("")
			if err != nil {
				return "", err
			}
			result, err := store.Dispatch(command, encoded)
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(result)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		},
	}
}

func memoryManagerCommand(command string) bool {
	switch command {
	case "memory_list", "memory_read", "memory_search", "memory_write", "memory_update", "memory_delete", "memory_accept":
		return true
	default:
		return false
	}
}
