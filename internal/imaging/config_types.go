package imaging

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type ImageSettings struct {
	Revision    uint64            `json:"revision"`
	Connections []ImageConnection `json:"connections"`
	Targets     []ImageTarget     `json:"targets"`
}
type ImageSettingsUpdate struct {
	ExpectedRevision uint64            `json:"expectedRevision"`
	Connections      []ImageConnection `json:"connections"`
	Targets          []ImageTarget     `json:"targets"`
}
type ImageConnection struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Vendor               string            `json:"vendor"`
	Enabled              bool              `json:"enabled"`
	Binding              ConnectionBinding `json:"binding"`
	CredentialConfigured bool              `json:"credentialConfigured"`
	MaxConcurrent        int               `json:"maxConcurrent"`
}
type ConnectionBinding struct {
	Kind           string            `json:"kind"`
	ProviderID     string            `json:"providerId,omitempty"`
	BaseURL        string            `json:"baseUrl,omitempty"`
	AuthKind       string            `json:"authKind,omitempty"`
	SecretRefs     map[string]string `json:"secretRefs,omitempty"`
	CustomHeaders  map[string]string `json:"customHeaders,omitempty"`
	UseSystemProxy bool              `json:"useSystemProxy,omitempty"`
}
type ImageTarget struct {
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	ConnectionID      string               `json:"connectionId"`
	Adapter           string               `json:"adapter"`
	AdapterVersion    string               `json:"adapterVersion"`
	Kind              string               `json:"kind"`
	RemoteID          string               `json:"remoteId"`
	Revision          uint64               `json:"revision"`
	Enabled           bool                 `json:"enabled"`
	DisabledReason    string               `json:"disabledReason,omitempty"`
	Capabilities      TargetCapabilities   `json:"capabilities"`
	Parameters        []ParameterSpec      `json:"parameters"`
	WorkflowMapping   *WorkflowMapping     `json:"workflowMapping,omitempty"`
	VerificationState *VerificationSummary `json:"verification,omitempty"` // Read-only; ignored on settings writes.
	Verification      TargetVerification   `json:"-"`                      // Trusted backend evidence, never accepted over HTTP.
}

// Evidence is bound to the connection, adapter version and target revision.
// The registration layer must supply genuine evidence; zero values disable execution.
type TargetVerification struct {
	BindingDigest    string    `json:"-"`
	JobID            string    `json:"-"`
	ContractComplete bool      `json:"-"`
	LiveVerifiedAt   time.Time `json:"-"`
	ConnectionID     string    `json:"-"`
	AdapterVersion   string    `json:"-"`
	TargetRevision   uint64    `json:"-"`
}
type TargetCapabilities struct {
	Operations           []Operation `json:"operations"`
	SupportsCancel       bool        `json:"supportsCancel"`
	SupportsReconcile    bool        `json:"supportsReconcile"`
	SupportsContinuation bool        `json:"supportsContinuation"`
	SupportsPreview      bool        `json:"supportsPreview"`
	MaxInputs            int         `json:"maxInputs"`
	MaxCount             int         `json:"maxCount"`
	OutputFormats        []string    `json:"outputFormats"`
}
type ParameterSpec struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []any    `json:"enum,omitempty"`
	Minimum  *float64 `json:"minimum,omitempty"`
	Maximum  *float64 `json:"maximum,omitempty"`
	Default  any      `json:"default,omitempty"`
	Unit     string   `json:"unit,omitempty"`
}
type WorkflowMapping struct {
	Revision uint64           `json:"revision"`
	Inputs   []WorkflowInput  `json:"inputs"`
	Outputs  []WorkflowOutput `json:"outputs"`
}
type WorkflowInput struct {
	ParameterKey string `json:"parameterKey"`
	NodeID       string `json:"nodeId"`
	InputName    string `json:"inputName"`
}
type WorkflowOutput struct {
	NodeID     string `json:"nodeId"`
	OutputName string `json:"outputName"`
}
type TargetSnapshot struct {
	VerificationID  string             `json:"verificationId,omitempty"` // Backend-issued receipt identity; never a credential.
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	ConnectionID    string             `json:"connectionId"`
	Adapter         string             `json:"adapter"`
	AdapterVersion  string             `json:"adapterVersion"`
	Kind            string             `json:"kind"`
	RemoteID        string             `json:"remoteId"`
	Revision        uint64             `json:"revision"`
	Capabilities    TargetCapabilities `json:"capabilities"`
	Parameters      []ParameterSpec    `json:"parameters"`
	WorkflowMapping *WorkflowMapping   `json:"workflowMapping,omitempty"`
}

// Registry resolves authorized, enabled targets and freezes their verified definition.
// Registration and credential lookup belong to later implementation stages.
type Registry interface {
	ResolveTarget(context.Context, Scope, string) (TargetSnapshot, error)
}
type TargetResolver = Registry

type SecretResolver interface {
	ResolveSecret(context.Context, Scope, string) (string, error)
}
type NetworkProfile struct {
	AllowPrivateAddresses bool     `json:"-"`
	AllowedHosts          []string `json:"-"`
}
type ResolvedConnection struct {
	HTTPClient     *http.Client   `json:"-"`
	SecretResolver SecretResolver `json:"-"`
	NetworkProfile NetworkProfile `json:"-"`
}

func (ResolvedConnection) MarshalJSON() ([]byte, error) {
	return nil, invalid("execution connection cannot be serialized")
}
func (s ImageSettings) MarshalJSON() ([]byte, error) {
	type wire ImageSettings
	s = PublicSettings(s)
	return safeMarshal(wire(s), s.Revision)
}
func (s ImageSettingsUpdate) MarshalJSON() ([]byte, error) {
	type wire ImageSettingsUpdate
	return safeMarshal(wire(s), s.ExpectedRevision)
}
func (t ImageTarget) MarshalJSON() ([]byte, error) {
	type wire ImageTarget
	t = PublicTarget(t)
	return safeMarshal(wire(t), t.Revision)
}
func (t TargetSnapshot) MarshalJSON() ([]byte, error) {
	type wire TargetSnapshot
	return safeMarshal(wire(t), t.Revision)
}
func (m WorkflowMapping) MarshalJSON() ([]byte, error) {
	type wire WorkflowMapping
	return safeMarshal(wire(m), m.Revision)
}
func (c ImageConnection) MarshalJSON() ([]byte, error) {
	type wire ImageConnection
	return json.Marshal(wire(PublicConnection(c, c.CredentialConfigured)))
}
func PublicConnection(c ImageConnection, configured bool) ImageConnection {
	b := ConnectionBinding{Kind: c.Binding.Kind}
	switch b.Kind {
	case "provider_ref":
		b.ProviderID = c.Binding.ProviderID
	case "independent":
		b.BaseURL = publicBaseURL(c.Binding.BaseURL)
		b.AuthKind = c.Binding.AuthKind
		b.UseSystemProxy = c.Binding.UseSystemProxy
		b.SecretRefs = map[string]string{}
		for k, v := range c.Binding.SecretRefs {
			if opaqueRef(v) {
				b.SecretRefs[k] = v
			}
		}
		// Header values are execution configuration, not credential-safe public data.
	}
	return ImageConnection{ID: c.ID, Name: c.Name, Vendor: c.Vendor, Enabled: c.Enabled, Binding: b, CredentialConfigured: configured, MaxConcurrent: c.MaxConcurrent}
}
func PublicTarget(t ImageTarget) ImageTarget {
	s := SnapshotTarget(t)
	state := t.VerificationState
	if state == nil {
		state = &VerificationSummary{State: "unverified"}
		if t.Verification.ContractComplete && !t.Verification.LiveVerifiedAt.IsZero() {
			state = &VerificationSummary{State: "verified", JobID: t.Verification.JobID, LiveVerifiedAt: &t.Verification.LiveVerifiedAt}
		}
	}
	return ImageTarget{VerificationState: state, ID: s.ID, Name: s.Name, ConnectionID: s.ConnectionID, Adapter: s.Adapter, AdapterVersion: s.AdapterVersion, Kind: s.Kind, RemoteID: s.RemoteID, Revision: s.Revision, Enabled: t.Enabled, DisabledReason: t.DisabledReason, Capabilities: s.Capabilities, Parameters: s.Parameters, WorkflowMapping: s.WorkflowMapping}
}
func PublicSettings(s ImageSettings) ImageSettings {
	c := make([]ImageConnection, len(s.Connections))
	for i, v := range s.Connections {
		c[i] = PublicConnection(v, v.CredentialConfigured)
	}
	t := make([]ImageTarget, len(s.Targets))
	for i, v := range s.Targets {
		t[i] = PublicTarget(v)
	}
	return ImageSettings{Revision: s.Revision, Connections: c, Targets: t}
}
func SnapshotTarget(t ImageTarget) TargetSnapshot {
	return cloneSnapshot(TargetSnapshot{ID: t.ID, Name: t.Name, ConnectionID: t.ConnectionID, Adapter: t.Adapter, AdapterVersion: t.AdapterVersion, Kind: t.Kind, RemoteID: t.RemoteID, Revision: t.Revision, Capabilities: t.Capabilities, Parameters: t.Parameters, WorkflowMapping: t.WorkflowMapping})
}
func cloneSnapshot(t TargetSnapshot) TargetSnapshot {
	c := t.Capabilities
	c.Operations = append([]Operation{}, c.Operations...)
	c.OutputFormats = append([]string{}, c.OutputFormats...)
	ps := make([]ParameterSpec, len(t.Parameters))
	for i, p := range t.Parameters {
		ps[i] = ParameterSpec{Key: p.Key, Label: p.Label, Type: p.Type, Required: p.Required, Enum: append([]any(nil), p.Enum...), Default: p.Default, Unit: p.Unit}
		if p.Minimum != nil {
			v := *p.Minimum
			ps[i].Minimum = &v
		}
		if p.Maximum != nil {
			v := *p.Maximum
			ps[i].Maximum = &v
		}
	}
	var m *WorkflowMapping
	if t.WorkflowMapping != nil {
		v := t.WorkflowMapping
		m = &WorkflowMapping{Revision: v.Revision, Inputs: append([]WorkflowInput{}, v.Inputs...), Outputs: append([]WorkflowOutput{}, v.Outputs...)}
	}
	return TargetSnapshot{VerificationID: t.VerificationID, ID: t.ID, Name: t.Name, ConnectionID: t.ConnectionID, Adapter: t.Adapter, AdapterVersion: t.AdapterVersion, Kind: t.Kind, RemoteID: t.RemoteID, Revision: t.Revision, Capabilities: c, Parameters: ps, WorkflowMapping: m}
}
