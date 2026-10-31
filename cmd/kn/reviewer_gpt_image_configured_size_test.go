package main

import (
	"context"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

// Round-2 check of the documented claim: a configured (standalone) target keeps
// its persisted size schema, and for GPT Image 2/2.5 the adapter accepts any WxH
// within OpenAI's rules. Uses the production runtime (openImageRuntime ->
// resolveVerifiedImageAdapter -> ResolveImageTargetConnection -> NewOpenAI).
func TestReviewerConfiguredTargetPersistedSizeSchema(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		schema      []string
		size        string
		wantStatus  imaging.JobStatus
		wantCode    string
		wantWire    []string
	}{
		{"three-value enum on gpt-image-2", "gpt-image-2", []string{"1024x1024", "1536x1024", "1024x1536"}, "1536x1024", imaging.JobSucceeded, "", []string{"1536x1024"}},
		{"custom 2048x2048 on gpt-image-2", "gpt-image-2", []string{"2048x2048"}, "2048x2048", imaging.JobSucceeded, "", []string{"2048x2048"}},
		{"custom 3840x2160 on dated 2.5-flare", "gpt-image-2.5-flare-2026-09-08", []string{"3840x2160"}, "3840x2160", imaging.JobSucceeded, "", []string{"3840x2160"}},
		{"persisted value outside OpenAI rules", "gpt-image-2", []string{"1000x1000"}, "1000x1000", imaging.JobFailed, imaging.CodeInvalidInput, nil},
		{"custom size on fixed-size 1.5 stays rejected", "gpt-image-1.5", []string{"2048x2048"}, "2048x2048", imaging.JobFailed, imaging.CodeInvalidInput, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, sizes := reviewerSizeUpstream(t)
			defer upstream.Close()
			cfg := config.Default()
			cfg.Providers["image-test"] = config.Provider{API: "openai-completions", BaseURL: upstream.URL + "/v1", APIKey: "fake-image-key"}
			values := make([]any, 0, len(tc.schema))
			for _, v := range tc.schema {
				values = append(values, v)
			}
			cfg.Image = &imaging.ImageSettings{Revision: 1, Connections: []imaging.ImageConnection{{ID: "ic_test", Name: "test", Vendor: "openai", Enabled: true, MaxConcurrent: 1, Binding: imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "image-test"}}}, Targets: []imaging.ImageTarget{{ID: "it_test", Name: "test", ConnectionID: "ic_test", Adapter: direct.OpenAIID, AdapterVersion: direct.Version, Kind: "model", RemoteID: tc.model, Revision: 1, Enabled: true, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, MaxCount: 1, OutputFormats: []string{"png"}}, Parameters: []imaging.ParameterSpec{{Key: "size", Label: "Size", Type: "string", Enum: values}}, Verification: imaging.TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Now(), ConnectionID: "ic_test", AdapterVersion: direct.Version, TargetRevision: 1}}}}
			runtime, err := openImageRuntime(context.Background(), t.TempDir(), backend.NewSettingsStore(cfg, func(*config.Config) error { return nil }), true)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close(context.Background())
			scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
			project, err := runtime.store.CreateProject(context.Background(), scope, "configured", "")
			if err != nil {
				t.Fatal(err)
			}
			job, _, err := runtime.module.Jobs.CreateJob(context.Background(), scope, "configured-size", imaging.CreateJobRequest{ProjectID: project.ID, TargetID: "it_test", Operation: imaging.OperationGenerate, Prompt: "synthetic", Count: 1, Parameters: map[string]any{"size": tc.size}})
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			if job.TargetSnapshot.Source == "provider" {
				t.Fatal("fixture resolved a provider target instead of the configured one")
			}
			// not_sent adapter errors are retried 3 times with backoff before failing.
			for deadline := time.Now().Add(30 * time.Second); !imaging.IsTerminal(job.Status); time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("job did not finish: %s", job.Status)
				}
				if job, err = runtime.module.Jobs.GetJob(context.Background(), scope, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			code := ""
			if job.Error != nil {
				code = job.Error.Code
			}
			got := sizes()
			if job.Status != tc.wantStatus || code != tc.wantCode || len(got) != len(tc.wantWire) || (len(got) == 1 && got[0] != tc.wantWire[0]) {
				t.Fatalf("status=%s code=%q wire=%v, want status=%s code=%q wire=%v", job.Status, code, got, tc.wantStatus, tc.wantCode, tc.wantWire)
			}
		})
	}
}
