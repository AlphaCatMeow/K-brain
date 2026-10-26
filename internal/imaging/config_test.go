package imaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func testTarget() ImageTarget {
	return ImageTarget{ID: "it_test", Name: "test", ConnectionID: "ic_test", Adapter: "openai-images", AdapterVersion: "1", Kind: "model", RemoteID: "model", Revision: 1, Capabilities: TargetCapabilities{Operations: []Operation{OperationGenerate, OperationReference, OperationEdit, OperationMask}, MaxInputs: 4, MaxCount: 4, OutputFormats: []string{"png"}}, Parameters: []ParameterSpec{{Key: "quality", Type: "string", Enum: []any{"high", "low"}, Default: "high"}, {Key: "steps", Type: "integer", Minimum: ptr(1), Maximum: ptr(10), Required: true}}}
}
func ptr(v float64) *float64 { return &v }
func testSettings() ImageSettings {
	return ImageSettings{Connections: []ImageConnection{{ID: "ic_test", Name: "test", Vendor: "openai", Enabled: true, MaxConcurrent: 2, Binding: ConnectionBinding{Kind: "independent", BaseURL: "https://example.invalid", AuthKind: "bearer"}}}, Targets: []ImageTarget{testTarget()}}
}
func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ImageSettings)
		valid  bool
	}{
		{"empty legacy", func(s *ImageSettings) { *s = ImageSettings{} }, true},
		{"disabled known", func(s *ImageSettings) {}, true},
		{"empty targets", func(s *ImageSettings) { s.Targets = nil }, true},
		{"duplicate connection", func(s *ImageSettings) { s.Connections = append(s.Connections, s.Connections[0]) }, false},
		{"duplicate target", func(s *ImageSettings) { s.Targets = append(s.Targets, s.Targets[0]) }, false},
		{"dangling connection", func(s *ImageSettings) { s.Targets[0].ConnectionID = "ic_missing" }, false},
		{"empty provider ref", func(s *ImageSettings) { s.Connections[0].Binding = ConnectionBinding{Kind: "provider_ref"} }, false},
		{"unknown disabled", func(s *ImageSettings) { s.Targets[0].Adapter = "future-protocol" }, true},
		{"brand is not protocol", func(s *ImageSettings) { s.Targets[0].Adapter = "openai"; s.Targets[0].Enabled = true }, false},
		{"unknown enabled", func(s *ImageSettings) { s.Targets[0].Adapter = "future-protocol"; s.Targets[0].Enabled = true }, false},
		{"known unverified enabled", func(s *ImageSettings) { s.Targets[0].Enabled = true }, true},
		{"contract only enabled", func(s *ImageSettings) {
			s.Targets[0].Enabled = true
			s.Targets[0].Verification = TargetVerification{ContractComplete: true}
		}, true},
		{"verified enabled", func(s *ImageSettings) {
			v := &s.Targets[0]
			v.Enabled = true
			v.Verification = TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), ConnectionID: v.ConnectionID, AdapterVersion: v.AdapterVersion, TargetRevision: v.Revision}
		}, true},
		{"invalid kind", func(s *ImageSettings) { s.Targets[0].Kind = "script" }, false},
		{"invalid operation", func(s *ImageSettings) { s.Targets[0].Capabilities.Operations = []Operation{"other"} }, false},
		{"duplicate operation", func(s *ImageSettings) {
			s.Targets[0].Capabilities.Operations = []Operation{OperationGenerate, OperationGenerate}
		}, false},
		{"count cap", func(s *ImageSettings) { s.Targets[0].Capabilities.MaxCount = 17 }, false},
		{"mask insufficient inputs", func(s *ImageSettings) { s.Targets[0].Capabilities.MaxInputs = 1 }, false},
		{"unsafe revision", func(s *ImageSettings) { s.Revision = MaxSafeInteger + 1 }, false},
		{"enum type", func(s *ImageSettings) { s.Targets[0].Parameters[0].Enum = []any{1} }, false},
		{"default range", func(s *ImageSettings) { s.Targets[0].Parameters[1].Default = 11 }, false},
		{"duplicate parameter", func(s *ImageSettings) {
			s.Targets[0].Parameters = append(s.Targets[0].Parameters, s.Targets[0].Parameters[0])
		}, false},
		{"duplicate enum", func(s *ImageSettings) { s.Targets[0].Parameters[0].Enum = []any{"high", "high"} }, false},
		{"invalid schema type", func(s *ImageSettings) { s.Targets[0].Parameters[0].Type = "object" }, false},
		{"reversed range", func(s *ImageSettings) { s.Targets[0].Parameters[1].Minimum = ptr(20) }, false},
		{"workflow missing revision", func(s *ImageSettings) {
			s.Targets[0].Kind = "workflow"
			s.Targets[0].WorkflowMapping = &WorkflowMapping{}
		}, false},
		{"workflow undeclared", func(s *ImageSettings) {
			s.Targets[0].Kind = "workflow"
			s.Targets[0].WorkflowMapping = &WorkflowMapping{Revision: 1, Inputs: []WorkflowInput{{ParameterKey: "secret", NodeID: "1", InputName: "text"}}, Outputs: []WorkflowOutput{{NodeID: "1", OutputName: "image"}}}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings()
			tc.change(&s)
			err := ValidateSettings(s)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
func TestConfigProviderCatalog(t *testing.T) {
	s := testSettings()
	s.Connections[0].Binding = ConnectionBinding{Kind: "provider_ref", ProviderID: "provider_fixture"}
	if err := ValidateSettingsWithProviders(s, func(id string) bool { return id == "provider_fixture" }); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSettingsWithProviders(s, func(string) bool { return false }); err == nil {
		t.Fatal("dangling provider accepted")
	}
}
func TestPublicSecretsAndErrors(t *testing.T) {
	const sentinel = "SENTINEL_PRIVATE_CREDENTIAL"
	c := testSettings().Connections[0]
	c.Binding.CustomHeaders = map[string]string{"Authorization": sentinel, "X-Custom": sentinel}
	c.Binding.SecretRefs = map[string]string{"bearer": "exec:" + sentinel}
	raw, err := json.Marshal(PublicConnection(c, true))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("public connection contains private credential")
	}
	if !bytes.Contains(raw, []byte(`"credentialConfigured":true`)) {
		t.Fatal("missing credential state")
	}
	c.Binding.BaseURL = "https://example.invalid/?token=" + sentinel
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("URL credential leaked")
	}
	for _, value := range []any{ResolvedConnection{}, RemoteResult{Handle: sentinel}, OutputSource{URL: sentinel}, ExecuteRequest{Continuation: []byte(sentinel)}, ResolvedAsset{}, ImageSecretInput{Value: sentinel}} {
		if _, err = json.Marshal(value); err == nil {
			t.Fatal("private execution object serialized")
		}
	}
	ae := &AdapterError{Code: CodeAuthRequired, Delivery: DeliveryRejected, Cause: errors.New(sentinel)}
	if bytes.Contains([]byte(ae.Error()), []byte(sentinel)) {
		t.Fatal("cause leaked")
	}
	raw, err = json.Marshal(ae)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("serialized cause")
	}
	if !errors.Is(&AdapterError{Code: CodeUnsupportedOperation, Cause: ErrUnsupported}, ErrUnsupported) {
		t.Fatal("unsupported identity lost")
	}
	ae.Delivery = "success"
	if _, err = json.Marshal(ae); err == nil {
		t.Fatal("invalid delivery accepted")
	}
	e := ImageError{Code: CodeAuthRequired, Message: sentinel}
	raw, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("raw error message leaked")
	}
}
func TestPublicSnapshotIsolation(t *testing.T) {
	target := testTarget()
	snapshot := SnapshotTarget(target)
	target.Capabilities.Operations[0] = "changed"
	target.Parameters[0].Enum[0] = "changed"
	*target.Parameters[1].Minimum = 8
	if snapshot.Capabilities.Operations[0] != OperationGenerate || snapshot.Parameters[0].Enum[0] != "high" || *snapshot.Parameters[1].Minimum != 1 {
		t.Fatal("snapshot changed with settings")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enabled", "disabledReason", "verification", "liveVerifiedAt"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Fatal("snapshot contains mutable or diagnostic fields")
		}
	}
}
func TestWireFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version  string        `json:"version"`
		Settings ImageSettings `json:"settings"`
		Job      ImageJob      `json:"job"`
		Event    ImageEvent    `json:"event"`
		Error    ImageError    `json:"error"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != WireVersion || fixture.Job.Status != JobPartialSucceeded || fixture.Job.Usage != nil || fixture.Event.Data.Status != JobPartialSucceeded {
		t.Fatal("incomplete fixture")
	}
	if err = ValidateSettings(fixture.Settings); err != nil {
		t.Fatal(err)
	}
	jobRaw, err := json.Marshal(fixture.Job)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(jobRaw, []byte(`"usage":null`)) {
		t.Fatal("unknown usage must be null")
	}
	for _, value := range []any{ImageSettings{Revision: MaxSafeInteger + 1}, ImageSettingsUpdate{ExpectedRevision: MaxSafeInteger + 1}, ImageTarget{Revision: MaxSafeInteger + 1}, TargetSnapshot{Revision: MaxSafeInteger + 1}, WorkflowMapping{Revision: MaxSafeInteger + 1}, ImageProject{Revision: MaxSafeInteger + 1}, ImageEvent{Seq: MaxSafeInteger + 1}, ImageJob{Revision: MaxSafeInteger + 1}} {
		if _, err = json.Marshal(value); err == nil {
			t.Fatal("unsafe wire integer accepted")
		}
	}
	fixture.Event.Data.Status = ""
	if _, err = json.Marshal(fixture.Event); err == nil {
		t.Fatal("job.finished lacks status")
	}
}
