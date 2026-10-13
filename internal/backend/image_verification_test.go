package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

type verificationFixture struct {
	t         *testing.T
	dir       string
	m         *ImageSettingsManager
	store     *imaging.Store
	assets    *imaging.AssetStore
	service   *imaging.Service
	scheduler *imaging.Scheduler
	verifier  *ImageVerificationManager
	input     imaging.VerifyTargetRequest
	calls     atomic.Int32
	failSave  atomic.Bool
	server    *httptest.Server
	adapter   string
}

func newVerificationFixture(t *testing.T, adapter, result string) *verificationFixture {
	t.Helper()
	model := "gpt-image-2"
	if adapter == direct.VolcengineID {
		model = "doubao-seedream-5-0-pro-260628"
	}
	f := &verificationFixture{t: t, dir: t.TempDir(), adapter: adapter}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/images/generations") || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != model || body["prompt"] != "fixture prompt" {
			t.Error("confirmation was not executed")
		}
		switch result {
		case "empty":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "rejected":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"private sentinel"}}`))
		case "unknown":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": encoded}}})
		}
	}))
	t.Cleanup(f.server.Close)
	f.open(false)
	t.Cleanup(f.close)
	ctx, scope := context.Background(), imageTestScope()
	c := imageTestConnection()
	c.Binding.BaseURL = f.server.URL + "/v1"
	c.MaxConcurrent = 1
	target := imageTestTarget()
	target.Adapter = adapter
	target.AdapterVersion = "1"
	target.RemoteID = model
	target.Verification = imaging.TargetVerification{}
	s, err := f.m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{c}, Targets: []imaging.ImageTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := f.m.WriteImageSecret(ctx, scope, imaging.ImageSecretInput{ConnectionID: c.ID, Purpose: "apiKey", Value: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	c.Binding.SecretRefs = map[string]string{"apiKey": secret.Ref}
	s, err = f.m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: s.Revision, Connections: []imaging.ImageConnection{c}, Targets: []imaging.ImageTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := f.store.CreateProject(ctx, scope, "Verification fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	revision, targetRevision := s.Revision, target.Revision
	f.input = imaging.VerifyTargetRequest{ExpectedRevision: &revision, ProjectID: project.ID, ConnectionID: c.ID, RemoteID: target.RemoteID, TargetRevision: &targetRevision, Operation: imaging.OperationGenerate, Count: 1, Prompt: "fixture prompt", ConfirmPaid: true}
	return f
}
func (f *verificationFixture) open(reload bool) {
	f.t.Helper()
	cfg := config.Default()
	var err error
	if reload {
		cfg, err = config.LoadFile(filepath.Join(f.dir, "config.json"))
		if err != nil {
			f.t.Fatal(err)
		}
	}
	settings := NewSettingsStore(cfg, func(c *config.Config) error {
		if f.failSave.Load() {
			return errors.New("private save sentinel")
		}
		return c.SaveFile(filepath.Join(f.dir, "config.json"))
	})
	f.m, err = NewImageSettingsManager(settings, filepath.Join(f.dir, "secrets.json"), ImageSettingsOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	f.store, err = imaging.OpenStore(filepath.Join(f.dir, "store"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.assets, err = imaging.NewAssetStore(f.store, filepath.Join(f.dir, "assets"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.service = imaging.NewService(f.store, f.m, nil, imaging.ServiceOptions{AssetSink: f.assets, InputResolver: f.assets, PollInterval: time.Millisecond, AdapterResolver: func(ctx context.Context, scope imaging.Scope, target imaging.TargetSnapshot) (imaging.Adapter, error) {
		if err := f.verifier.CheckExecution(ctx, scope, target); err != nil {
			return nil, err
		}
		c, err := f.m.ResolveImageConnection(ctx, scope, target.ConnectionID)
		if err != nil {
			return nil, err
		}
		opts := direct.Options{HTTPClient: f.server.Client(), BaseURL: c.BaseURL, Scope: scope, SecretResolver: c.SecretResolver, SecretRef: c.SecretRefs["apiKey"]}
		c.HTTPClient.CloseIdleConnections()
		if target.Adapter == direct.VolcengineID {
			return direct.NewVolcengine(opts)
		}
		return direct.NewOpenAI(opts)
	}})
	f.verifier = NewImageVerificationManager(f.m, f.service, f.assets)
	f.scheduler = imaging.NewScheduler(f.service)
}
func (f *verificationFixture) run() {
	f.t.Helper()
	if err := f.scheduler.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}
func (f *verificationFixture) close() {
	if f.scheduler != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.scheduler.Close(ctx); err != nil {
			f.t.Error(err)
		}
		f.scheduler = nil
	}
	if f.assets != nil {
		_ = f.assets.Close()
		f.assets = nil
	}
	if f.store != nil {
		_ = f.store.Close()
		f.store = nil
	}
}
func (f *verificationFixture) start(key string) imaging.VerificationReceipt {
	f.t.Helper()
	r, _, err := f.verifier.Start(context.Background(), imageTestScope(), "it_test", key, f.input)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *verificationFixture) wait(id string) imaging.ImageJob {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := f.service.GetJob(context.Background(), imageTestScope(), id)
		if err != nil {
			f.t.Fatal(err)
		}
		switch j.Status {
		case imaging.JobSucceeded, imaging.JobFailed, imaging.JobCancelled, imaging.JobResultUnknown:
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatal("job did not finish")
	return imaging.ImageJob{}
}
func (f *verificationFixture) get(id string) imaging.VerificationReceipt {
	f.t.Helper()
	r, err := f.verifier.Get(context.Background(), imageTestScope(), "it_test", id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestImageVerificationDurableLifecycle(t *testing.T) {
	for _, adapter := range []string{direct.OpenAIID, direct.VolcengineID} {
		t.Run(adapter, func(t *testing.T) {
			f := newVerificationFixture(t, adapter, "success")
			// Ordinary creation cannot bypass the disabled target, even with its own key.
			ordinary := imaging.CreateJobRequest{ProjectID: f.input.ProjectID, TargetID: "it_test", Operation: imaging.OperationGenerate, Count: 1, Prompt: "fixture prompt"}
			if _, _, err := f.service.CreateJob(context.Background(), imageTestScope(), "ordinary", ordinary); err == nil {
				t.Fatal("ordinary disabled target accepted")
			}
			r := f.start("double-click")
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					next, replay, err := f.verifier.Start(context.Background(), imageTestScope(), "it_test", "double-click", f.input)
					if err != nil || !replay || next.JobID != r.JobID {
						t.Errorf("bad replay: %+v %v", next, err)
					}
				}()
			}
			wg.Wait()
			// Restart before submission: same durable job, one billable request.
			f.close()
			f.open(true)
			f.run()
			if j := f.wait(r.JobID); j.Status != imaging.JobSucceeded {
				t.Fatalf("job: %+v", j)
			}
			f.failSave.Store(true)
			if _, err := f.verifier.Get(context.Background(), imageTestScope(), "it_test", r.ID); err == nil {
				t.Fatal("save failure issued evidence")
			}
			f.failSave.Store(false)
			got := f.get(r.ID)
			if got.State != "verified" || got.SettingsRevision != *f.input.ExpectedRevision+1 {
				t.Fatalf("receipt: %+v", got)
			}
			if again := f.get(r.ID); again != got {
				t.Fatalf("finalize not idempotent: %+v", again)
			}
			settings, _ := f.m.ImageSettings(imageTestScope())
			if settings.Targets[0].Enabled || settings.Targets[0].VerificationState.State != "verified" {
				t.Fatal("verification auto-enabled or missing summary")
			}
			raw, _ := json.Marshal(settings.Targets[0])
			for _, secret := range []string{"fixture-key", "bindingDigest", "secretRefs", f.server.URL} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("private verification material leaked")
				}
			}
			settings.Targets[0].Enabled = true
			if _, err := f.m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{ExpectedRevision: settings.Revision, Connections: settings.Connections, Targets: settings.Targets}); err != nil {
				t.Fatal(err)
			}
			f.close()
			f.open(true)
			if next := f.start("double-click"); next.JobID != r.JobID {
				t.Fatal("restart changed job identity")
			}
			if got := f.get(r.ID); got.State != "verified" {
				t.Fatalf("persisted evidence lost: %+v", got)
			}
			if f.calls.Load() != 1 {
				t.Fatalf("requests: %d", f.calls.Load())
			}
			// The same ref rotates in place, so ref comparison alone is insufficient.
			before, _ := f.m.ImageSettings(imageTestScope())
			rotated, err := f.m.WriteImageSecret(context.Background(), imageTestScope(), imaging.ImageSecretInput{ConnectionID: "ic_test", Purpose: "apiKey", Value: "rotated fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if rotated.Ref != before.Connections[0].Binding.SecretRefs["apiKey"] {
				t.Fatal("fixture did not rotate in place")
			}
			if got := f.get(r.ID); got.State != "stale" {
				t.Fatal("rotation preserved evidence")
			}
			if _, err := f.m.ResolveTarget(context.Background(), imageTestScope(), "it_test"); err == nil {
				t.Fatal("rotated target remained executable")
			}
			// Rotating back to the original literal must still invalidate old evidence.
			_, _ = f.m.WriteImageSecret(context.Background(), imageTestScope(), imaging.ImageSecretInput{ConnectionID: "ic_test", Purpose: "apiKey", Value: "fixture-key"})
			if got := f.get(r.ID); got.State != "stale" {
				t.Fatal("credential generation was not bound")
			}
		})
	}
}

func TestImageVerificationFailuresNeverIssueEvidenceOrResubmit(t *testing.T) {
	for _, result := range []string{"empty", "rejected", "unknown"} {
		t.Run(result, func(t *testing.T) {
			f := newVerificationFixture(t, direct.OpenAIID, result)
			r := f.start("one")
			f.run()
			j := f.wait(r.JobID)
			if j.Status == imaging.JobSucceeded {
				t.Fatal("bad upstream succeeded")
			}
			got := f.get(r.ID)
			if got.State == "verified" {
				t.Fatal("failure certified")
			}
			f.close()
			f.open(true)
			f.run()
			again := f.start("one")
			if again.JobID != r.JobID {
				t.Fatal("restart retried charge")
			}
			f.wait(r.JobID)
			if f.calls.Load() != 1 {
				t.Fatalf("charged %d times", f.calls.Load())
			}
			settings, _ := f.m.ImageSettings(imageTestScope())
			if settings.Targets[0].VerificationState.State == "verified" {
				t.Fatal("evidence issued")
			}
		})
	}
}

func TestImageVerificationConsentProfilesAndRevision(t *testing.T) {
	f := newVerificationFixture(t, direct.OpenAIID, "success")
	for _, change := range []func(*imaging.VerifyTargetRequest){func(r *imaging.VerifyTargetRequest) { r.ConfirmPaid = false }, func(r *imaging.VerifyTargetRequest) { r.Count = 2 }, func(r *imaging.VerifyTargetRequest) { r.Operation = imaging.OperationEdit }, func(r *imaging.VerifyTargetRequest) { r.RemoteID = "other" }, func(r *imaging.VerifyTargetRequest) { r.ConnectionID = "other" }, func(r *imaging.VerifyTargetRequest) { r.ExpectedRevision = nil }, func(r *imaging.VerifyTargetRequest) { r.Prompt = "" }} {
		r := f.input
		change(&r)
		if _, _, err := f.verifier.Start(context.Background(), imageTestScope(), "it_test", "reject", r); err == nil {
			t.Fatal("invalid consent accepted")
		}
	}
	r := f.start("key")
	changed := f.input
	changed.Prompt = "changed"
	if _, _, err := f.verifier.Start(context.Background(), imageTestScope(), "it_test", "key", changed); err == nil {
		t.Fatal("key payload conflict ignored")
	}
	settings, _ := f.m.ImageSettings(imageTestScope())
	settings.Targets[0].Name = "rename"
	if _, err := f.m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{ExpectedRevision: settings.Revision, Connections: settings.Connections, Targets: settings.Targets}); err != nil {
		t.Fatal(err)
	}
	f.run()
	f.wait(r.JobID)
	if got := f.get(r.ID); got.State != "stale" {
		t.Fatalf("revision change certified: %+v", got)
	}
	if f.calls.Load() != 0 {
		t.Fatal("changed queued binding submitted")
	}
	for _, adapter := range []string{direct.GoogleID, "runninghub-workflow", "jimeng-image"} {
		snapshot := imaging.SnapshotTarget(imageTestTarget())
		snapshot.Adapter = adapter
		snapshot.AdapterVersion = "1"
		if imaging.ValidateVerificationProfile(snapshot) == nil {
			t.Fatalf("unsupported adapter %s", adapter)
		}
	}
	snapshot := imaging.SnapshotTarget(imageTestTarget())
	snapshot.AdapterVersion = "1"
	snapshot.Capabilities.Operations = append(snapshot.Capabilities.Operations, imaging.OperationEdit)
	if imaging.ValidateVerificationProfile(snapshot) == nil {
		t.Fatal("generate endorsed edit")
	}
}

func TestImageVerificationHTTPAuthorizationAndWire(t *testing.T) {
	f := newVerificationFixture(t, direct.OpenAIID, "success")
	s := &Server{token: "test-image-token", image: &ImageModule{Settings: f.m, Store: f.store, Assets: f.assets, Jobs: f.service, Verification: f.verifier, AllowLocalImageScope: true}}
	body, _ := json.Marshal(f.input)
	req := func(raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/image/targets/it_test/verify", bytes.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer test-image-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Idempotency-Key", "http")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	s.image.ResolveScope = func(*http.Request) (ImageAuthorization, error) {
		return ImageAuthorization{Scope: imageTestScope()}, nil
	}
	imageAssertError(t, req(body), 403, imaging.CodeForbidden)
	s.image.ResolveScope = nil
	imageAssertError(t, req([]byte(strings.TrimSuffix(string(body), "}")+`,"verification":{"state":"verified"}}`)), 422, imaging.CodeInvalidInput)
	w := req(body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		Version      string                      `json:"version"`
		Verification imaging.VerificationReceipt `json:"verification"`
		Replay       bool                        `json:"replay"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Version != imaging.WireVersion || out.Verification.JobID == "" {
		t.Fatal(w.Body.String())
	}
	f.run()
	f.wait(out.Verification.JobID)
	w = imageHTTPRequest(s, "GET", "/v1/image/targets/it_test/verifications/"+out.Verification.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"verified"`) {
		t.Fatal(w.Body.String())
	}
	s.image.ResolveScope = func(*http.Request) (ImageAuthorization, error) {
		return ImageAuthorization{Scope: imageTestScope()}, nil
	}
	imageAssertError(t, imageHTTPRequest(s, "GET", "/v1/image/targets/it_test/verifications/"+out.Verification.ID, nil), 403, imaging.CodeForbidden)
	if f.calls.Load() != 1 {
		t.Fatal("HTTP duplicate submit")
	}
}
