package imaging

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"time"
)

// These disk-only types intentionally do not embed the public types: their
// MarshalJSON methods redact execution headers and trusted verification evidence.
type imageConfigDiskSettings struct {
	Revision    uint64                      `json:"revision"`
	Connections []imageConfigDiskConnection `json:"connections"`
	Targets     []imageConfigDiskTarget     `json:"targets"`
}
type imageConfigDiskConnection struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Vendor        string            `json:"vendor"`
	Enabled       bool              `json:"enabled"`
	Binding       ConnectionBinding `json:"binding"`
	MaxConcurrent int               `json:"maxConcurrent"`
}
type imageConfigDiskVerification struct {
	BindingDigest    string    `json:"bindingDigest,omitempty"`
	JobID            string    `json:"jobId,omitempty"`
	ContractComplete bool      `json:"contractComplete"`
	LiveVerifiedAt   time.Time `json:"liveVerifiedAt"`
	ConnectionID     string    `json:"connectionId"`
	AdapterVersion   string    `json:"adapterVersion"`
	TargetRevision   uint64    `json:"targetRevision"`
}
type imageConfigDiskTarget struct {
	ID              string                      `json:"id"`
	Name            string                      `json:"name"`
	ConnectionID    string                      `json:"connectionId"`
	Adapter         string                      `json:"adapter"`
	AdapterVersion  string                      `json:"adapterVersion"`
	Kind            string                      `json:"kind"`
	RemoteID        string                      `json:"remoteId"`
	Revision        uint64                      `json:"revision"`
	Enabled         bool                        `json:"enabled"`
	DisabledReason  string                      `json:"disabledReason,omitempty"`
	Capabilities    TargetCapabilities          `json:"capabilities"`
	Parameters      []ParameterSpec             `json:"parameters"`
	WorkflowMapping *WorkflowMapping            `json:"workflowMapping,omitempty"`
	Verification    imageConfigDiskVerification `json:"verification"`
}

// MarshalSettingsConfig is only for private backend configuration files. Never
// use its output in an HTTP response. Secret literals belong in a separate store.
func MarshalSettingsConfig(s ImageSettings) ([]byte, error) {
	p := imageConfigDiskSettings{Revision: s.Revision, Connections: make([]imageConfigDiskConnection, len(s.Connections)), Targets: make([]imageConfigDiskTarget, len(s.Targets))}
	for i, c := range s.Connections {
		p.Connections[i] = imageConfigDiskConnection{ID: c.ID, Name: c.Name, Vendor: c.Vendor, Enabled: c.Enabled, Binding: c.Binding, MaxConcurrent: c.MaxConcurrent}
	}
	for i, t := range s.Targets {
		v := t.Verification
		p.Targets[i] = imageConfigDiskTarget{ID: t.ID, Name: t.Name, ConnectionID: t.ConnectionID, Adapter: t.Adapter, AdapterVersion: t.AdapterVersion, Kind: t.Kind, RemoteID: t.RemoteID, Revision: t.Revision, Enabled: t.Enabled, DisabledReason: t.DisabledReason, Capabilities: t.Capabilities, Parameters: t.Parameters, WorkflowMapping: t.WorkflowMapping, Verification: imageConfigDiskVerification{BindingDigest: v.BindingDigest, JobID: v.JobID, ContractComplete: v.ContractComplete, LiveVerifiedAt: v.LiveVerifiedAt, ConnectionID: v.ConnectionID, AdapterVersion: v.AdapterVersion, TargetRevision: v.TargetRevision}}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, invalid("cannot encode private image configuration")
	}
	return raw, nil
}

// UnmarshalSettingsConfig keeps strict unknown-field rejection, including nested
// verification records. Only trusted disk loading may restore this evidence.
func UnmarshalSettingsConfig(raw []byte) (ImageSettings, error) {
	var p imageConfigDiskSettings
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return ImageSettings{}, invalid("invalid private image configuration")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ImageSettings{}, invalid("invalid private image configuration")
	}
	s := ImageSettings{Revision: p.Revision, Connections: make([]ImageConnection, len(p.Connections)), Targets: make([]ImageTarget, len(p.Targets))}
	for i, c := range p.Connections {
		s.Connections[i] = ImageConnection{ID: c.ID, Name: c.Name, Vendor: c.Vendor, Enabled: c.Enabled, Binding: c.Binding, MaxConcurrent: c.MaxConcurrent}
	}
	for i, t := range p.Targets {
		v := t.Verification
		s.Targets[i] = ImageTarget{ID: t.ID, Name: t.Name, ConnectionID: t.ConnectionID, Adapter: t.Adapter, AdapterVersion: t.AdapterVersion, Kind: t.Kind, RemoteID: t.RemoteID, Revision: t.Revision, Enabled: t.Enabled, DisabledReason: t.DisabledReason, Capabilities: t.Capabilities, Parameters: t.Parameters, WorkflowMapping: t.WorkflowMapping, Verification: TargetVerification{BindingDigest: v.BindingDigest, JobID: v.JobID, ContractComplete: v.ContractComplete, LiveVerifiedAt: v.LiveVerifiedAt, ConnectionID: v.ConnectionID, AdapterVersion: v.AdapterVersion, TargetRevision: v.TargetRevision}}
	}
	return s, nil
}

// CloneSettings preserves execution-only fields without using public JSON.
func CloneSettings(s ImageSettings) ImageSettings {
	out := ImageSettings{Revision: s.Revision, Connections: make([]ImageConnection, len(s.Connections)), Targets: make([]ImageTarget, len(s.Targets))}
	for i, c := range s.Connections {
		c.Binding.SecretRefs = maps.Clone(c.Binding.SecretRefs)
		c.Binding.CustomHeaders = maps.Clone(c.Binding.CustomHeaders)
		out.Connections[i] = c
	}
	for i, t := range s.Targets {
		v := SnapshotTarget(t)
		t.Capabilities = v.Capabilities
		t.Parameters = v.Parameters
		t.WorkflowMapping = v.WorkflowMapping
		for j := range t.Parameters {
			p := &t.Parameters[j]
			p.Default = cloneConfigValue(p.Default)
			for k := range p.Enum {
				p.Enum[k] = cloneConfigValue(p.Enum[k])
			}
		}
		out.Targets[i] = t
	}
	return out
}
func cloneConfigValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = cloneConfigValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneConfigValue(v)
		}
		return out
	default:
		return v
	}
}
