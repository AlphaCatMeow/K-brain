package tools

import (
	"encoding/json"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestDefsStableAcrossDiscoveryOrder(t *testing.T) {
	a := Tool{Def: ai.NewTool("alpha", "a", `{"type":"object"}`)}
	z := Tool{Def: ai.NewTool("zeta", "z", `{"type":"object"}`)}
	input := []Tool{z, a}
	first, _ := json.Marshal(Defs(input))
	second, _ := json.Marshal(Defs([]Tool{a, z}))
	if string(first) != string(second) {
		t.Fatal("discovery order changed serialized definitions")
	}
	if input[0].Def.Function.Name != "zeta" {
		t.Fatal("sorting mutated the execution tool list")
	}
}
