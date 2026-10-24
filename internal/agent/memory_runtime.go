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
	// Abandoned is called for a turn that ended without completing (stopped,
	// failed, plan-mode exit) after its user message entered History. It must
	// not take the agent's locks.
	Abandoned(sessionID string, user ai.Message)
	Tools(func() string) []tools.Tool
	Close() error
}

func (a *Agent) SetMemoryRuntime(runtime MemoryRuntime) {
	a.memoryRuntime = runtime
	if runtime == nil {
		return
	}
	// The runtime's store is the only memory the host shows; markdown
	// remember/forget files would be a second, invisible one.
	a.markdownMemoryReadOnly = true
	kept := a.Tools[:0]
	for _, tool := range a.Tools {
		if name := tool.Def.Function.Name; name != "remember" && name != "forget" {
			kept = append(kept, tool)
		}
	}
	a.Tools = kept
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
	if strings.TrimSpace(block) == "" {
		block = "No current memory entries."
	}
	a.runtimeMemoryBlock = marker + "Current memory index; replaces earlier memory index snapshots.\n" + block + "\n</kbrain-memory-runtime>"
}
