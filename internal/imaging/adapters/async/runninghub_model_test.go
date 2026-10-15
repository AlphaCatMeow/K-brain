package asyncadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestRunningHubModelSubmitPollContract(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-api" {
			t.Error("native auth/method lost")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("body invalid")
		}
		switch r.URL.Path {
		case "/openapi/v2/rhart-image-n-g31-flash-lite-official/text-to-image":
			if body["prompt"] != "ceramic bird" || body["aspectRatio"] != "1:1" || len(body) != 2 {
				t.Fatal("native input mismatch")
			}
			_, _ = w.Write([]byte(`{"taskId":"task-fixture","status":"RUNNING","errorCode":"","results":null}`))
		case "/openapi/v2/query":
			if body["taskId"] != "task-fixture" || len(body) != 1 {
				t.Fatal("query mismatch")
			}
			_, _ = w.Write([]byte(`{"taskId":"task-fixture","status":"SUCCESS","errorCode":"","results":[{"url":"https://example.com/image.png","outputType":"png"}]}`))
		default:
			t.Fatal("unexpected route")
		}
	}))
	defer srv.Close()
	a, err := NewRunningHubModel(options(srv))
	if err != nil {
		t.Fatal(err)
	}
	request := imaging.ExecuteRequest{Target: imaging.TargetSnapshot{Adapter: RunningHubModelID, AdapterVersion: Version, Kind: "model", RemoteID: RunningHubLiteModel}, Request: imaging.CreateJobRequest{Operation: imaging.OperationGenerate, Prompt: "ceramic bird", Count: 1}}
	out, err := a.Submit(context.Background(), request)
	if err != nil || out.State != "pending" || out.Handle != "task-fixture" {
		t.Fatalf("submit %v %+v", err, out)
	}
	out, err = a.Poll(context.Background(), out.Handle)
	if err != nil || out.State != "completed" || len(out.Outputs) != 1 || out.Outputs[0].MIME != "image/png" {
		t.Fatalf("poll %v %+v", err, out)
	}
	if out.Usage != nil {
		t.Fatal("unknown monetary usage invented")
	}
	request.Request.Count = 2
	if _, err = a.Submit(context.Background(), request); err == nil {
		t.Fatal("unverified batch accepted")
	}
	if calls != 2 {
		t.Fatal("invalid input was submitted")
	}
	if _, err = a.Reconcile(context.Background(), "key"); err != imaging.ErrUnsupported {
		t.Fatal("invented provider idempotency")
	}
}

func TestRunningHubModelPollRejectsUnsafeOrUncorrelatedOutputs(t *testing.T) {
	for _, body := range []string{
		`{"taskId":"other","status":"SUCCESS","results":[{"url":"https://example.com/image.png","outputType":"png"}]}`,
		`{"taskId":"task-fixture","status":"SUCCESS","results":[{"url":"http://example.com/image.png","outputType":"png"}]}`,
		`{"taskId":"task-fixture","status":"SUCCESS","results":[{"url":"https://example.com/video.mp4","outputType":"mp4"}]}`,
		`{"taskId":"task-fixture","status":"SUCCESS","results":[]}`,
		`{"taskId":"task-fixture","status":"NEW_STATE","results":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			a, err := NewRunningHubModel(options(srv))
			if err != nil {
				t.Fatal(err)
			}
			out, _ := a.Poll(context.Background(), "task-fixture")
			if out.State == "completed" {
				t.Fatal("invalid result reported success")
			}
		})
	}
}

func TestRunningHubModelExplicitRejectionWithoutTaskID(t *testing.T) {
	for _, body := range []string{`{"errorCode":"1014","errorMessage":"private response"}`, `{"errorCode":1014,"errorMessage":"private response"}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			a, err := NewRunningHubModel(options(srv))
			if err != nil {
				t.Fatal(err)
			}
			r := imaging.ExecuteRequest{Target: imaging.TargetSnapshot{Adapter: RunningHubModelID, AdapterVersion: Version, Kind: "model", RemoteID: RunningHubLiteModel}, Request: imaging.CreateJobRequest{Operation: imaging.OperationGenerate, Prompt: "fixture", Count: 1}}
			result, err := a.Submit(context.Background(), r)
			if result.State != "failed" || result.Handle != "" {
				t.Fatalf("explicit rejection lost: %+v %v", result, err)
			}
			assertError(t, err, imaging.CodeForbidden, imaging.DeliveryRejected)
		})
	}
}
