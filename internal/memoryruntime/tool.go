package memoryruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func memoryManagerTool(store *memory.Store, workdir func() string) tools.Tool {
	return tools.Tool{Def: ai.NewTool("memory_manager", "List, read, search, or propose a memory mutation. Mutations are applied through the canonical memory store.", `{"type":"object","properties":{"action":{"type":"string","enum":["list","read","search","write","update","delete","accept"]},"args":{"type":"object"}},"required":["action"]}`), Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", err
		}
		if len(in.Args) == 0 {
			in.Args = []byte(`{}`)
		}
		dir := workdir()
		var result any
		var err error
		switch in.Action {
		case "list":
			var a memory.ListArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.List(a)
			}
		case "read":
			var a memory.ReadArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.Read(a)
			}
		case "search":
			var a memory.SearchArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.Search(a)
			}
		case "write":
			var a memory.WriteArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.ApplyBatch(memory.BatchArgs{Workdir: dir, Decisions: []memory.Decision{{Op: "upsert", Slug: a.Slug, Scope: a.Scope, MemoryType: a.MemoryType, Description: &a.Description, Body: &a.Body, Evidence: a.Evidence}}})
			}
		case "update":
			var a memory.UpdateArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.ApplyBatch(memory.BatchArgs{Workdir: dir, Decisions: []memory.Decision{{Op: "update", Slug: a.Slug, Scope: a.Scope, WorkdirHash: a.WorkdirHash, MemoryType: value(a.MemoryType), Description: a.Description, Body: a.Body, Mode: a.Mode, Evidence: a.Evidence}}})
			}
		case "delete":
			var a memory.DeleteArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.ApplyBatch(memory.BatchArgs{Workdir: dir, Decisions: []memory.Decision{{Op: "delete", Slug: a.Slug, Scope: a.Scope, WorkdirHash: a.WorkdirHash, Reason: a.Reason}}})
			}
		case "accept":
			var a memory.ReadArgs
			err = json.Unmarshal(in.Args, &a)
			a.Workdir = dir
			if err == nil {
				result, err = store.ApplyBatch(memory.BatchArgs{Workdir: dir, Decisions: []memory.Decision{{Op: "accept", Slug: a.Slug, Scope: a.Scope, WorkdirHash: a.WorkdirHash}}})
			}
		default:
			return "", fmt.Errorf("unsupported memory action %q", in.Action)
		}
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(result)
		return string(out), err
	}}
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
