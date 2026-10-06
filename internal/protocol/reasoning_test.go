package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestDisplayReasoningPersistsWithoutExposingProviderReplay(t *testing.T) {
	input := Message{Role: RoleAssistant, Content: []ContentBlock{{Type: ContentThinking, Text: "display text"}, {Type: ContentText, Text: "answer"}}}
	message, err := input.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	message.Replay = &ai.ProviderReplay{API: ai.APIMessages, Endpoint: "private-endpoint", Model: "m", Blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","signature":"private-signature","thinking":"private-reasoning"}`)}}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var restored ai.Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Reasoning != "display text" || restored.Replay == nil {
		t.Fatalf("lost persisted state: %+v", restored)
	}
	canonical, err := FromAIMessageValidated(restored)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || !strings.Contains(string(encoded), "display text") {
		t.Fatalf("canonical = %s", encoded)
	}
}
