package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func TestContextSnapshotsTrackSourcesAndRetainedHistory(t *testing.T) {
	a := New(nil, "fixture", 100, "stable")
	if got := a.contextSnapshots(); len(got) != 0 {
		t.Fatalf("initial empty context emitted a snapshot: %+v", got)
	}
	a.memoryBlock = "saved one"
	a.installRuntimeMemory("runtime one")
	first := a.contextSnapshots()
	if len(first) != 2 || first[0].Source != savedMemorySource || first[1].Source != runtimeMemorySource {
		t.Fatalf("snapshots = %+v", first)
	}
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "first", PromptSnapshots: first})
	if len(a.contextSnapshots()) != 0 {
		t.Fatal("unchanged retained context duplicated")
	}
	a.memoryBlock = ""
	clear := a.contextSnapshots()
	if len(clear) != 1 || clear[0].Text != clearedSavedMemory {
		t.Fatalf("clear must only replace the changed source: %+v", clear)
	}
	a.Messages = append(a.Messages, ai.Message{Role: "user", PromptSnapshots: clear})
	stored, err := json.Marshal(a.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var restored []ai.Message
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	a.RestoreMessages(restored)
	if len(a.contextSnapshots()) != 0 {
		t.Fatal("restored clear marker duplicated")
	}
	a.RestoreMessages([]ai.Message{{Role: "system", Content: "stable"}})
	if got := a.contextSnapshots(); len(got) != 1 || got[0].Source != runtimeMemorySource {
		t.Fatalf("removed snapshots must be reintroduced from current context: %+v", got)
	}
}

func TestContextSnapshotOwnershipAndLegacyMigration(t *testing.T) {
	a := New(nil, "fixture", 100, "stable")
	a.installRuntimeMemory("one")
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: a.runtimeMemoryBlock})
	if len(a.contextSnapshots()) != 1 {
		t.Fatal("user-authored text was mistaken for owned context")
	}
	a.Messages = append(a.Messages, ai.Message{Role: "user", PromptContext: "plan" + a.runtimeMemoryBlock})
	if len(a.contextSnapshots()) != 0 {
		t.Fatal("legacy request-only snapshot was unnecessarily reinserted")
	}
	a.installRuntimeMemory("two")
	if got := a.contextSnapshots(); len(got) != 1 || !strings.Contains(got[0].Text, "two") {
		t.Fatalf("migration suppressed a real change: %+v", got)
	}
}

func TestSavedMemoryUpdatesPreserveWirePrefix(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Setenv("LIVEAGENT_HOME", "")
	var requests []ai.Request
	server := textServer(t, func(_ int, request ai.Request) string {
		requests = append(requests, request)
		return "done"
	})
	defer server.Close()
	newAgent := func() *Agent { return New(ai.New(server.URL, "fixture"), "fixture", 100, "stable") }
	a := newAgent()
	for i := range 4 {
		if i == 0 {
			if err := memory.Installation().Remember("saved fact"); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if err := memory.Installation().Forget(1); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.TurnAuthored(t.Context(), "continue", Events{}); err != nil {
			t.Fatal(err)
		}
		persisted, _ := json.Marshal(a.MessagesSnapshot())
		var restored []ai.Message
		if err := json.Unmarshal(persisted, &restored); err != nil {
			t.Fatal(err)
		}
		a = newAgent()
		a.RestoreMessages(restored)
	}
	for i, request := range requests {
		if request.Messages[0].Content != "stable" {
			t.Fatal("saved memory rewrote the leading system prompt")
		}
		if i > 0 && !reflect.DeepEqual(requests[i-1].Messages, request.Messages[:len(requests[i-1].Messages)]) {
			t.Fatal("saved memory or reload changed previous request prefix")
		}
		last := request.Messages[len(request.Messages)-1].Content
		if i == 0 && !strings.Contains(last, "saved fact") || i == 2 && !strings.Contains(last, "Earlier saved-memory snapshots no longer apply") {
			t.Fatal("saved memory update or clear was not delivered")
		}
		if (i == 1 || i == 3) && last != "continue" {
			t.Fatal("unchanged snapshot duplicated after reload")
		}
	}
}

func TestNewContextNoticePersistsAtItsCausalPosition(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var requests []ai.Request
	server := textServer(t, func(_ int, request ai.Request) string {
		requests = append(requests, request)
		return "done"
	})
	defer server.Close()
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "original task"})
	a.resetContextMessages(true)
	for range 2 {
		if _, err := a.TurnAuthored(t.Context(), "continue", Events{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(requests[0].Messages, requests[1].Messages[:len(requests[0].Messages)]) {
		t.Fatal("new-context notice disappeared on the next request")
	}
	count := 0
	for _, m := range requests[1].Messages {
		if strings.Contains(m.Content, "A new context window is active") {
			count++
			if m.Role != "user" || !strings.Contains(m.Content, "original task") {
				t.Fatal("notice lost task or would be hoisted into the system prefix")
			}
		}
	}
	if count != 1 {
		t.Fatalf("context notices = %d", count)
	}
}
