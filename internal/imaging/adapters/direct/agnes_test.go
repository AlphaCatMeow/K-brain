package direct

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func agnesRequestFor(op imaging.Operation) imaging.ExecuteRequest {
	r := request(OpenAIID)
	r.Target.Source = "provider"
	r.Target.RemoteID = "agnes-image-2.5-flash"
	r.Request.Operation = op
	return r
}

func TestContractAgnesGenerateAndEditUseJSONGenerations(t *testing.T) {
	raw := syntheticPNG(t, 16, 30, false)
	source := syntheticPNG(t, 8, 60, false)
	var calls atomic.Int32
	var bodies []map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://apihub.agnes-ai.com/v1/images/generations" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("wrong Agnes route %s %s", r.URL, r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("Agnes body is not JSON")
		}
		bodies = append(bodies, body)
		// Agnes returns b64_json:null alongside url, or the reverse.
		out := `{"created":1,"data":[{"url":null,"b64_json":"` + base64.StdEncoding.EncodeToString(raw) + `","revised_prompt":null}]}`
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(out))}, nil
	})}
	o := options(client, "https://apihub.agnes-ai.com/v1")
	o.Profile = "agnes"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	gen := agnesRequestFor(imaging.OperationGenerate)
	gen.Request.Parameters = map[string]any{"imageSize": "2K", "aspectRatio": "16:9"}
	if res, err := a.Submit(context.Background(), gen); err != nil || len(res.Outputs) != 1 {
		t.Fatalf("Agnes generate failed: %v", err)
	}
	edit := agnesRequestFor(imaging.OperationEdit)
	addInput(&edit, "source", "source", source, nil)
	if res, err := a.Submit(context.Background(), edit); err != nil || len(res.Outputs) != 1 {
		t.Fatalf("Agnes edit failed: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("calls %d", len(bodies))
	}
	g, e := bodies[0], bodies[1]
	if g["model"] != "agnes-image-2.5-flash" || g["size"] != "2K" || g["ratio"] != "16:9" || g["return_base64"] != true || g["extra_body"] != nil || g["n"] != nil || g["response_format"] != nil {
		t.Fatalf("generate body %#v", g)
	}
	extra, _ := e["extra_body"].(map[string]any)
	images, _ := extra["image"].([]any)
	if e["size"] != "1K" || e["return_base64"] != nil || e["response_format"] != nil || e["image"] != nil || extra["response_format"] != "b64_json" || len(images) != 1 || images[0] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(source) {
		t.Fatalf("edit body %#v", e)
	}

	for _, params := range []map[string]any{{"imageSize": "1024x1024"}, {"aspectRatio": "5:4"}, {"size": "2K"}, {"quality": "high"}} {
		bad := agnesRequestFor(imaging.OperationGenerate)
		bad.Request.Parameters = params
		if _, err := a.Submit(context.Background(), bad); err == nil {
			t.Fatalf("Agnes accepted %v", params)
		}
	}
	mask := agnesRequestFor(imaging.OperationMask)
	addInput(&mask, "source", "source", source, nil)
	addInput(&mask, "mask", "mask", syntheticPNG(t, 8, 30, true), nil)
	if _, err := a.Submit(context.Background(), mask); err == nil {
		t.Fatal("Agnes mask accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("rejected input reached upstream")
	}
}

func TestContractAgnesURLResultAndProfileBinding(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.agnes-ai.cn" {
			t.Errorf("wrong host %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://storage.googleapis.com/agnes-aigc/x.png","b64_json":null}]}`))}, nil
	})}
	o := options(client, "https://api.agnes-ai.cn/v1")
	o.Profile = "agnes"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Submit(context.Background(), agnesRequestFor(imaging.OperationGenerate))
	if err != nil || len(res.Outputs) != 1 || res.Outputs[0].Kind != "url" {
		t.Fatalf("Agnes URL rejected: %v", err)
	}
	for _, base := range []string{"https://api.openai.com/v1", "http://apihub.agnes-ai.com/v1", "https://rolldek.com/v1"} {
		o.BaseURL = base
		if _, err := NewOpenAI(o); err == nil {
			t.Fatalf("Agnes profile accepted on %s", base)
		}
	}
	if ProfileForBaseURL("https://apihub.agnes-ai.com/v1") != "agnes" || ProfileForBaseURL("https://api.agnes-ai.cn/v1") != "agnes" || ProfileForBaseURL("https://proxy.example/v1") != "" {
		t.Fatal("profile detection")
	}
}
