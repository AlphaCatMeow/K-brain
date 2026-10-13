package direct

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestContractNativeParameters(t *testing.T) {
	for _, tc := range []struct {
		id     string
		params map[string]any
		want   map[string]any
	}{
		{OpenAIID, map[string]any{"size": "1536x1024", "quality": "high", "output_format": "webp", "output_compression": 80, "background": "transparent"}, map[string]any{"model": "gpt-image-2", "prompt": "A synthetic ceramic bird", "n": float64(1), "size": "1536x1024", "quality": "high", "output_format": "webp", "output_compression": float64(80), "background": "transparent"}},
		{VolcengineID, map[string]any{"size": "2048x1024", "response_format": "b64_json", "output_format": "png", "watermark": false, "optimize_prompt_options.mode": "fast"}, map[string]any{"model": "doubao-seedream-5-0-pro-260628", "prompt": "A synthetic ceramic bird", "size": "2048x1024", "response_format": "b64_json", "output_format": "png", "watermark": false, "optimize_prompt_options": map[string]any{"mode": "fast"}}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			raw := syntheticPNG(t, 16, 20, false)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if !reflect.DeepEqual(b, tc.want) {
					t.Errorf("native fields %#v", b)
				}
				io.WriteString(w, responseJSON(tc.id, raw))
			}))
			defer srv.Close()
			a := adapter(t, tc.id, options(srv.Client(), srv.URL))
			r := request(tc.id)
			r.Request.Parameters = tc.params
			if _, err := a.Submit(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		id     string
		params map[string]any
	}{
		{OpenAIID, map[string]any{"quality": "xhigh"}}, {OpenAIID, map[string]any{"output_format": "png", "output_compression": 10}},
		{OpenAIID, map[string]any{"output_format": "jpeg", "background": "transparent"}}, {OpenAIID, map[string]any{"output_format": "jpeg", "output_compression": 100.1}},
		{OpenAIID, map[string]any{"n": 2}}, {OpenAIID, map[string]any{"response_format": "url"}},
		{GoogleID, map[string]any{"candidateCount": 1}}, {GoogleID, map[string]any{"responseMimeType": "image/png"}},
		{VolcengineID, map[string]any{"size": "512x512"}}, {VolcengineID, map[string]any{"size": "4K"}},
		{VolcengineID, map[string]any{"size": "99999999999999999999x999999999999999"}}, {VolcengineID, map[string]any{"size": "2048X1024"}},
		{VolcengineID, map[string]any{"size": "4096x4096"}}, {VolcengineID, map[string]any{"size": "16384x64"}},
		{VolcengineID, map[string]any{"output_format": "webp"}}, {VolcengineID, map[string]any{"watermark": "false"}},
		{VolcengineID, map[string]any{"sequential_image_generation": "disabled"}}, {VolcengineID, map[string]any{"n": 1}},
	} {
		t.Run(tc.id+"/reject", func(t *testing.T) {
			calls := 0
			a := adapter(t, tc.id, options(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}, "https://example.invalid"))
			r := request(tc.id)
			r.Request.Parameters = tc.params
			res, err := a.Submit(context.Background(), r)
			assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
			if calls != 0 {
				t.Fatal("sent invalid parameters")
			}
		})
	}
}

func TestContractEditAndInputBounds(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID, VolcengineID} {
		t.Run(id, func(t *testing.T) {
			raw := syntheticPNG(t, 16, 20, false)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, responseJSON(id, raw)) }))
			defer srv.Close()
			a := adapter(t, id, options(srv.Client(), srv.URL))
			r := request(id)
			r.Request.Operation = imaging.OperationEdit
			r.Request.ParentAssetID = "source"
			addInput(&r, "source", "source", raw, nil)
			if res, err := a.Submit(context.Background(), r); err != nil || res.State != "completed" {
				t.Fatalf("%+v %v", res, err)
			}
			baseCalls := calls
			var closed atomic.Int32
			r = request(id)
			r.Request.Operation = imaging.OperationReference
			addInput(&r, "a", "reference", raw, &closed)
			r.Inputs[0].Bytes++
			res, err := a.Submit(context.Background(), r)
			assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
			if closed.Load() != 1 {
				t.Fatal("failed decode did not close reader")
			}
			r = request(id)
			r.Request.Operation = imaging.OperationReference
			for i := 0; i < a.Profile().Capabilities.MaxInputs+1; i++ {
				addInput(&r, string(rune('a'+i)), "reference", raw, nil)
			}
			res, err = a.Submit(context.Background(), r)
			assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
			if calls != baseCalls {
				t.Fatal("sent oversized input group")
			}
			if id != OpenAIID {
				r = request(id)
				r.Request.Operation = imaging.OperationMask
				res, err = a.Submit(context.Background(), r)
				assertError(t, res, err, imaging.CodeUnsupportedOperation, imaging.DeliveryNotSent)
			}
		})
	}
	t.Run("Ark minimum dimensions", func(t *testing.T) {
		a := adapter(t, VolcengineID, options(&http.Client{}, "https://example.invalid"))
		r := request(VolcengineID)
		r.Request.Operation = imaging.OperationReference
		addInput(&r, "a", "reference", syntheticPNG(t, 14, 20, false), nil)
		res, err := a.Submit(context.Background(), r)
		assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	})
	t.Run("Google total encoded body", func(t *testing.T) {
		calls := 0
		o := options(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}, "https://example.invalid")
		o.Limits.MaxRequestBytes = 500
		a := adapter(t, GoogleID, o)
		r := request(GoogleID)
		r.Request.Operation = imaging.OperationReference
		r.Request.Prompt = strings.Repeat("x", 400)
		addInput(&r, "a", "reference", syntheticPNG(t, 16, 20, false), nil)
		res, err := a.Submit(context.Background(), r)
		assertError(t, res, err, imaging.CodeTooLarge, imaging.DeliveryNotSent)
		if calls != 0 {
			t.Fatal("oversized Google JSON sent")
		}
	})
}

func TestContractConstructorAndSecrets(t *testing.T) {
	for _, base := range []string{"", "http://example.invalid", "https://user:SECRET_SENTINEL@example.invalid", "https://example.invalid/?secret=SECRET_SENTINEL", "https://example.invalid/#secret", "file:///path"} {
		if _, err := NewOpenAI(options(&http.Client{}, base)); err == nil {
			t.Fatalf("accepted base %s", base)
		}
	}
	o := options(&http.Client{}, "https://example.invalid")
	o.Limits.MaxResponseBytes = 65 << 20
	if _, err := NewOpenAI(o); err == nil {
		t.Fatal("unbounded response")
	}
	for _, secret := range []string{"", "key\r\nInjected: SECRET_SENTINEL"} {
		calls := 0
		o = options(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}, "https://example.invalid")
		o.SecretResolver = secretFunc(func(context.Context, imaging.Scope, string) (string, error) { return secret, nil })
		a := adapter(t, GoogleID, o)
		res, err := a.Submit(context.Background(), request(GoogleID))
		assertError(t, res, err, imaging.CodeAuthRequired, imaging.DeliveryNotSent)
		if calls != 0 {
			t.Fatal("sent invalid auth")
		}
	}
	o = options(&http.Client{}, "https://example.invalid")
	o.SecretResolver = secretFunc(func(context.Context, imaging.Scope, string) (string, error) { return "", errors.New("SECRET_SENTINEL") })
	a := adapter(t, OpenAIID, o)
	res, err := a.Submit(context.Background(), request(OpenAIID))
	assertError(t, res, err, imaging.CodeAuthRequired, imaging.DeliveryNotSent)
}
