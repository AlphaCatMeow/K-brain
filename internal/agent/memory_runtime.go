package agent

import (
	"context"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// MemoryTurn is the durable input and history snapshot supplied after a real turn.
type MemoryTurn struct {
	SessionID string
	Workdir   string
	Model     string
	User      ai.Message
	History   []ai.Message
}

// MemoryRuntime owns backend memory injection and hidden extraction. Implementations
// must keep persistence behind the internal/memory Store seam.
type MemoryRuntime interface {
	Inject(context.Context, string) (string, error)
	Completed(context.Context, MemoryTurn)
	Tools(func() string) []tools.Tool
	Close() error
}

func (a *Agent) SetMemoryRuntime(runtime MemoryRuntime) {
	a.memoryRuntime = runtime
	if runtime == nil {
		return
	}
	for _, candidate := range runtime.Tools(func() string { return a.WorkingDir }) {
		duplicate := false
		for _, existing := range a.Tools {
			if existing.Def.Function.Name == candidate.Def.Function.Name {
				duplicate = true
				break
			}
		}
		if !duplicate {
			a.Tools = append(a.Tools, candidate)
		}
	}
}

func (a *Agent) installRuntimeMemory(block string) {
	const marker = "\n\n<kbrain-memory-runtime>\n"
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	if len(a.Messages) == 0 || a.Messages[0].Role != "system" {
		return
	}
	content := a.Messages[0].Content
	if a.runtimeMemoryBlock != "" {
		content = strings.TrimSuffix(content, a.runtimeMemoryBlock)
	}
	runtimeBlock := marker + block + "\n</kbrain-memory-runtime>"
	a.Messages[0].Content = strings.TrimRight(content, "\n") + runtimeBlock
	a.runtimeMemoryBlock = runtimeBlock
}
