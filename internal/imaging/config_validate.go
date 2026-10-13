package imaging

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
)

// KnownAdapter identifies a protocol, not an implementation or proof of verification.
func KnownAdapter(id string) bool {
	switch id {
	case "openai-images", "google-generate-content-image", "google-interactions-image", "volcengine-ark-image", "jimeng-image", "runninghub-workflow":
		return true
	}
	return false
}

var opaquePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func opaqueRef(s string) bool { return opaquePattern.MatchString(s) }
func validID(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && len(s) > len(prefix) && opaqueRef(s)
}
func publicBaseURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}
func ValidateSettings(s ImageSettings) error {
	return validateSettings(s, nil)
}

// ValidateSettingsWithProviders adds the external provider catalog check without
// inventing provider state inside the public settings document. The caller must
// supply the current trusted catalog when accepting provider_ref updates.
func ValidateSettingsWithProviders(s ImageSettings, providerExists func(string) bool) error {
	if providerExists == nil {
		return invalid("provider catalog is required")
	}
	return validateSettings(s, providerExists)
}
func validateSettings(s ImageSettings, providerExists func(string) bool) error {
	if s.Revision > MaxSafeInteger {
		return invalid("settings revision exceeds wire range")
	}
	connections := map[string]ImageConnection{}
	for _, c := range s.Connections {
		if !validID(c.ID, "ic_") {
			return invalid("invalid connection ID")
		}
		if _, ok := connections[c.ID]; ok {
			return invalid("duplicate connection ID")
		}
		connections[c.ID] = c
		if c.MaxConcurrent < 1 || c.MaxConcurrent > 16 {
			return invalid("connection concurrency must be 1 through 16")
		}
		b := c.Binding
		switch b.Kind {
		case "provider_ref":
			if b.ProviderID == "" {
				return invalid("provider reference is required")
			}
			if providerExists != nil && !providerExists(b.ProviderID) {
				return invalid("provider reference does not exist")
			}
			if b.BaseURL != "" || b.AuthKind != "" || len(b.SecretRefs) > 0 || len(b.CustomHeaders) > 0 || b.UseSystemProxy {
				return invalid("provider reference has independent fields")
			}
		case "independent":
			if b.ProviderID != "" || publicBaseURL(b.BaseURL) == "" {
				return invalid("invalid independent connection URL")
			}
			switch b.AuthKind {
			case "bearer", "api_key_header", "signed_request":
			default:
				return invalid("unsupported authentication kind")
			}
			for purpose, ref := range b.SecretRefs {
				if !keyPattern.MatchString(purpose) || !opaqueRef(ref) {
					return invalid("secret reference must be opaque")
				}
			}
			for name, value := range b.CustomHeaders {
				if !keyPattern.MatchString(name) || strings.ContainsAny(value, "\r\n") {
					return invalid("invalid custom header")
				}
				switch strings.ToLower(name) {
				case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
					return invalid("credentials require secret references")
				}
			}
		default:
			return invalid("invalid connection binding kind")
		}
	}
	seen := map[string]bool{}
	for _, t := range s.Targets {
		if !validID(t.ID, "it_") || seen[t.ID] {
			return invalid("invalid or duplicate target ID")
		}
		seen[t.ID] = true
		c, ok := connections[t.ConnectionID]
		if !ok {
			return invalid("target connection does not exist")
		}
		if t.Kind != "model" && t.Kind != "workflow" {
			return invalid("invalid target kind")
		}
		if t.Adapter == "" || t.AdapterVersion == "" || t.RemoteID == "" {
			return invalid("target execution identity is required")
		}
		if t.Revision > MaxSafeInteger {
			return invalid("target revision exceeds wire range")
		}
		if err := validateCapabilities(t.Capabilities); err != nil {
			return err
		}
		if err := validateSchema(t.Parameters); err != nil {
			return err
		}
		if err := validateWorkflow(t.Kind, t.WorkflowMapping, t.Parameters); err != nil {
			return err
		}
		if t.Enabled {
			if t.Adapter == "google-generate-content-image" || t.Adapter == "google-interactions-image" || t.Adapter == "runninghub-workflow" || t.Adapter == "jimeng-image" {
				return invalid("adapter contract or signing verification is incomplete")
			}
			if t.Verification.BindingDigest != "" {
				if err := ValidateVerificationProfile(SnapshotTarget(t)); err != nil {
					return err
				}
			}
			if !KnownAdapter(t.Adapter) {
				return invalid("enabled target uses an unknown adapter")
			}
			if !c.Enabled {
				return invalid("enabled target uses a disabled connection")
			}
			v := t.Verification
			if !v.ContractComplete || v.LiveVerifiedAt.IsZero() || v.ConnectionID != t.ConnectionID || v.AdapterVersion != t.AdapterVersion || v.TargetRevision != t.Revision {
				return invalid("enabled target requires current contract and live verification")
			}
		}
	}
	return nil
}
func validateCapabilities(c TargetCapabilities) error {
	if c.MaxInputs < 0 || c.MaxInputs > 16 || c.MaxCount < 1 || c.MaxCount > 16 {
		return invalid("invalid target input or count limits")
	}
	if len(c.Operations) == 0 || len(c.OutputFormats) == 0 {
		return invalid("target capabilities are empty")
	}
	ops := map[Operation]bool{}
	for _, op := range c.Operations {
		if !validOperation(op) || ops[op] {
			return invalid("invalid or duplicate operation")
		}
		ops[op] = true
		if op == OperationMask && c.MaxInputs < 2 {
			return invalid("mask requires two inputs")
		}
		if (op == OperationReference || op == OperationEdit) && c.MaxInputs < 1 {
			return invalid("operation requires inputs")
		}
	}
	formats := map[string]bool{}
	for _, f := range c.OutputFormats {
		if (f != "png" && f != "jpeg" && f != "webp") || formats[f] {
			return invalid("invalid or duplicate output format")
		}
		formats[f] = true
	}
	return nil
}
func validOperation(op Operation) bool {
	return op == OperationGenerate || op == OperationReference || op == OperationEdit || op == OperationMask
}
func validateSchema(ps []ParameterSpec) error {
	seen := map[string]bool{}
	for _, p := range ps {
		if !keyPattern.MatchString(p.Key) || seen[p.Key] {
			return invalid("invalid or duplicate parameter key")
		}
		seen[p.Key] = true
		switch p.Type {
		case "string", "number", "integer", "boolean":
		default:
			return invalid("unsupported parameter schema type")
		}
		if (p.Minimum != nil || p.Maximum != nil) && p.Type != "number" && p.Type != "integer" {
			return invalid("range requires numeric parameter")
		}
		for _, n := range []*float64{p.Minimum, p.Maximum} {
			if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || (p.Type == "integer" && math.Trunc(*n) != *n)) {
				return invalid("invalid schema range")
			}
		}
		if p.Minimum != nil && p.Maximum != nil && *p.Minimum > *p.Maximum {
			return invalid("reversed schema range")
		}
		if p.Enum != nil && len(p.Enum) == 0 {
			return invalid("empty parameter enum")
		}
		values := map[string]bool{}
		for _, v := range p.Enum {
			if err := validateParameter(v, p, false); err != nil {
				return err
			}
			s, err := canonicalValue(v)
			if err != nil {
				return err
			}
			if values[string(s)] {
				return invalid("duplicate enum value")
			}
			values[string(s)] = true
		}
		if p.Default != nil {
			if err := validateParameter(p.Default, p, true); err != nil {
				return fmt.Errorf("%s: invalid schema default", CodeInvalidInput)
			}
		}
	}
	return nil
}
func validateWorkflow(kind string, m *WorkflowMapping, ps []ParameterSpec) error {
	if kind == "model" {
		if m != nil {
			return invalid("model cannot contain workflow mapping")
		}
		return nil
	}
	if m == nil || m.Revision == 0 || m.Revision > MaxSafeInteger || len(m.Outputs) == 0 {
		return invalid("workflow requires versioned mapping and outputs")
	}
	params := map[string]bool{}
	for _, p := range ps {
		params[p.Key] = true
	}
	bound := map[string]bool{}
	slots := map[string]bool{}
	for _, in := range m.Inputs {
		slot := in.NodeID + "\x00" + in.InputName
		if !params[in.ParameterKey] || in.NodeID == "" || in.InputName == "" || bound[in.ParameterKey] || slots[slot] {
			return invalid("invalid workflow input mapping")
		}
		bound[in.ParameterKey] = true
		slots[slot] = true
	}
	for _, p := range ps {
		if !bound[p.Key] {
			return invalid("workflow parameter lacks input mapping")
		}
	}
	slots = map[string]bool{}
	for _, out := range m.Outputs {
		slot := out.NodeID + "\x00" + out.OutputName
		if out.NodeID == "" || out.OutputName == "" || slots[slot] {
			return invalid("invalid workflow output mapping")
		}
		slots[slot] = true
	}
	return nil
}
