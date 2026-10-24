package agent

import "github.com/Stack-Cairn/K-brain/internal/tools"

const memoryOffNotice = "\n\n<memory>Memory is turned off. Earlier saved-memory and memory index snapshots no longer apply; do not rely on them.</memory>"

// memoryToolNames are the agent-facing memory tools: remember/forget (memory.md) and
// memory_manager (registered by the agent and, with the same name, by MemoryRuntime).
var memoryToolNames = map[string]bool{"remember": true, "forget": true, "memory_manager": true}

// IsMemoryTool reports whether name is a memory tool the memory switch hides.
func IsMemoryTool(name string) bool { return memoryToolNames[name] }

// WithMemoryEnabled sets the memory master switch at construction.
func WithMemoryEnabled(enabled bool) Option {
	return func(a *Agent) { a.SetMemoryEnabled(enabled) }
}

// SetMemoryEnabled toggles memory injection, hidden extraction and memory tools from the
// next turn on. Stored memories are untouched.
func (a *Agent) SetMemoryEnabled(enabled bool) {
	a.memoryOff.Store(!enabled)
	if !enabled {
		a.msgsMu.Lock()
		a.memoryBlock, a.runtimeMemoryBlock = "", ""
		a.msgsMu.Unlock()
	}
}

func (a *Agent) MemoryEnabled() bool { return !a.memoryOff.Load() }

func (a *Agent) withoutMemoryTools(all []tools.Tool) []tools.Tool {
	if a.MemoryEnabled() {
		return all
	}
	out := all[:0:0]
	for _, tool := range all {
		if !IsMemoryTool(tool.Def.Function.Name) {
			out = append(out, tool)
		}
	}
	return out
}
