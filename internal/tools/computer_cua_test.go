package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestComputerResultImagesAndErrors(t *testing.T) {
	for _, vision := range []bool{false, true} {
		tool := Tool{Def: ComputerExec().Def, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			return computerResult(ctx, &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"detail": "original"}, Content: []mcp.Content{
				&mcp.TextContent{Text: "original failure"}, &mcp.ImageContent{MIMEType: "image/png", Data: []byte("fixture")},
			}})
		}}
		result := ExecuteResult(t.Context(), []Tool{tool}, "computer_exec", json.RawMessage(`{}`), vision)
		if !result.Failed || !strings.Contains(result.Text, "original failure") || !strings.Contains(result.Text, `"detail":"original"`) {
			t.Fatalf("lost result: %+v", result)
		}
		if (len(result.Parts) == 1) != vision {
			t.Fatalf("image parts: %d", len(result.Parts))
		}
		if !vision && !strings.Contains(result.Text, "vision-capable") {
			t.Fatal("silently discarded screenshot")
		}
	}
}

func TestComputerConfigAndPermissions(t *testing.T) {
	for _, cfg := range []config.ComputerConfig{{Backend: "typo"}, {Deny: []string{"blocked-app"}}} {
		_, err := ComputerExec().Run(WithComputerConfig(t.Context(), cfg), json.RawMessage(`{"action":"discover"}`))
		if err == nil {
			t.Fatalf("ignored config: %+v", cfg)
		}
	}
	ctx := WithGate(t.Context(), func(GateRequest) (GateDecision, string) { return GateReject, "fixture denied" })
	_, err := ComputerExec().Run(ctx, json.RawMessage(`{"action":"discover"}`))
	if err == nil || !strings.Contains(err.Error(), "fixture denied") {
		t.Fatalf("gate bypassed: %v", err)
	}
	if _, err := ComputerExec().Run(t.Context(), json.RawMessage(`{"code":"click(1,2)"}`)); err == nil {
		t.Fatal("legacy pseudocode silently accepted")
	}
}
