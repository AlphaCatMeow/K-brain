package agent

import (
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	memoryOffNotice = "\n\n<memory>Memory is turned off. Earlier saved-memory and memory index snapshots no longer apply; do not rely on them.</memory>"
	// memoryOffSource marks, with an empty snapshot, every user message created while
	// memory is off. It is persisted with the message so the extractor never sees those
	// turns, also after re-enabling or reloading the session.
	memoryOffSource = "memory-off"
)

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

// KnownTools are the tools a run may name in its options, including memory tools the
// switch currently hides, so a run's validation does not depend on the switch.
func (a *Agent) KnownTools() []tools.Tool {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	return a.modeTools(append(append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...), a.pluginTools...))
}

func (a *Agent) withoutMemoryTools(all []tools.Tool) []tools.Tool {
	if a.MemoryEnabled() {
		return all
	}
	return slices.DeleteFunc(all, func(tool tools.Tool) bool { return tool.Memory })
}

// withoutMemoryOffTurns drops user messages created while memory was off from the
// history handed to the extractor.
func withoutMemoryOffTurns(history []ai.Message) []ai.Message {
	return slices.DeleteFunc(history, func(m ai.Message) bool {
		return m.Role == "user" && slices.ContainsFunc(m.PromptSnapshots, func(s ai.PromptSnapshot) bool {
			return s.Source == memoryOffSource
		})
	})
}
