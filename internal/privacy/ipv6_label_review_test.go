package privacy

import (
	"strings"
	"testing"
)

// Probe: MaskText (main model) and RedactText (reviewer) locate the same span for label
// inputs, so the token restores exactly the redacted value and nothing beyond the label
// tail stays in clear text.
func TestIPv6LabelMaskAndRedactAgree(t *testing.T) {
	ClearMemory()
	t.Cleanup(ClearMemory)
	for _, tc := range []struct{ input, value string }{
		{"v=spf1 ip6:2001:db8:4860::/32 -all", "2001:db8:4860::"},
		{"eth0:fe80::1 up", "fe80::1"},
		{"src:fe80::1c2d:3e4f", "fe80::1c2d:3e4f"},
		{"IP6:2001:DB8::1", "2001:DB8::1"},
		{"ETH0:FE80::1", "FE80::1"},
		{"地址ip6:2001:db8::1，完", "2001:db8::1"},
		{"src:2001:db8:85a3:0:0:8a2e:370:7334", "2001:db8:85a3:0:0:8a2e:370:7334"},
		{"x1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7:8"},
		{"ip6:2001:db8::1\r\n", "2001:db8::1"},
	} {
		masked, values := MaskText(tc.input)
		var token string
		for k, v := range values {
			if v == tc.value {
				token = k
			}
		}
		if token == "" {
			t.Errorf("MaskText(%q) = %q values=%v, want a token for %q", tc.input, masked, values, tc.value)
			continue
		}
		redacted, _ := RedactText(tc.input)
		if strings.Replace(masked, token, "[REDACTED_IPV6]", 1) != redacted {
			t.Errorf("%q: MaskText %q and RedactText %q disagree", tc.input, masked, redacted)
		}
	}
}

// Probe: what the label rule leaves in clear text. Only the 1-2 character hex tail can stay
// outside; an address that has no "::" after its first group is still taken whole.
func TestIPv6LabelRuleClearTextResidue(t *testing.T) {
	for input, want := range map[string]string{
		"addr_64:ff9b::c000:221": "addr_64:[REDACTED_IPV6]",
		"x1:2:3:4:5:6::8":        "x1:[REDACTED_IPV6]",
		"xab:cd:ef:1:2:3:4:5":    "x[REDACTED_IPV6]",
		"ip6:::ffff:a00:1":       "ip6:::ffff:a00:1", // baseline: "::"-prefixed after a hex tail is not redacted at all
		"eth0:::2001:db8:1":      "eth0:::2001:db8:1",
	} {
		if got, _ := RedactText(input); got != want {
			t.Errorf("RedactText(%q) = %q, want (documented current) %q", input, got, want)
		}
	}
}
