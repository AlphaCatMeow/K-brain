package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageConfigSnapshotPersistence(t *testing.T) {
	cfg := Default()
	cfg.Image = &imaging.ImageSettings{Revision: 9, Connections: []imaging.ImageConnection{{ID: "ic_test", MaxConcurrent: 2, Binding: imaging.ConnectionBinding{Kind: "independent", BaseURL: "https://example.invalid", AuthKind: "bearer", CustomHeaders: map[string]string{"X-Test": "private-sentinel"}}}}, Targets: []imaging.ImageTarget{{ID: "it_test", ConnectionID: "ic_test", Adapter: "openai-images", AdapterVersion: "1", Kind: "model", RemoteID: "demo", Revision: 3, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, MaxCount: 1, OutputFormats: []string{"png"}}, Parameters: []imaging.ParameterSpec{{Key: "quality", Type: "string", Enum: []any{"high"}, Default: "high"}}, Verification: imaging.TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Unix(123, 0).UTC(), ConnectionID: "ic_test", AdapterVersion: "1", TargetRevision: 3}}}}
	snap := cfg.Snapshot()
	snap.Image.Connections[0].Binding.CustomHeaders["X-Test"] = "changed"
	snap.Image.Targets[0].Parameters[0].Enum[0] = "changed"
	if cfg.Image.Connections[0].Binding.CustomHeaders["X-Test"] != "private-sentinel" || cfg.Image.Targets[0].Parameters[0].Enum[0] != "high" {
		t.Fatal("snapshot aliases image config")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Image.Revision != 9 || loaded.Image.Connections[0].Binding.CustomHeaders["X-Test"] != "private-sentinel" || !loaded.Image.Targets[0].Verification.ContractComplete || !loaded.Image.Targets[0].Verification.LiveVerifiedAt.Equal(time.Unix(123, 0)) {
		t.Fatal("private configuration lost on reload")
	}
	raw, err := json.Marshal(loaded.Image)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private-sentinel")) || bytes.Contains(raw, []byte("contractComplete")) {
		t.Fatal("private configuration leaked to public JSON")
	}
}

func TestImageConfigOldEmptyAndUnknownFields(t *testing.T) {
	for _, raw := range []string{`{"providers":{}}`, `{"providers":{},"image":null}`, `{"providers":{},"image":{}}`} {
		var cfg Config
		if err := parseConfigJSONC([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := marshalConfig(cfg.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"providers":{},"surprise":true}`, `{"providers":{},"image":{"surprise":true}}`, `{"providers":{},"image":{"connections":[{"binding":{"secretLiteral":"fake-sentinel"}}]}}`, `{"providers":{},"image":{"targets":[{"verification":{"unexpected":true}}]}}`} {
		var cfg Config
		if err := parseConfigJSONC([]byte(raw), &cfg); err == nil {
			t.Fatalf("accepted unknown field: %s", raw)
		}
	}
	cfg := Default()
	raw, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"image"`)) {
		t.Fatal("absent image must stay omitted")
	}
}

func TestImageConfigSnapshotNestedIsolation(t *testing.T) {
	minimum, maximum := 1.0, 5.0
	cfg := Default()
	cfg.Image = &imaging.ImageSettings{Connections: []imaging.ImageConnection{{Binding: imaging.ConnectionBinding{SecretRefs: map[string]string{"apiKey": "is_fake"}}}}, Targets: []imaging.ImageTarget{{Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, OutputFormats: []string{"png"}}, Parameters: []imaging.ParameterSpec{{Minimum: &minimum, Maximum: &maximum, Default: map[string]any{"nested": []any{"original"}}, Enum: []any{map[string]any{"nested": []any{"original"}}}}}, WorkflowMapping: &imaging.WorkflowMapping{Inputs: []imaging.WorkflowInput{{NodeID: "original"}}, Outputs: []imaging.WorkflowOutput{{NodeID: "original"}}}}}}
	snapshot := cfg.Snapshot()
	snapshot.Image.Connections[0].Binding.SecretRefs["apiKey"] = "changed"
	target := &snapshot.Image.Targets[0]
	target.Capabilities.Operations[0] = imaging.OperationEdit
	target.Capabilities.OutputFormats[0] = "jpeg"
	*target.Parameters[0].Minimum = 2
	*target.Parameters[0].Maximum = 4
	target.Parameters[0].Default.(map[string]any)["nested"].([]any)[0] = "changed"
	target.Parameters[0].Enum[0].(map[string]any)["nested"].([]any)[0] = "changed"
	target.WorkflowMapping.Inputs[0].NodeID = "changed"
	target.WorkflowMapping.Outputs[0].NodeID = "changed"
	original := cfg.Image.Targets[0]
	if cfg.Image.Connections[0].Binding.SecretRefs["apiKey"] != "is_fake" || original.Capabilities.Operations[0] != imaging.OperationGenerate || original.Capabilities.OutputFormats[0] != "png" || minimum != 1 || maximum != 5 || original.Parameters[0].Default.(map[string]any)["nested"].([]any)[0] != "original" || original.Parameters[0].Enum[0].(map[string]any)["nested"].([]any)[0] != "original" || original.WorkflowMapping.Inputs[0].NodeID != "original" || original.WorkflowMapping.Outputs[0].NodeID != "original" {
		t.Fatal("image snapshot shares mutable nested data")
	}
}

func TestImageConfigAtomicSaveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	cfg.Image = &imaging.ImageSettings{Revision: imaging.MaxSafeInteger + 1}
	if err := cfg.SaveFile(path); err == nil {
		t.Fatal("expected invalid revision save to fail")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed existing file")
	}
}
