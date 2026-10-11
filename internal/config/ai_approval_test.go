package config

import "testing"

func TestAIApprovalModes(t *testing.T) {
	for _, mode := range []string{"", "off", "risk_only", "auto"} {
		c := AIApprovalConfig{Mode: mode, Provider: "p", Model: "small"}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []AIApprovalConfig{{Mode: "allow"}, {Mode: "auto"}, {Mode: "risk_only", Model: "small"}} {
		if c.Validate() == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}
