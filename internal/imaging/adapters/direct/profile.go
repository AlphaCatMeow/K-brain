package direct

import "github.com/Stack-Cairn/K-brain/internal/imaging"

// Profile is static implementation metadata, never TargetVerification. Registry
// owners must bind real evidence to the connection/revision; no live timestamps
// or enabled targets are manufactured here. RemoteIDs are frozen verification
// examples; ImageModelAdapter also recognizes ordinary image model families.
// Explicit provider metadata may bind aliases. Ark additionally accepts bound ep-* IDs.
type Profile struct {
	Adapter          string
	Version          string
	ContractComplete bool
	LiveVerified     bool
	RemoteIDs        []string
	Capabilities     imaging.TargetCapabilities
	Parameters       []imaging.ParameterSpec
}

func (a *Adapter) Profile() Profile { return ProtocolProfile(a.id) }

// ProtocolProfile describes implemented wire capabilities without claiming live
// verification or provider-specific support. Callers may restrict this profile.
func ProtocolProfile(adapter string) Profile {
	p := Profile{Adapter: adapter, Version: Version, ContractComplete: adapter != GoogleID,
		Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit}, MaxCount: 1}}
	switch adapter {
	case OpenAIID:
		p.RemoteIDs = []string{"gpt-image-2", "gpt-image-2-2026-04-21"}
		p.Capabilities.MaxInputs = 16 // Includes mask; locally stricter than 16 images plus a mask.
		p.Capabilities.Operations = append(p.Capabilities.Operations, imaging.OperationMask)
		p.Capabilities.OutputFormats = []string{"png", "jpeg", "webp"}
		p.Parameters = []imaging.ParameterSpec{
			enumSpec("size", "1024x1024", "1536x1024", "1024x1536"), enumSpec("quality", "low", "medium", "high", "auto"),
			enumSpec("output_format", "png", "jpeg", "webp"), enumSpec("background", "opaque", "transparent", "auto"),
		}
		low, high := float64(0), float64(100)
		p.Parameters = append(p.Parameters, imaging.ParameterSpec{Key: "output_compression", Label: "Output compression", Type: "integer", Minimum: &low, Maximum: &high})
	case GoogleID:
		p.RemoteIDs = []string{"gemini-nano-banana-2.1"}
		p.Capabilities.MaxInputs = 14
		// PNG is the frozen output example; it is not a selectable MIME parameter.
		p.Capabilities.OutputFormats = []string{"png"}
		p.Parameters = []imaging.ParameterSpec{}
	case VolcengineID:
		p.RemoteIDs = []string{"doubao-seedream-5-0-pro-260628"}
		p.Capabilities.MaxInputs = 10
		p.Capabilities.OutputFormats = []string{"png", "jpeg"}
		p.Parameters = []imaging.ParameterSpec{
			{Key: "size", Label: "Size (1K, 1.5K, 2K or width x height)", Type: "string"},
			enumSpec("response_format", "b64_json", "url"), enumSpec("output_format", "png", "jpeg"),
			{Key: "watermark", Label: "Watermark", Type: "boolean"}, enumSpec("optimize_prompt_options.mode", "standard", "fast"),
		}
	}
	return p
}

// ProfileForBaseURL names the vendor dialect behind an OpenAI-compatible base URL.
// Only exact official endpoints qualify; proxies keep the standard OpenAI contract.
func ProfileForBaseURL(base string) string {
	switch base {
	case "https://rolldek.com/v1", "https://api-cn.rolldek.com/v1":
		return "rolldek"
	case "https://apihub.agnes-ai.com/v1", "https://api.agnes-ai.cn/v1":
		return "agnes"
	}
	return ""
}

var agnesSizes = []string{"1K", "2K", "3K", "4K"}
var agnesRatios = []string{"1:1", "3:4", "4:3", "16:9", "9:16", "2:3", "3:2", "21:9"}

// AgnesParameters is the tiered size/ratio contract of Agnes Image; exact pixel
// sizes are normalized server-side, so only tiers are offered. Keys reuse the
// Google names clients already render; the request maps them to size/ratio.
func AgnesParameters() []imaging.ParameterSpec {
	size, ratio := enumSpec("imageSize", agnesSizes...), enumSpec("aspectRatio", agnesRatios...)
	size.Label, ratio.Label = "Size", "Aspect ratio"
	return []imaging.ParameterSpec{ratio, size}
}

func enumSpec(key string, values ...string) imaging.ParameterSpec {
	p := imaging.ParameterSpec{Key: key, Label: key, Type: "string"}
	for _, v := range values {
		p.Enum = append(p.Enum, v)
	}
	return p
}
