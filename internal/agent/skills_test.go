package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestSkillsManagerToolConstructionHasNoSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv(datapath.LiveAgentHomeEnv, home)
	_ = New(ai.New("http://unused", "key"), "model", 100, "system")
	if _, err := os.Stat(filepath.Join(home, "skills")); !os.IsNotExist(err) {
		t.Fatalf("Agent construction created skills directory: err=%v", err)
	}
}

func TestSkillsManagerRegisteredAndExecutesManager(t *testing.T) {
	t.Setenv(datapath.LiveAgentHomeEnv, t.TempDir())
	ag := New(ai.New("http://unused", "key"), "model", 100, "system")

	var registered bool
	for _, tool := range ag.Tools {
		if tool.Def.Function.Name == "SkillsManager" {
			registered = true
			break
		}
	}
	if !registered {
		t.Fatal("SkillsManager is not registered")
	}

	result := tools.Execute(context.Background(), ag.Tools, "SkillsManager", json.RawMessage(`{"action":"list"}`))
	if strings.HasPrefix(result, "Error:") {
		t.Fatalf("SkillsManager list failed: %s", result)
	}
	var definition struct {
		Function struct {
			Parameters struct {
				AdditionalProperties bool `json:"additionalProperties"`
				Properties           map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"parameters"`
		} `json:"function"`
	}
	for _, tool := range ag.Tools {
		if tool.Def.Function.Name == "SkillsManager" {
			if err := json.Unmarshal(tool.Def.Function.Parameters, &definition.Function.Parameters); err != nil {
				t.Fatal(err)
			}
		}
	}
	if definition.Function.Parameters.AdditionalProperties {
		t.Fatal("SkillsManager schema must reject unknown properties")
	}
	for _, action := range []string{"read", "list", "install", "create", "validate", "package", "delete", "clawhub_search", "clawhub_install"} {
		found := false
		for _, candidate := range definition.Function.Parameters.Properties["action"].Enum {
			found = found || candidate == action
		}
		if !found {
			t.Fatalf("SkillsManager schema missing LiveAgent action %q", action)
		}
	}

	var response struct {
		Action string `json:"action"`
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatalf("decode SkillsManager response: %v (%s)", err, result)
	}
	if response.Action != "list" || len(response.Skills) < 2 {
		t.Fatalf("SkillsManager list response = %+v, want built-in skills", response)
	}
}
