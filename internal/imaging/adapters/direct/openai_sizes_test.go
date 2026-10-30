package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func parameterKeys(ps []imaging.ParameterSpec) []string {
	keys := make([]string, 0, len(ps))
	for _, p := range ps {
		keys = append(keys, p.Key)
	}
	return keys
}

func parameterEnum(ps []imaging.ParameterSpec, key string) []any {
	for _, p := range ps {
		if p.Key == key {
			return p.Enum
		}
	}
	return nil
}

func TestOpenAIFlexibleModelsExposeRatioAndTier(t *testing.T) {
	base := []any{"low", "medium", "high", "auto"}
	for _, tc := range []struct {
		model   string
		quality []any
	}{
		{"gpt-image-2", base},
		{"gpt-image-2-2026-04-21", base},
		{"gpt-image-2.5-flare", append(append([]any{}, base...), "xhigh", "max")},
		{"gpt-image-2.5-flare-2026-09-08", append(append([]any{}, base...), "xhigh", "max")},
		{"gpt-image-2.5-sunburst", append(append([]any{}, base...), "xhigh", "max")},
	} {
		t.Run(tc.model, func(t *testing.T) {
			schema := ModelParameters(OpenAIID, tc.model)
			if got, want := parameterKeys(schema), []string{"aspectRatio", "imageSize", "quality", "output_format", "background", "output_compression"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("keys = %v, want %v", got, want)
			}
			if got := parameterEnum(schema, "aspectRatio"); !reflect.DeepEqual(got, []any{"1:1", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16", "21:9"}) {
				t.Fatalf("aspect ratios = %v", got)
			}
			if got := parameterEnum(schema, "imageSize"); !reflect.DeepEqual(got, []any{"1K", "2K", "4K"}) {
				t.Fatalf("image sizes = %v", got)
			}
			if got := parameterEnum(schema, "quality"); !reflect.DeepEqual(got, tc.quality) {
				t.Fatalf("quality = %v, want %v", got, tc.quality)
			}
			// Labels match the names LiveAgent already localizes for Google targets.
			if schema[0].Label != "Aspect ratio" || schema[1].Label != "Image size" {
				t.Fatalf("labels = %q, %q", schema[0].Label, schema[1].Label)
			}
		})
	}
	// Fixed-size models and unrecognized aliases keep the three-size contract.
	for _, model := range []string{"gpt-image-1", "gpt-image-1-mini", "gpt-image-1.5", "moonup-image", "gpt-image-2-all", "gpt-image-2.5-flare-preview"} {
		if got, want := ModelParameters(OpenAIID, model), ProtocolProfile(OpenAIID).Parameters; !reflect.DeepEqual(got, want) {
			t.Errorf("%s schema changed: %v", model, parameterKeys(got))
		}
	}
}

func TestOpenAIRecognizesGPTImage25Names(t *testing.T) {
	for _, id := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst-2026-09-08"} {
		if ImageModelAdapter(id) != OpenAIID {
			t.Errorf("%s not recognized as an OpenAI image model", id)
		}
	}
}

// openAIRule checks OpenAI's documented size constraints for GPT Image 2 / 2.5.
func openAIRule(t *testing.T, size string) (int, int) {
	t.Helper()
	w, h, ok := strings.Cut(size, "x")
	if !ok {
		t.Fatalf("size %q is not WxH", size)
	}
	width, _ := strconv.Atoi(w)
	height, _ := strconv.Atoi(h)
	long, short := max(width, height), min(width, height)
	if width%16 != 0 || height%16 != 0 || long > 3840 || long > 3*short || width*height < 655360 || width*height > 8294400 {
		t.Fatalf("size %s breaks OpenAI size rules", size)
	}
	return width, height
}

func TestOpenAIRatioAndTierMapToSize(t *testing.T) {
	sizes := map[string][3]string{
		"1:1":  {"1024x1024", "1920x1920", "2880x2880"},
		"3:2":  {"1248x832", "2304x1536", "3504x2336"},
		"2:3":  {"832x1248", "1536x2304", "2336x3504"},
		"4:3":  {"1152x864", "2176x1632", "3264x2448"},
		"3:4":  {"864x1152", "1632x2176", "2448x3264"},
		"16:9": {"1280x720", "2560x1440", "3840x2160"},
		"9:16": {"720x1280", "1440x2560", "2160x3840"},
		"21:9": {"1456x624", "2912x1248", "3808x1632"},
	}
	type wire struct {
		model  string
		params map[string]any
		size   string
	}
	var cases []wire
	for ratio, row := range sizes {
		a, b, _ := strings.Cut(ratio, ":")
		ra, _ := strconv.Atoi(a)
		rb, _ := strconv.Atoi(b)
		for i, tier := range []string{"1K", "2K", "4K"} {
			w, h := openAIRule(t, row[i])
			if w*rb != h*ra {
				t.Fatalf("%s %s is not exactly %s", tier, row[i], ratio)
			}
			cases = append(cases, wire{"gpt-image-2.5-flare", map[string]any{"aspectRatio": ratio, "imageSize": tier}, row[i]})
		}
	}
	cases = append(cases,
		// A ratio alone uses the 1K budget; a tier alone is square.
		wire{"gpt-image-2", map[string]any{"aspectRatio": "16:9"}, "1280x720"},
		wire{"gpt-image-2-2026-04-21", map[string]any{"imageSize": "2K"}, "1920x1920"},
		// Configured targets may persist their own size schema.
		wire{"gpt-image-2", map[string]any{"size": "1536x1024"}, "1536x1024"},
		wire{"gpt-image-2.5-sunburst", map[string]any{"size": "2560x1440"}, "2560x1440"},
		wire{"gpt-image-1.5", map[string]any{"size": "1024x1536"}, "1024x1536"},
		// No size parameter leaves the provider default.
		wire{"gpt-image-2.5-flare", map[string]any{"quality": "max"}, ""},
	)
	raw := syntheticPNG(t, 16, 20, false)
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%v", tc.model, tc.params), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				size, _ := body["size"].(string)
				if size != tc.size {
					t.Errorf("wire size = %q, want %q", size, tc.size)
				}
				for _, key := range []string{"aspectRatio", "imageSize"} {
					if _, leaked := body[key]; leaked {
						t.Errorf("%s leaked to the OpenAI body", key)
					}
				}
				io.WriteString(w, responseJSON(OpenAIID, raw))
			}))
			defer srv.Close()
			a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
			r := request(OpenAIID)
			r.Target.RemoteID = tc.model
			r.Request.Parameters = tc.params
			if _, err := a.Submit(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAISizeParametersRejectedBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		model  string
		params map[string]any
	}{
		{"gpt-image-1", map[string]any{"aspectRatio": "16:9"}},
		{"gpt-image-1.5", map[string]any{"imageSize": "2K"}},
		{"moonup-image", map[string]any{"aspectRatio": "1:1"}},
		{"gpt-image-1", map[string]any{"size": "2048x2048"}},
		{"gpt-image-2", map[string]any{"aspectRatio": "5:4"}},
		{"gpt-image-2", map[string]any{"imageSize": "8K"}},
		{"gpt-image-2", map[string]any{"imageSize": "2k"}},
		{"gpt-image-2", map[string]any{"aspectRatio": 1}},
		{"gpt-image-2", map[string]any{"size": "1536x1024", "aspectRatio": "3:2"}},
		{"gpt-image-2", map[string]any{"size": "1000x1000"}},
		{"gpt-image-2", map[string]any{"size": "3840x1024"}},
		{"gpt-image-2", map[string]any{"size": "512x512"}},
		{"gpt-image-2", map[string]any{"size": "4096x2048"}},
		{"gpt-image-2", map[string]any{"size": "01024x1024"}},
		{"gpt-image-2", map[string]any{"size": "1024X1024"}},
		{"gpt-image-2", map[string]any{"size": "auto"}},
		{"gpt-image-2", map[string]any{"quality": "xhigh"}},
		{"gpt-image-2-2026-04-21", map[string]any{"quality": "max"}},
		{"gpt-image-1", map[string]any{"quality": "max"}},
	} {
		t.Run(fmt.Sprintf("%s/%v", tc.model, tc.params), func(t *testing.T) {
			calls := 0
			a := adapter(t, OpenAIID, options(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}, "https://example.invalid"))
			r := request(OpenAIID)
			r.Target.RemoteID = tc.model
			r.Target.Source = "provider"
			r.Request.Parameters = tc.params
			res, err := a.Submit(context.Background(), r)
			assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
			if calls != 0 {
				t.Fatal("sent invalid parameters")
			}
		})
	}
}
