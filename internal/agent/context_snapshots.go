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
	return snapshots
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
