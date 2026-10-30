package privacy

import "testing"

// A hex tail that belongs to a word ("ip6", "eth0") stays outside the address when the part
// after the colon is an address on its own; the reviewer and the model keep the label.
func TestIPv6KeepsWordLabelHexTail(t *testing.T) {
	for input, want := range map[string]string{
		"v=spf1 ip6:2001:db8:4860::/32 -all": "v=spf1 ip6:[REDACTED_IPV6]/32 -all",
		"eth0:fe80::1 up":                    "eth0:[REDACTED_IPV6] up",
		"route via 2001:db8::1":              "route via [REDACTED_IPV6]",
		"c:fe80::1":                          "[REDACTED_IPV6]",
		"x1:2:3:4:5:6:7:8":                   "x[REDACTED_IPV6]",
	} {
		if got, _ := RedactText(input); got != want {
			t.Errorf("RedactText(%q) = %q, want %q", input, got, want)
		}
	}
}
