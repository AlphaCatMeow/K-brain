package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// Independent oracle: brute-force every exact-ratio WxH (16-px grid, edge <= 3840)
// and keep the largest area inside the tier budget. Compared with the wire size.
func reviewerBruteForceSize(rw, rh, budget int) string {
	best, bestArea := "", 0
	for w := 16; w <= 3840; w += 16 {
		if (w*rh)%rw != 0 {
			continue
		}
		h := w * rh / rw
		if h%16 != 0 || h > 3840 || h < 16 {
			continue
		}
		if a := w * h; a <= budget && a > bestArea {
			best, bestArea = fmt.Sprintf("%dx%d", w, h), a
		}
	}
	return best
}

func reviewerSubmitSize(t *testing.T, model string, params map[string]any) (string, error) {
	t.Helper()
	got := "<not sent>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got, _ = body["size"].(string)
		io.WriteString(w, responseJSON(OpenAIID, syntheticPNG(t, 16, 20, false)))
	}))
	defer srv.Close()
	a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
	r := request(OpenAIID)
	r.Target.RemoteID = model
	r.Target.Source = "provider"
	r.Request.Parameters = params
	_, err := a.Submit(context.Background(), r)
	return got, err
}

func TestReviewerOpenAIRatioTierMatchesBruteForceAndRules(t *testing.T) {
	budgets := map[string]int{"1K": 1024 * 1024, "2K": 2560 * 1440, "4K": 3840 * 2160}
	ratios := map[string][2]int{"1:1": {1, 1}, "3:2": {3, 2}, "2:3": {2, 3}, "4:3": {4, 3}, "3:4": {3, 4}, "16:9": {16, 9}, "9:16": {9, 16}, "21:9": {21, 9}}
	for ratio, rr := range ratios {
		for tier, budget := range budgets {
			want := reviewerBruteForceSize(rr[0], rr[1], budget)
			for _, params := range []map[string]any{{"aspectRatio": ratio, "imageSize": tier}} {
				got, err := reviewerSubmitSize(t, "gpt-image-2.5-sunburst-2026-09-08", params)
				if err != nil || got != want {
					t.Errorf("%s/%s: got %q err=%v, want %q", ratio, tier, got, err, want)
					continue
				}
				openAIRule(t, got)
			}
		}
		// ratio alone -> 1K
		if got, err := reviewerSubmitSize(t, "gpt-image-2", map[string]any{"aspectRatio": ratio}); err != nil || got != reviewerBruteForceSize(rr[0], rr[1], budgets["1K"]) {
			t.Errorf("%s alone: got %q err=%v", ratio, got, err)
		}
	}
	for tier, budget := range budgets {
		if got, err := reviewerSubmitSize(t, "gpt-image-2", map[string]any{"imageSize": tier}); err != nil || got != reviewerBruteForceSize(1, 1, budget) {
			t.Errorf("%s alone: got %q err=%v", tier, got, err)
		}
	}
}

func TestReviewerOpenAIPixelSizeAdversarialInputs(t *testing.T) {
	for _, v := range []any{
		"", "x", "16x16x16", "1024x1024x", "x1024", "1024x", "-1024x-1024", "+1024x1024", "1024x+1024",
		" 1024x1024", "1024x1024 ", "1024 x 1024", "１０２４x1024", "1024×1024", "1024X1024", "0x0",
		"99999999999999999999x16", "9223372036854775807x9223372036854775807", "3856x2160", "3840x1264",
		"1024.0x1024", "0x1024", "00x1024", "1024x01024", 1024, 1024.0, nil, true, []any{"1024x1024"},
		"3840x2176", // over pixel cap
		"816x800",   // 652,800 < min
	} {
		calls := 0
		a := adapter(t, OpenAIID, options(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}, "https://example.invalid"))
		r := request(OpenAIID)
		r.Target.Source = "provider"
		r.Request.Parameters = map[string]any{"size": v}
		res, err := a.Submit(context.Background(), r)
		assertError(t, res, err, imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		if calls != 0 {
			t.Fatalf("%#v sent", v)
		}
	}
	// Boundaries that OpenAI documents as valid must pass.
	for _, s := range []string{"640x1024", "1024x640", "3840x2160", "2160x3840", "3840x1280", "1280x3840", "2880x2880"} {
		if got, err := reviewerSubmitSize(t, "gpt-image-2", map[string]any{"size": s}); err != nil || got != s {
			t.Errorf("valid boundary %s: got %q err=%v", s, got, err)
		}
	}
}

// Every value the advertised schema offers must be accepted by request(), for
// every model shape, so the workbench can never offer a value that fails before HTTP.
func TestReviewerOpenAISchemaAndRequestAgreeAcrossModelNames(t *testing.T) {
	models := []string{
		"gpt-image-1", "gpt-image-1-mini", "gpt-image-1.5", "gpt-image-1.5-2025-12-16",
		"gpt-image-2", "gpt-image-2-2026-04-21", "gpt-image-2.5", "gpt-image-2.5-2026-09-08",
		"gpt-image-2.5-flare", "gpt-image-2.5-sunburst-2026-09-08",
		"gpt-image-2-all", "gpt-image-2.5-flare-preview", "GPT-Image-2", "gpt-image-2.5-FLARE", "moonup-image",
	}
	for _, model := range models {
		for _, p := range ModelParameters(OpenAIID, model) {
			values := p.Enum
			extra := map[string]any{}
			if p.Key == "output_compression" {
				values = []any{0, 100}
				extra["output_format"] = "jpeg"
			}
			for _, v := range values {
				params := map[string]any{p.Key: v}
				for k, x := range extra {
					params[k] = x
				}
				if p.Key == "background" && v == "transparent" {
					params["output_format"] = "png"
				}
				if _, err := reviewerSubmitSize(t, model, params); err != nil {
					t.Errorf("%s: schema offers %s=%v but request rejects it: %v", model, p.Key, v, err)
				}
			}
		}
		flexible := strings.Contains(fmt.Sprint(parameterKeys(ModelParameters(OpenAIID, model))), "aspectRatio")
		if flexible != openAIFlexibleModel.MatchString(model) {
			t.Errorf("%s: schema/request flexibility disagree", model)
		}
	}
}
