package asyncadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type testSecrets struct{}

func (testSecrets) ResolveSecret(_ context.Context, _ imaging.Scope, ref string) (string, error) {
	return "fixture-" + ref, nil
}
func options(s *httptest.Server) Options {
	return Options{Connection: imaging.ResolvedConnection{HTTPClient: s.Client(), SecretResolver: testSecrets{}}, BaseURL: s.URL, SecretRef: "api", AccessKeyRef: "ak", SecretKeyRef: "sk", Now: func() time.Time { return time.Date(2026, 10, 11, 12, 34, 56, 0, time.UTC) }}
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func jimengRequest() imaging.ExecuteRequest {
	return imaging.ExecuteRequest{Target: imaging.TargetSnapshot{Adapter: JimengID, AdapterVersion: Version, RemoteID: jimengKey}, Request: imaging.CreateJobRequest{Operation: imaging.OperationGenerate, Prompt: "ceramic bird", Count: 1, Parameters: map[string]any{"size": 4194304}}}
}
func assertError(t *testing.T, e error, code string, d imaging.Delivery) {
	t.Helper()
	var a *imaging.AdapterError
	if !errors.As(e, &a) || a.Code != code || a.Delivery != d {
		t.Fatalf("unexpected normalized error: %v", e)
	}
	if errors.Unwrap(e) != nil {
		t.Fatal("raw cause escaped")
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "fixture-api") || strings.Contains(e.Error(), "secret") {
		t.Fatal("secret escaped")
	}
}
func TestContractJimengSubmitPoll(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/" || r.URL.Query().Get("Version") != "2022-08-31" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong visual protocol")
		}
		raw, _ := io.ReadAll(r.Body)
		if hash(raw) != r.Header.Get("X-Content-Sha256") || !strings.Contains(r.Header.Get("Authorization"), "/cn-north-1/cv/request") {
			t.Error("wrong signature headers")
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		switch r.URL.Query().Get("Action") {
		case "CVSync2AsyncSubmitTask":
			if body["force_single"] != true || body["prompt"] != "ceramic bird" || body["req_key"] != jimengKey || body["size"] != float64(4194304) {
				t.Error("incorrect submit body")
			}
			_, _ = w.Write(fixture(t, "jimeng-submit.json"))
		case "CVSync2AsyncGetResult":
			if body["task_id"] != "fixture-visual-task" || body["req_json"] != `{"return_url":true}` {
				t.Error("incorrect poll body")
			}
			_, _ = w.Write(fixture(t, "jimeng-done.json"))
		default:
			t.Error("unexpected action")
		}
	}))
	defer s.Close()
	a, e := NewJimeng(options(s))
	if e != nil {
		t.Fatal(e)
	}
	got, e := a.Submit(context.Background(), jimengRequest())
	if e != nil || got.State != "pending" || got.Handle != "fixture-visual-task" {
		t.Fatalf("submit: %+v %v", got, e)
	}
	// Fresh instance demonstrates durable-handle recovery without submit memory.
	fresh, _ := NewJimeng(options(s))
	got, e = fresh.Poll(context.Background(), got.Handle)
	if e != nil || got.State != "completed" || len(got.Outputs) != 1 || got.Outputs[0].URL != "https://example.invalid/image.png?private=fixture" || calls != 2 {
		t.Fatalf("poll: %+v %v", got, e)
	}
	if _, e = json.Marshal(got); e == nil {
		t.Fatal("private remote result serialized")
	}
	if a.Cancel(context.Background(), "h") != imaging.ErrUnsupported {
		t.Fatal("invented cancel")
	}
	if _, e = a.Reconcile(context.Background(), "h"); e != imaging.ErrUnsupported {
		t.Fatal("invented reconcile")
	}
}
func TestContractVisualSignatureDeterminism(t *testing.T) {
	body := []byte(`{"req_key":"jimeng_t2i_v40","prompt":"fixture","force_single":true}`)
	req, _ := http.NewRequest("POST", "https://visual.volcengineapi.com/?Version=2022-08-31&Action=CVSync2AsyncSubmitTask", nil)
	now := time.Date(2026, 10, 11, 12, 34, 56, 0, time.UTC)
	canonical := signVisual(req, body, "fixture-ak", "fixture-sk", "", now)
	// Self-built vector from the documented algorithm; NOT an official CV vector.
	expected := strings.ReplaceAll(string(fixture(t, "visual-canonical.txt")), "\r\n", "\n")
	if canonical != strings.TrimSuffix(expected, "\n") {
		t.Fatalf("canonical mismatch\n%s", canonical)
	}
	auth := req.Header.Get("Authorization")
	want := strings.TrimSpace(string(fixture(t, "visual-authorization.txt")))
	if auth != want {
		t.Fatalf("authorization mismatch: %s", auth)
	}
	req2, _ := http.NewRequest("POST", req.URL.String(), nil)
	signVisual(req2, body, "fixture-ak", "fixture-sk", "", now.In(time.FixedZone("offset", 3600)))
	if req2.Header.Get("Authorization") != auth {
		t.Fatal("UTC signature not deterministic")
	}
	signVisual(req2, body, "fixture-ak", "fixture-sk", "fixture-sts", now)
	if req2.Header.Get("X-Security-Token") != "fixture-sts" || !strings.Contains(req2.Header.Get("Authorization"), "x-date;x-security-token") || req2.Header.Get("Authorization") == auth {
		t.Fatal("STS omitted from signature")
	}
}
func TestContractJimengStatesAndErrors(t *testing.T) {
	cases := []struct{ name, body, state, code string }{
		{"queue", `{"code":10000,"data":{"status":"in_queue"}}`, "pending", ""},
		{"running", `{"code":10000,"data":{"status":"generating"}}`, "pending", ""},
		{"expired", `{"code":10000,"data":{"status":"expired"}}`, "unknown", imaging.CodeResultUnknown},
		{"missing", `{"code":10000,"data":{"status":"not_found"}}`, "unknown", imaging.CodeResultUnknown},
		{"new", `{"code":10000,"data":{"status":"surprise"}}`, "unknown", imaging.CodeResultUnknown},
		{"empty", `{"code":10000,"data":{"status":"done"}}`, "unknown", imaging.CodeNoImageOutput},
		{"invalid-url", `{"code":10000,"data":{"status":"done","image_urls":["file:///secret"]}}`, "unknown", imaging.CodeResultUnknown},
		{"rate", `{"code":50429,"message":"secret upstream URL"}`, "unknown", imaging.CodeRateLimited},
		{"clock", `{"ResponseMetadata":{"Error":{"Code":"InvalidTimestamp","Message":"secret"}}}`, "unknown", imaging.CodeAuthRequired},
		{"auth", `{"ResponseMetadata":{"Error":{"Code":"SignatureDoesNotMatch","Message":"secret"}}}`, "unknown", imaging.CodeAuthRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer s.Close()
			a, _ := NewJimeng(options(s))
			got, e := a.Poll(context.Background(), "kept-handle")
			if got.State != tc.state || got.Handle != "kept-handle" || got.ErrorCode != tc.code {
				t.Fatalf("wrong result %+v %v", got, e)
			}
			if e != nil && strings.Contains(e.Error(), "secret") {
				t.Fatal("unsafe error")
			}
		})
	}
}
func TestContractSubmitTimeoutNoReplay(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer s.Close()
	o := options(s)
	a, _ := NewJimeng(o)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, e := a.Submit(ctx, jimengRequest())
	assertError(t, e, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	if got.State != "unknown" || calls.Load() != 1 {
		t.Fatal("uncertain submit replayed")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, e = a.Submit(ctx2, jimengRequest())
	assertError(t, e, imaging.CodeUnavailable, imaging.DeliveryNotSent)
}

const graphFixture = `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":""}},"14":{"class_type":"LoadImage","inputs":{"image":"input.png"}},"9":{"class_type":"SaveImage","inputs":{"images":["14",0]}}}`

func workflowOptions(s *httptest.Server, webhook bool) WorkflowOptions {
	o := WorkflowOptions{Options: options(s), APIFormatJSON: []byte(graphFixture), Target: imaging.TargetSnapshot{ID: "fixture-target", Adapter: RunningHubID, AdapterVersion: Version, Kind: "workflow", RemoteID: "fixture-workflow", Revision: 1, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference}, MaxCount: 1, MaxInputs: 1}, WorkflowMapping: &imaging.WorkflowMapping{Revision: 1, Inputs: []imaging.WorkflowInput{{ParameterKey: "prompt", NodeID: "6", InputName: "text"}}, Outputs: []imaging.WorkflowOutput{{NodeID: "9", OutputName: "image"}}}}}
	o.Target.Parameters = []imaging.ParameterSpec{{Key: "prompt", Type: "string"}}
	if webhook {
		o.WebhookURL = "https://example.invalid/private-webhook"
	}
	return o
}
func workflowRequest(o WorkflowOptions) imaging.ExecuteRequest {
	return imaging.ExecuteRequest{Target: o.Target, Request: imaging.CreateJobRequest{Operation: imaging.OperationGenerate, Prompt: "fixture bird", Count: 1}}
}
func serveGraph(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"prompt": graphFixture}})
}
func TestContractRunningHubUploadSubmitOutputs(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-api" {
			t.Error("wrong method/auth")
		}
		if r.URL.Path == "/openapi/v2/media/upload/binary" {
			if e := r.ParseMultipartForm(maxUpload); e != nil {
				t.Error(e)
				return
			}
			f, _, e := r.FormFile("file")
			if e != nil {
				t.Error(e)
				return
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if len(b) == 0 || len(r.MultipartForm.Value) != 0 {
				t.Error("upload must contain only file")
			}
			_, _ = w.Write(fixture(t, "runninghub-upload.json"))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.HasPrefix(r.URL.Path, "/task/") || strings.HasPrefix(r.URL.Path, "/api/") {
			if body["apiKey"] != "fixture-api" {
				t.Error("legacy body key missing")
			}
		} else if _, ok := body["apiKey"]; ok {
			t.Error("v2 body key leaked")
		}
		switch r.URL.Path {
		case "/api/openapi/getJsonApiFormat":
			serveGraph(w)
		case "/task/openapi/create":
			if body["workflowId"] != "fixture-workflow" || body["webhookUrl"] != "https://example.invalid/private-webhook" {
				t.Error("incorrect create")
			}
			nodes := body["nodeInfoList"].([]any)
			if len(nodes) != 2 || nodes[0].(map[string]any)["fieldValue"] != "fixture bird" || nodes[1].(map[string]any)["fieldValue"] != "uploads/fixture.png" {
				t.Error("incorrect mapping/upload reference")
			}
			_, _ = w.Write(fixture(t, "runninghub-create.json"))
		case "/openapi/v2/query":
			if body["taskId"] != "9007199254740993123" {
				t.Error("task ID precision lost")
			}
			_, _ = w.Write(fixture(t, "runninghub-query.json"))
		case "/task/openapi/getWebhookDetail":
			_, _ = w.Write(fixture(t, "runninghub-detail.json"))
		case "/task/openapi/cancel":
			_, _ = io.WriteString(w, `{"code":0,"data":null}`)
		default:
			t.Error("undocumented endpoint " + r.URL.Path)
		}
	}))
	defer s.Close()
	o := workflowOptions(s, true)
	o.Target.WorkflowMapping.Inputs = append(o.Target.WorkflowMapping.Inputs, imaging.WorkflowInput{ParameterKey: "input.reference", NodeID: "14", InputName: "image"})
	o.Target.Parameters = append(o.Target.Parameters, imaging.ParameterSpec{Key: "input.reference", Type: "string"})
	a, e := NewRunningHub(o)
	if e != nil {
		t.Fatal(e)
	}
	r := workflowRequest(o)
	r.Request.Operation = imaging.OperationReference
	r.Request.Inputs = []imaging.InputAsset{{AssetID: "asset", Role: "reference"}}
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	closed := false
	r.Inputs = []imaging.ResolvedAsset{{AssetID: "asset", MIME: "image/png", Bytes: int64(imageData.Len()), Open: func() (io.ReadCloser, error) {
		return &trackedReadCloser{Reader: bytes.NewReader(imageData.Bytes()), closed: &closed}, nil
	}}}
	got, e := a.Submit(context.Background(), r)
	if e != nil || got.State != "pending" || !closed {
		t.Fatalf("submit: %+v %v closed=%v", got, e, closed)
	}
	fresh, e := NewRunningHub(o)
	if e != nil {
		t.Fatal(e)
	}
	got, e = fresh.Poll(context.Background(), got.Handle)
	if e != nil || got.State != "completed" || len(got.Outputs) != 1 || got.Outputs[0].MIME != "image/png" || got.Usage != nil {
		t.Fatalf("outputs: %+v %v", got, e)
	}
	e = fresh.Cancel(context.Background(), got.Handle)
	assertError(t, e, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	if _, e = fresh.Reconcile(context.Background(), got.Handle); e != imaging.ErrUnsupported {
		t.Fatal("invented reconciliation")
	}
	want := "/api/openapi/getJsonApiFormat,/openapi/v2/media/upload/binary,/task/openapi/create,/openapi/v2/query,/task/openapi/getWebhookDetail,/task/openapi/cancel"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths: %v", paths)
	}
}

type trackedReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackedReadCloser) Close() error { *r.closed = true; return nil }
func TestContractRunningHubRevisionAndValidation(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"prompt": strings.Replace(graphFixture, `"text":""`, `"text":"changed"`, 1)}})
	}))
	defer s.Close()
	o := workflowOptions(s, false)
	a, e := NewRunningHub(o)
	if e != nil {
		t.Fatal(e)
	}
	r := workflowRequest(o)
	_, e = a.Submit(context.Background(), r)
	assertError(t, e, imaging.CodeRevisionConflict, imaging.DeliveryNotSent)
	if calls != 1 {
		t.Fatal("submitted changed workflow")
	}
	r.Request.Parameters = map[string]any{"undeclared": "x"}
	_, e = a.Submit(context.Background(), r)
	assertError(t, e, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	if calls != 1 {
		t.Fatal("invalid input sent")
	}
	r = workflowRequest(o)
	r.Target.WorkflowMapping.Revision = 2
	_, e = a.Submit(context.Background(), r)
	assertError(t, e, imaging.CodeRevisionConflict, imaging.DeliveryNotSent)
	bad := workflowOptions(s, false)
	bad.Target.WorkflowMapping.Inputs[0].NodeID = "missing"
	if _, e = NewRunningHub(bad); e == nil {
		t.Fatal("missing node accepted")
	}
	bad = workflowOptions(s, false)
	bad.Target.WorkflowMapping.Inputs[0] = imaging.WorkflowInput{ParameterKey: "prompt", NodeID: "9", InputName: "images"}
	if _, e = NewRunningHub(bad); e == nil {
		t.Fatal("connection array binding accepted")
	}
	bad = workflowOptions(s, false)
	bad.Target.WorkflowMapping.Outputs[0].OutputName = "images"
	if _, e = NewRunningHub(bad); e == nil {
		t.Fatal("invented output socket accepted")
	}
}
func TestContractRunningHubQueryInterruptedResume(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi/v2/query" {
			t.Error("query recovery resubmitted")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"taskId":"kept","status":"RUNNING","errorMessage":"secret"}`)
	}))
	defer s.Close()
	o := workflowOptions(s, false)
	a, _ := NewRunningHub(o)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, e := a.Poll(ctx, "kept")
	if e == nil || got.State != "unknown" || got.Handle != "kept" {
		t.Fatalf("interruption: %+v %v", got, e)
	}
	a, _ = NewRunningHub(o)
	got, e = a.Poll(context.Background(), "kept")
	if e != nil || got.State != "pending" || calls.Load() != 2 {
		t.Fatalf("resume: %+v %v", got, e)
	}
}
func TestContractRunningHubV2CannotGuessOutputNode(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fixture(t, "runninghub-query.json")) }))
	defer s.Close()
	a, _ := NewRunningHub(workflowOptions(s, false))
	got, e := a.Poll(context.Background(), "9007199254740993123")
	if e != nil || got.State != "unknown" || len(got.Outputs) != 0 {
		t.Fatalf("invented output mapping: %+v %v", got, e)
	}
}
func TestContractSensitiveErrorsAndUnknownSubmit(t *testing.T) {
	for _, tc := range []struct {
		body, code string
		delivery   imaging.Delivery
	}{{`{"code":802,"msg":"fixture-api https://secret.invalid"}`, imaging.CodeAuthRequired, imaging.DeliveryRejected}, {`{"code":421,"msg":"secret"}`, imaging.CodeRateLimited, imaging.DeliveryRejected}, {`{"code":0,"data":{}}`, imaging.CodeResultUnknown, imaging.DeliveryUncertain}, {`{"code":999999,"msg":"secret"}`, imaging.CodeUnavailable, imaging.DeliveryUncertain}} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/openapi/getJsonApiFormat" {
				serveGraph(w)
				return
			}
			calls++
			_, _ = io.WriteString(w, tc.body)
		}))
		o := workflowOptions(s, false)
		a, _ := NewRunningHub(o)
		_, e := a.Submit(context.Background(), workflowRequest(o))
		assertError(t, e, tc.code, tc.delivery)
		if calls != 1 {
			t.Fatal("submit replayed")
		}
		s.Close()
	}
}
