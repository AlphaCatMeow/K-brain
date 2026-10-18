package direct

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestGoogleImageConfigWireAndConditionalSchema(t *testing.T) {
	for _, tc := range []struct {
		model, size string
		sizes       []any
	}{
		{"gemini-2.5-flash-image", "", nil},
		{"gemini-3-pro-image-preview", "4K", []any{"1K", "2K", "4K"}},
		{"gemini-nano-banana-2.1", "2K", []any{"1K", "2K", "4K"}},
		{"gemini-3.1-flash-image-preview", "512", []any{"512", "1K", "2K", "4K"}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			raw := syntheticPNG(t, 8, 40, false)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1beta/models/"+tc.model+":generateContent" || r.Header.Get("x-goog-api-key") != "synthetic-key" || r.Header.Get("Authorization") != "" {
					t.Error("wrong Google route or authentication")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				config, _ := body["generationConfig"].(map[string]any)
				want := map[string]any{"aspectRatio": "16:9"}
				if tc.size != "" {
					want["imageSize"] = tc.size
				}
				if !reflect.DeepEqual(config["imageConfig"], want) || !reflect.DeepEqual(config["responseModalities"], []any{"TEXT", "IMAGE"}) {
					t.Errorf("wrong Google imageConfig wire: %#v", config)
				}
				contents, _ := body["contents"].([]any)
				content, _ := contents[0].(map[string]any)
				parts, _ := content["parts"].([]any)
				if len(parts) != 2 {
					t.Error("Google semantic edit lost its source image")
				}
				io.WriteString(w, responseJSON(GoogleID, raw))
			}))
			defer server.Close()
			a := adapter(t, GoogleID, options(server.Client(), server.URL+"/v1beta"))
			r := request(GoogleID)
			r.Target.RemoteID = tc.model
			r.Request.Operation = imaging.OperationEdit
			r.Request.Parameters = map[string]any{"aspectRatio": "16:9"}
			if tc.size != "" {
				r.Request.Parameters["imageSize"] = tc.size
			}
			addInput(&r, "source", "source", raw, nil)
			result, err := a.Submit(context.Background(), r)
			if err != nil || result.State != "completed" || calls.Load() != 1 {
				t.Fatalf("Google edit failed: %v", err)
			}
			schema := ModelParameters(GoogleID, tc.model)
			if len(schema) == 0 || schema[0].Key != "aspectRatio" {
				t.Fatal("aspectRatio not exposed to generic parameter form")
			}
			var sizes []any
			for _, p := range schema {
				if p.Key == "imageSize" {
					sizes = p.Enum
				}
			}
			if !reflect.DeepEqual(sizes, tc.sizes) {
				t.Fatalf("wrong conditional imageSize schema: %#v", sizes)
			}
		})
	}
}

func TestGoogleUnsupportedImageConfigRejectedBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, nil })}
	a := adapter(t, GoogleID, options(client, "https://image.example/v1beta"))
	for _, tc := range []struct {
		model      string
		parameters map[string]any
	}{
		{"gemini-2.5-flash-image", map[string]any{"imageSize": "2K"}},
		{"gemini-nano-banana-2.1", map[string]any{"imageSize": "512"}},
		{"gemini-3-pro-image-preview", map[string]any{"imageSize": "2k"}},
		{"gemini-3.1-flash-image-preview", map[string]any{"aspectRatio": "7:5"}},
		{"gemini-3.1-flash-image-preview", map[string]any{"imageSize": 2048}},
	} {
		r := request(GoogleID)
		r.Target.RemoteID = tc.model
		r.Request.Parameters = tc.parameters
		result, err := a.Submit(context.Background(), r)
		assertError(t, result, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported Google configuration sent HTTP")
	}
}
