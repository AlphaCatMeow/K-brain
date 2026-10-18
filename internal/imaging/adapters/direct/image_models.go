package direct

import (
	"regexp"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

var imageModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
var openAIImageModel = regexp.MustCompile(`^gpt-image-(1|1-mini|1\.5|2|2\.5)(-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)

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

// ModelParameters returns fresh schema slices for the implemented model-specific
// wire contract. It does not claim live verification or change legacy evidence.
func ModelParameters(adapter, model string) []imaging.ParameterSpec {
	if adapter == OpenAIID {
		return ProtocolProfile(adapter).Parameters
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
