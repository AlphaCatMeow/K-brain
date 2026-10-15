package asyncadapter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// Ensure the reserved bindings are representable by the unchanged core settings
// validator. Production enabled evidence is intentionally absent.
func TestContractWorkflowFitsCoreSettings(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("configuration validation must not call upstream") }))
	defer s.Close()
	o := workflowOptions(s, false)
	o.Target.Parameters = append(o.Target.Parameters, imaging.ParameterSpec{Key: "input.reference", Type: "string"})
	o.Target.WorkflowMapping.Inputs = append(o.Target.WorkflowMapping.Inputs, imaging.WorkflowInput{ParameterKey: "input.reference", NodeID: "14", InputName: "image"})
	o.Target.Capabilities.OutputFormats = []string{"png"}
	cfg := imaging.ImageSettings{
		Connections: []imaging.ImageConnection{{ID: "ic_fixture", Vendor: "runninghub", MaxConcurrent: 1, Binding: imaging.ConnectionBinding{Kind: "independent", BaseURL: "https://www.runninghub.cn", AuthKind: "bearer", SecretRefs: map[string]string{"api_key": "secret_fixture"}}}},
		Targets:     []imaging.ImageTarget{{ID: "it_fixture", ConnectionID: "ic_fixture", Adapter: RunningHubID, AdapterVersion: Version, Kind: "workflow", RemoteID: o.Target.RemoteID, Revision: 1, Capabilities: o.Target.Capabilities, Parameters: o.Target.Parameters, WorkflowMapping: o.Target.WorkflowMapping}},
	}
	if e := imaging.ValidateSettings(cfg); e != nil {
		t.Fatalf("core mapping incompatibility: %v", e)
	}
	if _, e := NewRunningHub(o); e != nil {
		t.Fatal(e)
	}
}
