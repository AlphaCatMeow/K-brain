package agent

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const (
	savedMemorySource   = "saved-memory"
	runtimeMemorySource = "runtime-memory"
	clearedSavedMemory  = "\n\n<memory>Current saved memory: none. Earlier saved-memory snapshots no longer apply.</memory>"
)

func promptContextText(message ai.Message) string {
	var text strings.Builder
	text.WriteString(message.PromptContext)
	for _, snapshot := range message.PromptSnapshots {
		text.WriteString(snapshot.Text)
	}
	return text.String()
}

func (a *Agent) contextSnapshots() []ai.PromptSnapshot {
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	var snapshots []ai.PromptSnapshot
	if !a.MemoryEnabled() {
		// Retract memory that earlier turns carried; history keeps its snapshots for cache
		// stability, so a newer snapshot must say they no longer apply. One notice is enough.
		notice := memoryOffNotice
		for _, source := range []string{savedMemorySource, runtimeMemorySource} {
			if previous, exists := retainedSnapshot(a.Messages, source); exists && previous != memoryOffNotice && previous != "" {
				snapshots = append(snapshots, ai.PromptSnapshot{Source: source, Text: notice})
				notice = ""
			}
		}
		// A session that never carried memory, or whose last instruction was "on again",
		// still needs the off instruction once.
		if notice != "" && lastMemorySwitchInstruction(a.Messages) != memoryOffNotice {
			snapshots = append(snapshots, ai.PromptSnapshot{Source: runtimeMemorySource, Text: notice})
		}
		return append(snapshots, MemoryOffSnapshot())
	}
	for _, current := range []ai.PromptSnapshot{
		{Source: savedMemorySource, Text: a.memoryBlock},
		{Source: runtimeMemorySource, Text: a.runtimeMemoryBlock},
	} {
		if current.Source == savedMemorySource && a.memoryDisabled {
			continue
		}
		// Empty runtime state means no successful refresh, not an empty index.
		if current.Source == runtimeMemorySource && current.Text == "" {
			continue
		}
		previous, exists := retainedSnapshot(a.Messages, current.Source)
		if current.Text == "" {
			if !exists {
				continue
			}
			current.Text = clearedSavedMemory
		}
		if exists && previous == current.Text {
			continue
		}
		snapshots = append(snapshots, current)
	}
	// Re-injected memory does not lift the off instruction, so say it once on a source of
	// its own; the memory sources keep deduplicating against their own blocks.
	if lastMemorySwitchInstruction(a.Messages) == memoryOffNotice {
		snapshots = append(snapshots, ai.PromptSnapshot{Source: memorySwitchSource, Text: memoryOnNotice})
	}
	return snapshots
}

// lastMemorySwitchInstruction is the newest off or on-again instruction in History.
func lastMemorySwitchInstruction(messages []ai.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		snapshots := messages[i].PromptSnapshots
		for j := len(snapshots) - 1; j >= 0; j-- {
			if text := snapshots[j].Text; text == memoryOffNotice || text == memoryOnNotice {
				return text
			}
		}
	}
	return ""
}

func retainedSnapshot(messages []ai.Message, source string) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		for j := len(message.PromptSnapshots) - 1; j >= 0; j-- {
			if snapshot := message.PromptSnapshots[j]; snapshot.Source == source {
				return snapshot.Text, true
			}
		}
		// Older backends persisted the runtime snapshot in request-only text.
		if source == runtimeMemorySource {
			const start = "\n\n<kbrain-memory-runtime>\n"
			if at := strings.LastIndex(message.PromptContext, start); at >= 0 && strings.HasSuffix(message.PromptContext, "\n</kbrain-memory-runtime>") {
				return message.PromptContext[at:], true
			}
		}
	}
	return "", false
}
