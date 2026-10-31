package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

// reviewerSizeUpstream records the size each generation/edit request carried.
func reviewerSizeUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(encoded.Bytes())
	var mu sync.Mutex
	var sizes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := "<none>"
		switch r.URL.Path {
		case "/v1/images/generations":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if s, ok := body["size"].(string); ok {
				size = s
			}
			for _, leaked := range []string{"aspectRatio", "imageSize"} {
				if _, ok := body[leaked]; ok {
					t.Errorf("%s leaked upstream", leaked)
				}
			}
		case "/v1/images/edits":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				break
			}
			for {
				part, err := reader.NextPart()
				if err != nil {
					break
				}
				raw, _ := io.ReadAll(part)
				if part.FormName() == "size" {
					size = string(raw)
				}
				part.Close()
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		mu.Lock()
		sizes = append(sizes, size)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": payload}}})
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sizes...)
	}
}

func reviewerWaitJob(t *testing.T, jobs interface {
	GetJob(context.Context, imaging.Scope, string) (imaging.ImageJob, error)
}, scope imaging.Scope, job imaging.ImageJob) imaging.ImageJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !imaging.IsTerminal(job.Status) {
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %s", job.ID, job.Status)
		}
		time.Sleep(5 * time.Millisecond)
		var err error
		if job, err = jobs.GetJob(context.Background(), scope, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

// Item 5: provider target -> HTTP-equivalent durable acceptance -> real adapter.
// aspectRatio/imageSize from the advertised schema must reach OpenAI as one size,
// for both the JSON generation route and the multipart edit route.
func TestReviewerProviderGPTImageRatioTierReachesUpstreamSize(t *testing.T) {
	upstream, sizes := reviewerSizeUpstream(t)
	defer upstream.Close()
	cfg := config.Default()
	cfg.Providers["proxy"] = config.Provider{API: "openai-completions", BaseURL: upstream.URL, APIKey: "fake-provider-key"}
	cfg.Models["gpt-image-2.5-flare"] = config.Model{Providers: []string{"proxy"}}
	runtime, err := openImageRuntime(context.Background(), t.TempDir(), backend.NewSettingsStore(cfg, func(*config.Config) error { return nil }), true)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	targets, err := runtime.module.Settings.ListImageTargets(scope)
	if err != nil || len(targets) != 1 || !targets[0].Enabled {
		t.Fatalf("targets=%d err=%v", len(targets), err)
	}
	project, err := runtime.store.CreateProject(context.Background(), scope, "sizes", "")
	if err != nil {
		t.Fatal(err)
	}
	gen := imaging.CreateJobRequest{ProjectID: project.ID, TargetID: targets[0].ID, Operation: imaging.OperationGenerate, Prompt: "synthetic", Count: 1, Parameters: map[string]any{"aspectRatio": "21:9", "imageSize": "4K", "quality": "max"}}
	job, _, err := runtime.module.Jobs.CreateJob(context.Background(), scope, "reviewer-gen", gen)
	if err != nil {
		t.Fatal(err)
	}
	job = reviewerWaitJob(t, runtime.module.Jobs, scope, job)
	if job.Status != imaging.JobSucceeded || len(job.AssetIDs) != 1 {
		t.Fatalf("generate status=%s error=%+v", job.Status, job.Error)
	}
	source := job.AssetIDs[0]
	edit := imaging.CreateJobRequest{ProjectID: project.ID, TargetID: targets[0].ID, Operation: imaging.OperationEdit, Prompt: "synthetic edit", Count: 1, ParentAssetID: source, Inputs: []imaging.InputAsset{{AssetID: source, Role: "source"}}, Parameters: map[string]any{"aspectRatio": "9:16"}}
	job, _, err = runtime.module.Jobs.CreateJob(context.Background(), scope, "reviewer-edit", edit)
	if err != nil {
		t.Fatal(err)
	}
	job = reviewerWaitJob(t, runtime.module.Jobs, scope, job)
	if job.Status != imaging.JobSucceeded {
		t.Fatalf("edit status=%s error=%+v", job.Status, job.Error)
	}
	if got := sizes(); len(got) != 2 || got[0] != "3808x1632" || got[1] != "720x1280" {
		t.Fatalf("upstream sizes=%v, want [3808x1632 720x1280]", got)
	}
}

type reviewerFrozenRegistry struct{ target imaging.TargetSnapshot }

func (r reviewerFrozenRegistry) ResolveTarget(context.Context, imaging.Scope, string) (imaging.TargetSnapshot, error) {
	return r.target, nil
}

// reviewerProviderRevision recomputes a provider target revision exactly as
// providerImageTargetsLocked does (the same JSON shape and digest truncation).
func reviewerProviderRevision(t *testing.T, p config.Provider, m config.Model, snap imaging.TargetSnapshot) uint64 {
	t.Helper()
	target := imaging.ImageTarget{Source: snap.Source, ProviderID: snap.ProviderID, ModelID: snap.ModelID, ID: snap.ID, Name: snap.Name, ConnectionID: snap.ConnectionID, Adapter: snap.Adapter, AdapterVersion: snap.AdapterVersion, Kind: snap.Kind, RemoteID: snap.RemoteID, Capabilities: snap.Capabilities, Parameters: snap.Parameters, WorkflowMapping: snap.WorkflowMapping}
	raw, err := json.Marshal(struct {
		Provider struct {
			Type           string
			API            string
			BaseURL        string
			IsFullURL      bool
			APIKey         string
			CustomHeaders  []config.CustomHeader
			UseSystemProxy bool
		}
		Model  config.Model
		Target imaging.TargetSnapshot
	}{struct {
		Type           string
		API            string
		BaseURL        string
		IsFullURL      bool
		APIKey         string
		CustomHeaders  []config.CustomHeader
		UseSystemProxy bool
	}{p.Type, p.API, p.BaseURL, p.IsFullURL, p.APIKey, p.CustomHeaders, p.UseSystemProxy}, m.Metadata(), imaging.SnapshotTarget(target)})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	revision := binary.BigEndian.Uint64(digest[:8]) & imaging.MaxSafeInteger
	if revision == 0 {
		revision = 1
	}
	return revision
}

// Item 4: a gpt-image-2 job accepted by the previous release (provider target
// snapshot with the fixed size enum) is still queued when the backend restarts on
// this change. Recovery goes through CheckProviderImageTarget, which DeepEquals
// the whole snapshot including Parameters, so the schema change fails the job
// closed before sending, like any other provider target change.
func TestReviewerQueuedPreUpgradeGPTImage2JobFailsClosedAfterUpgrade(t *testing.T) {
	upstream, sizes := reviewerSizeUpstream(t)
	defer upstream.Close()
	cfg := config.Default()
	provider := config.Provider{API: "openai-completions", BaseURL: upstream.URL, APIKey: "fake-provider-key"}
	model := config.Model{Providers: []string{"proxy"}}
	cfg.Providers["proxy"] = provider
	cfg.Models["gpt-image-2"] = model
	root := t.TempDir()
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}

	// Previous release: accept the job durably, then the process exits before running it.
	manager, err := backend.NewImageSettingsManager(backend.NewSettingsStore(cfg.Snapshot(), func(*config.Config) error { return nil }), filepath.Join(t.TempDir(), "secrets.json"), backend.ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := manager.ListImageTargets(scope)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%d err=%v", len(targets), err)
	}
	current, err := manager.ResolveTarget(context.Background(), scope, targets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewerProviderRevision(t, provider, model, current); got != current.Revision {
		t.Fatalf("revision formula drifted: %d != %d", got, current.Revision)
	}
	old := current
	old.Parameters = direct.ProtocolProfile(direct.OpenAIID).Parameters // ModelParameters before 86cf85a
	old.Revision = reviewerProviderRevision(t, provider, model, old)
	store, err := imaging.OpenStore(filepath.Join(root, "imaging"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), scope, "pre-upgrade", "")
	if err != nil {
		t.Fatal(err)
	}
	previous := imaging.NewService(store, reviewerFrozenRegistry{old}, nil)
	queued, _, err := previous.CreateJob(context.Background(), scope, "pre-upgrade", imaging.CreateJobRequest{ProjectID: project.ID, TargetID: old.ID, Operation: imaging.OperationGenerate, Prompt: "synthetic", Count: 1, Parameters: map[string]any{"size": "1536x1024"}})
	if err != nil || queued.Status != imaging.JobQueued {
		t.Fatalf("pre-upgrade accept status=%s err=%v", queued.Status, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// This release: restart on the same data root and configuration.
	runtime, err := openImageRuntime(context.Background(), root, backend.NewSettingsStore(cfg.Snapshot(), func(*config.Config) error { return nil }), true)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	job := reviewerWaitJob(t, runtime.module.Jobs, scope, queued)
	if job.Status != imaging.JobFailed || job.Error == nil || job.Error.Code != imaging.CodeRevisionConflict {
		t.Fatalf("pre-upgrade gpt-image-2 job status=%s error=%+v (want failed with %s)", job.Status, job.Error, imaging.CodeRevisionConflict)
	}
	if got := sizes(); len(got) != 0 {
		t.Fatalf("stale snapshot reached upstream: %v", got)
	}
}
