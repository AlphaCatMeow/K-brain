package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// Exercise discovery -> durable acceptance -> real adapter -> saved asset, with
// no standalone image config or paid verification receipts, against loopback only.
func TestImageRuntimeProviderDerivedTargets(t *testing.T) {
	for _, tc := range []struct {
		name, api, model, path string
		explicit               bool
	}{
		{"OpenAI known model", "openai-responses", "gpt-image-1", "/v1/images/generations", false},
		{"OpenAI declared alias", "openai-completions", "private-painter", "/v1/images/generations", true},
		{"Google known model", "google-generative-ai", "gemini-2.5-flash-image", "/v1beta/models/gemini-2.5-flash-image:generateContent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			payload := base64.StdEncoding.EncodeToString(encoded.Bytes())
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				edit := tc.api != "google-generative-ai" && r.URL.Path == "/v1/images/edits"
				if r.Method != http.MethodPost || (!edit && r.URL.Path != tc.path) || r.Header.Get("X-Fixture") != "inherited" {
					t.Error("wrong image request route or headers")
					w.WriteHeader(400)
					return
				}
				if edit {
					if r.Header.Get("Authorization") != "Bearer fake-provider-key" {
						t.Error("wrong edit authentication")
					}
					reader, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					images := 0
					for {
						part, err := reader.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Error(err)
							break
						}
						raw, err := io.ReadAll(part)
						if err != nil {
							t.Error(err)
						}
						if part.FormName() == "image[]" {
							images++
							if !bytes.Equal(raw, encoded.Bytes()) {
								t.Error("edit changed saved source bytes")
							}
						}
						if part.FormName() == "model" && string(raw) != tc.model {
							t.Error("wrong multipart model")
						}
						part.Close()
					}
					if images != 1 {
						t.Error("edit did not upload exactly one saved source")
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": payload}}})
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.api == "google-generative-ai" {
					if r.Header.Get("x-goog-api-key") != "fake-provider-key" || r.Header.Get("Authorization") != "" {
						t.Error("wrong Google authentication")
					}
					if _, ok := request["contents"]; !ok {
						t.Error("missing Google contents")
					}
					json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"role": "model", "parts": []any{map[string]any{"inlineData": map[string]string{"mimeType": "image/png", "data": payload}}}}}}})
				} else {
					if r.Header.Get("Authorization") != "Bearer fake-provider-key" || request["model"] != tc.model {
						t.Error("wrong OpenAI authentication/model")
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": payload}}})
				}
			}))
			defer upstream.Close()
			cfg := config.Default()
			cfg.Providers["image-provider"] = config.Provider{API: tc.api, BaseURL: upstream.URL, APIKey: "fake-provider-key", CustomHeaders: []config.CustomHeader{{Key: "X-Fixture", Value: "inherited"}}}
			model := config.Model{Providers: []string{"image-provider"}}
			if tc.explicit {
				model.OutputModalities = []string{"image"}
			}
			cfg.Models[tc.model] = model
			runtime, err := openImageRuntime(context.Background(), t.TempDir(), backend.NewSettingsStore(cfg, func(*config.Config) error { return nil }), true)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close(context.Background())
			scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
			targets, err := runtime.module.Settings.ListImageTargets(scope)
			if err != nil || len(targets) != 1 || !targets[0].Enabled {
				t.Fatalf("discover targets=%d err=%v", len(targets), err)
			}
			if calls.Load() != 0 {
				t.Fatal("listing triggered a paid request")
			}
			project, err := runtime.store.CreateProject(context.Background(), scope, "derived fixture", "")
			if err != nil {
				t.Fatal(err)
			}
			request := imaging.CreateJobRequest{ProjectID: project.ID, TargetID: targets[0].ID, Operation: imaging.OperationGenerate, Prompt: "synthetic fixture", Count: 1}
			job, replay, err := runtime.module.Jobs.CreateJob(context.Background(), scope, "provider-fixture", request)
			if err != nil || replay {
				t.Fatalf("accept replay=%v err=%v", replay, err)
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for !imaging.IsTerminal(job.Status) {
				select {
				case <-deadline.C:
					t.Fatal("image job did not finish")
				case <-tick.C:
					job, err = runtime.module.Jobs.GetJob(context.Background(), scope, job.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if job.Status != imaging.JobSucceeded || len(job.AssetIDs) != 1 || calls.Load() != 1 {
				t.Fatalf("status=%s assets=%d calls=%d error=%+v", job.Status, len(job.AssetIDs), calls.Load(), job.Error)
			}
			if job.TargetSnapshot.Source != "provider" {
				t.Fatal("durable job lost provider target provenance")
			}
			_, replay, err = runtime.module.Jobs.CreateJob(context.Background(), scope, "provider-fixture", request)
			if err != nil || !replay || calls.Load() != 1 {
				t.Fatal("replay resubmitted upstream")
			}
			source := job.AssetIDs[0]
			editRequest := imaging.CreateJobRequest{ProjectID: project.ID, TargetID: targets[0].ID, Operation: imaging.OperationEdit, Prompt: "synthetic edit", Count: 1, ParentAssetID: source, Inputs: []imaging.InputAsset{{AssetID: source, Role: "source"}}}
			if tc.api == "google-generative-ai" {
				editRequest.Parameters = map[string]any{"aspectRatio": "16:9"}
			} else {
				editRequest.Parameters = map[string]any{"size": "1024x1024"}
			}
			job, replay, err = runtime.module.Jobs.CreateJob(context.Background(), scope, "provider-edit-fixture", editRequest)
			if err != nil || replay {
				t.Fatalf("accept edit: replay=%v err=%v", replay, err)
			}
			for !imaging.IsTerminal(job.Status) {
				select {
				case <-deadline.C:
					t.Fatal("derived edit job did not finish")
				case <-tick.C:
					job, err = runtime.module.Jobs.GetJob(context.Background(), scope, job.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if job.Status != imaging.JobSucceeded || len(job.AssetIDs) != 1 || calls.Load() != 2 {
				t.Fatalf("edit status=%s assets=%d calls=%d error=%+v", job.Status, len(job.AssetIDs), calls.Load(), job.Error)
			}
		})
	}
}
