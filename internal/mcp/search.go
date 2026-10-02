package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	ToolSearchName           = "ToolSearch"
	toolSearchThresholdBytes = 48_000
	toolSearchMaxResults     = 10
)

type ToolActivation struct {
	mu    sync.RWMutex
	names map[string]bool
}

func NewToolActivation() *ToolActivation { return &ToolActivation{names: map[string]bool{}} }

func (a *ToolActivation) Add(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.names[name] = true
	a.mu.Unlock()
}

func (a *ToolActivation) Has(name string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.names[name]
}

func (m *LiveManager) ToolsForTurn(ctx context.Context, cwd string, selected []string, activation *ToolActivation) ([]tools.Tool, func([]tools.Tool) []tools.Tool) {
	all := m.ToolsFor(ctx, cwd, selected)
	if !shouldSearch(all) {
		return all, func(in []tools.Tool) []tools.Tool { return in }
	}
	for i := range all {
		name := all[i].Def.Function.Name
		run := all[i].Run
		all[i].Run = func(toolName string, toolRun func(context.Context, json.RawMessage) (string, error)) func(context.Context, json.RawMessage) (string, error) {
			return func(ctx context.Context, args json.RawMessage) (string, error) {
				activation.Add(toolName)
				return toolRun(ctx, args)
			}
		}(name, run)
	}
	all = append(all, toolSearch(all, activation))
	return all, FilterRequestTools(activation)
}

func FilterRequestTools(activation *ToolActivation) func([]tools.Tool) []tools.Tool {
	return func(in []tools.Tool) []tools.Tool {
		if activation == nil {
			return in
		}
		out := make([]tools.Tool, 0, len(in))
		for _, tool := range in {
			if !strings.HasPrefix(tool.Def.Function.Name, "mcp__") || activation.Has(tool.Def.Function.Name) || tool.Def.Function.Name == ToolSearchName {
				out = append(out, tool)
			}
		}
		return out
	}
}

func shouldSearch(list []tools.Tool) bool {
	size := 0
	for _, tool := range list {
		if strings.HasPrefix(tool.Def.Function.Name, "mcp__") {
			size += len(tool.Def.Function.Description) + len(tool.Def.Function.Parameters)
		}
	}
	return size > toolSearchThresholdBytes
}

func toolSearch(candidates []tools.Tool, activation *ToolActivation) tools.Tool {
	return tools.Tool{Def: ai.NewTool(ToolSearchName,
		"Search the deferred MCP tool catalog and activate matching tools. Call this before assuming an MCP capability is missing.",
		`{"type":"object","properties":{"query":{"type":"string"},"max_results":{"type":"number"}},"required":["query"]}`),
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var input struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			query := strings.TrimSpace(strings.ToLower(input.Query))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			limit := input.MaxResults
			if limit <= 0 || limit > toolSearchMaxResults {
				limit = 5
			}
			terms := strings.Fields(query)
			type result struct {
				tool  tools.Tool
				score int
			}
			results := make([]result, 0, len(candidates))
			for _, candidate := range candidates {
				text := strings.ToLower(candidate.Def.Function.Name + " " + candidate.Def.Function.Description)
				score := 0
				for _, term := range terms {
					if strings.Contains(candidate.Def.Function.Name, term) {
						score += 3
					}
					if strings.Contains(text, term) {
						score++
					}
				}
				if score > 0 {
					results = append(results, result{candidate, score})
				}
			}
			sort.SliceStable(results, func(i, j int) bool {
				if results[i].score != results[j].score {
					return results[i].score > results[j].score
				}
				return results[i].tool.Def.Function.Name < results[j].tool.Def.Function.Name
			})
			if len(results) > limit {
				results = results[:limit]
			}
			var b strings.Builder
			if len(results) == 0 {
				return fmt.Sprintf("No deferred MCP tools matched %q", input.Query), nil
			}
			fmt.Fprintf(&b, "Activated %d MCP tool(s):\n", len(results))
			for _, item := range results {
				activation.Add(item.tool.Def.Function.Name)
				fmt.Fprintf(&b, "\n## %s\n%s\n```json\n%s\n```\n", item.tool.Def.Function.Name, item.tool.Def.Function.Description, item.tool.Def.Function.Parameters)
			}
			return b.String(), nil
		},
	}
}
