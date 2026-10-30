package direct

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

var imageModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
var openAIImageModel = regexp.MustCompile(`^gpt-image-(1|1-mini|1\.5|2|2\.5|2\.5-flare|2\.5-sunburst)(-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)

// GPT Image 2 and 2.5 take any WxH within OpenAI's size rules; 2.5 adds xhigh/max
// quality. GPT Image 1/1-mini/1.5 and unknown aliases keep the three fixed sizes.
var openAIFlexibleModel = regexp.MustCompile(`^gpt-image-(2|2\.5|2\.5-flare|2\.5-sunburst)(-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)
var openAI25Model = regexp.MustCompile(`^gpt-image-2\.5(-flare|-sunburst)?(-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)

// ValidImageModelID excludes path/query injection in Google's model URL. Unknown
// aliases require explicit model metadata; names alone never establish a protocol.
func ValidImageModelID(id string) bool { return imageModelID.MatchString(id) }

// ImageModelAdapter is deliberately conservative: image INPUT support, display
// names and generic "image" substrings do not identify an image generation model.
func ImageModelAdapter(id string) string {
	if openAIImageModel.MatchString(id) {
		return OpenAIID
	}
	switch id {
	case "gemini-nano-banana-2.1", "gemini-2.5-flash-image", "gemini-2.5-flash-image-preview", "gemini-3-pro-image", "gemini-3-pro-image-preview", "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview":
		return GoogleID
	}
	return ""
}

var googleAspectRatios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}

// Resolution is model-dependent: Gemini 2.5 does not accept imageSize. Unknown
// aliases only expose the common aspect-ratio contract, without guessing sizes.
func googleImageSizes(model string) []string {
	switch model {
	case "gemini-nano-banana-2.1", "gemini-3-pro-image", "gemini-3-pro-image-preview":
		return []string{"1K", "2K", "4K"}
	case "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview":
		return []string{"512", "1K", "2K", "4K"}
	}
	return nil
}

var openAIAspectRatios = []string{"1:1", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16", "21:9"}
var openAIImageSizes = []string{"1K", "2K", "4K"}

// Pixel budget per tier: about 1024x1024, OpenAI's reliable upper bound 2560x1440,
// and its maximum 3840x2160.
var openAITierPixels = map[string]int{"1K": 1024 * 1024, "2K": 2560 * 1440, "4K": 3840 * 2160}

func openAIQualities(model string) []string {
	q := []string{"low", "medium", "high", "auto"}
	if openAI25Model.MatchString(model) {
		q = append(q, "xhigh", "max")
	}
	return q
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// openAISize returns the largest size at exactly the ratio whose edges are
// multiples of 16 and at most 3840 px and whose area fits the tier budget.
func openAISize(ratio, tier string) string {
	a, b, _ := strings.Cut(ratio, ":")
	w, _ := strconv.Atoi(a)
	h, _ := strconv.Atoi(b)
	d := gcd(w, h)
	w, h = w/d, h/d
	size := ""
	for k := 1; w*k <= 3840 && h*k <= 3840; k++ {
		if (w*k)%16 == 0 && (h*k)%16 == 0 && w*k*h*k <= openAITierPixels[tier] {
			size = fmt.Sprintf("%dx%d", w*k, h*k)
		}
	}
	return size
}

// openAIPixelSize accepts an explicit WxH under OpenAI's GPT Image 2 rules: edges
// divisible by 16, long edge at most 3840 and 3x the short edge, and 655,360 to
// 8,294,400 pixels.
func openAIPixelSize(v any) (string, error) {
	s, _ := v.(string)
	a, b, ok := strings.Cut(s, "x")
	w, e1 := strconv.Atoi(a)
	h, e2 := strconv.Atoi(b)
	if !ok || e1 != nil || e2 != nil || strconv.Itoa(w) != a || strconv.Itoa(h) != b || w <= 0 || h <= 0 {
		return "", badInput()
	}
	long, short := max(w, h), min(w, h)
	if w%16 != 0 || h%16 != 0 || long > 3840 || long > 3*short || w*h < 655360 || w*h > 8294400 {
		return "", badInput()
	}
	return s, nil
}

// ModelParameters returns fresh schema slices for the implemented model-specific
// wire contract. It does not claim live verification or change legacy evidence.
func ModelParameters(adapter, model string) []imaging.ParameterSpec {
	if adapter == OpenAIID {
		parameters := ProtocolProfile(adapter).Parameters
		if !openAIFlexibleModel.MatchString(model) {
			return parameters
		}
		// Ratio and tier replace the fixed size enum; requests turn them into WxH.
		aspect, tier := enumSpec("aspectRatio", openAIAspectRatios...), enumSpec("imageSize", openAIImageSizes...)
		aspect.Label, tier.Label = "Aspect ratio", "Image size"
		out := []imaging.ParameterSpec{aspect, tier}
		for _, p := range parameters {
			switch p.Key {
			case "size":
				continue
			case "quality":
				p = enumSpec("quality", openAIQualities(model)...)
			}
			out = append(out, p)
		}
		return out
	}
	if adapter != GoogleID {
		return []imaging.ParameterSpec{}
	}
	aspect := enumSpec("aspectRatio", googleAspectRatios...)
	aspect.Label = "Aspect ratio"
	parameters := []imaging.ParameterSpec{aspect}
	if sizes := googleImageSizes(model); len(sizes) > 0 {
		size := enumSpec("imageSize", sizes...)
		size.Label = "Image size"
		parameters = append(parameters, size)
	}
	return parameters
}
