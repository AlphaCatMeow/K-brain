package config

import "fmt"

type AIApprovalConfig struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
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
