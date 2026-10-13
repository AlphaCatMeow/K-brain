package asyncadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestContractRunningHubSubmitTimeout(t *testing.T) {
	var creates atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/api/openapi/getJsonApiFormat" {
			serveGraph(w)
			return
		}
		if r.URL.Path != "/task/openapi/create" {
			t.Error("unexpected request")
		}
		creates.Add(1)
		<-r.Context().Done()
	}))
	defer s.Close()
	o := workflowOptions(s, false)
	a, _ := NewRunningHub(o)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got, e := a.Submit(ctx, workflowRequest(o))
	assertError(t, e, imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	if got.State != "unknown" || creates.Load() != 1 {
		t.Fatalf("uncertain submit replayed: %+v", got)
	}
}
func TestContractRunningHubRequiredParametersAndTypes(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; serveGraph(w) }))
	defer s.Close()
	o := workflowOptions(s, false)
	o.Target.Parameters = []imaging.ParameterSpec{{Key: "text", Type: "string", Required: true}}
	o.Target.WorkflowMapping.Inputs[0].ParameterKey = "text"
	a, e := NewRunningHub(o)
	if e != nil {
		t.Fatal(e)
	}
	for _, params := range []map[string]any{nil, {"text": 12}, {"text": "ok", "undeclared": "x"}} {
		r := workflowRequest(o)
		r.Request.Prompt = ""
		r.Request.Parameters = params
		_, e = a.Submit(context.Background(), r)
		assertError(t, e, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if calls != 0 {
		t.Fatal("invalid mapping sent upstream")
	}
}
func TestContractRunningHubStatesAndEventIdentity(t *testing.T) {
	cases := []struct{ name, status, detail, want string }{
		{"queued", "QUEUED", "", "pending"},
		{"failed", "FAILED", "", "failed"},
		{"future-state", "NEW_STATUS", "", "unknown"},
		{"mismatched-task", "SUCCESS", `{"code":0,"data":{"taskId":"other","eventData":"{\"code\":0,\"data\":[]}"}}`, "unknown"},
		{"empty", "SUCCESS", `{"code":0,"data":{"taskId":"kept","eventData":"{\"code\":0,\"data\":[]}"}}`, "unknown"},
		{"malformed-event", "SUCCESS", `{"code":0,"data":{"taskId":"kept","eventData":"not json secret"}}`, "unknown"},
		{"no-event", "SUCCESS", `{"code":0,"data":null}`, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/openapi/v2/query" {
					_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "kept", "status": tc.status, "errorMessage": "secret https://private.invalid"})
					return
				}
				_, _ = io.WriteString(w, tc.detail)
			}))
			defer s.Close()
			a, _ := NewRunningHub(workflowOptions(s, true))
			got, e := a.Poll(context.Background(), "kept")
			if e != nil || got.State != tc.want || got.Handle != "kept" || len(got.Outputs) != 0 {
				t.Fatalf("incorrect state: %+v %v", got, e)
			}
		})
	}
}
func TestContractHTTPRejectionRedirectAndMalformed(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500, 302, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var redirected atomic.Int32
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer sink.Close()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", sink.URL)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "secret fixture-sk https://private.invalid")
			}))
			defer s.Close()
			a, _ := NewJimeng(options(s))
			got, e := a.Submit(context.Background(), jimengRequest())
			if e == nil || strings.Contains(e.Error(), "secret") || redirected.Load() != 0 {
				t.Fatal("unsafe upstream handling")
			}
			if status >= 500 || status == 302 || status == 200 {
				if got.State != "unknown" {
					t.Fatal("uncertain response was rejected")
				}
			} else if got.State != "failed" {
				t.Fatal("explicit rejection was not rejected")
			}
		})
	}
}
func TestContractJimengOutputRejectionAndBinary(t *testing.T) {
	for _, tc := range []struct{ body, state string }{
		{`{"code":50511,"data":{"status":"done"},"message":"secret"}`, "failed"},
		{`{"code":10000,"data":{"status":"done","binary_data_base64":["invalid!"]}}`, "unknown"},
		{`{"code":10000,"data":{"status":"done","binary_data_base64":["` + base64.StdEncoding.EncodeToString([]byte("not image")) + `"]}}`, "unknown"},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
		a, _ := NewJimeng(options(s))
		got, e := a.Poll(context.Background(), "kept")
		if e != nil || got.State != tc.state {
			t.Fatalf("output state: %+v %v", got, e)
		}
		s.Close()
	}
}
func TestContractFactoryPrivateOptionsAndDisabledEvidence(t *testing.T) {
	for _, v := range Availability() {
		if v.Enabled || v.LiveVerified || !strings.Contains(v.DisabledReason, "not enabled") {
			t.Fatal("unverified production enabled")
		}
	}
	if _, e := json.Marshal(WorkflowOptions{WebhookURL: "https://example.invalid/private"}); e == nil {
		t.Fatal("backend workflow config escaped")
	}
	if _, e := Factory(JimengID, "unsupported", Options{}, nil); e != imaging.ErrUnsupported {
		t.Fatal("unknown version accepted")
	}
	if _, e := Factory(RunningHubID, Version, Options{}, nil); e == nil {
		t.Fatal("missing mapping accepted")
	}
}
