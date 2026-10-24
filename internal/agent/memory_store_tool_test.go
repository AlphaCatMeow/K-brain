package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// The model only sees the schema. With an untyped args object a real model
// guessed key/content/scope="global/user" and needed six calls for one write.
func TestMemoryManagerSchemaAdvertisesArgs(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	var schema string
	for _, tool := range ag.Tools {
		if tool.Def.Function.Name == "memory_manager" {
			schema = string(tool.Def.Function.Parameters)
		}
	}
	if schema == "" {
		t.Fatal("memory_manager not registered")
	}
	for _, want := range []string{`"slug"`, `"body"`, `"description"`, `"memoryType"`, `"query"`, `"global"`, `"project"`, `"feedback"`, `"reference"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("memory_manager schema does not advertise %s: %s", want, schema)
		}
	}
}

func TestMemoryManagerWriteWithAdvertisedArgs(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	out := tools.Execute(context.Background(), ag.Tools, "memory_manager", json.RawMessage(`{"operation":"write","args":{"slug":"user-reply-language","scope":"global","memoryType":"user","description":"Reply in Chinese","body":"Answer in Chinese, conclusion first."}}`))
	if !strings.Contains(out, `"created":true`) {
		t.Fatalf("write with advertised args failed: %s", out)
	}
}
