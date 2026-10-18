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
func enumSpec(key string, values ...string) imaging.ParameterSpec {
	p := imaging.ParameterSpec{Key: key, Label: key, Type: "string"}
	for _, v := range values {
		p.Enum = append(p.Enum, v)
	}
	return p
}
