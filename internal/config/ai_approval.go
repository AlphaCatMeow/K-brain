package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type AIApprovalConfig struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// UnmarshalJSON accepts the read-only perRun marker that /v1/settings adds (capability
// ai_review_per_run), so a host can write back exactly what it read. The marker is never
// stored; any other unknown field is still rejected.
func (c *AIApprovalConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Mode     string          `json:"mode"`
		Provider string          `json:"provider,omitempty"`
		Model    string          `json:"model,omitempty"`
		PerRun   json.RawMessage `json:"perRun,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*c = AIApprovalConfig{Mode: raw.Mode, Provider: raw.Provider, Model: raw.Model}
	return nil
}

func (c AIApprovalConfig) Enabled() bool { return c.Mode == "risk_only" || c.Mode == "auto" }

func (c AIApprovalConfig) Validate() error {
	switch c.Mode {
	case "", "off":
		return nil
	case "risk_only", "auto":
		if c.Provider == "" || c.Model == "" {
			return fmt.Errorf("aiApproval requires an explicit reviewer provider and model")
		}
		return nil
	default:
		return fmt.Errorf("aiApproval.mode must be off, risk_only, or auto")
	}
}
