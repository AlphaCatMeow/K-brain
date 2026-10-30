package approval

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func capturingReviewer(evidence *string) *Reviewer {
	return &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		*evidence = req.Messages[1].Content
		return low, ai.Usage{}, nil
	}}
}

// 02a1aa5 keeps a 1-2 character hex tail after a letter outside the address. A short option
// with its value glued on ("-sab:2001:db8::1", getopt reads the address ab:2001:db8::1) is
// now located as the user's own 2001:db8::1: the different host borrows [USER_IPV6_1], the
// evidence is not marked sensitive, and an all-low report auto-approves in auto mode. Before
// the change the whole value was a non-user [REDACTED_IPV6] and the review required a human.
func TestReviewDoesNotLendUserPlaceholderToGluedShortOptionAddress(t *testing.T) {
	var evidence string
	report := capturingReviewer(&evidence).Review(t.Context(), Evidence{
		Phase:      "action",
		Tool:       "Bash",
		UserIntent: "allow 2001:db8::1 through the firewall",
		Action:     `{"command":"ip6tables -I INPUT -sab:2001:db8::1 -j ACCEPT"}`,
	})
	if report.Safe() && strings.Contains(evidence, `-sab:[USER_IPV6_1]`) {
		t.Fatalf("address ab:2001:db8::1 borrowed the user's placeholder and auto-approved; evidence=%s", evidence)
	}
}

// Probe: a label the user typed and the same label in the action keep one placeholder.
func TestReviewLabelledUserAddressKeepsOnePlaceholder(t *testing.T) {
	var evidence string
	report := capturingReviewer(&evidence).Review(t.Context(), Evidence{
		Phase:      "action",
		Tool:       "Bash",
		UserIntent: "add ip6:2001:db8:4860::/32 to the SPF record",
		Action:     `{"command":"echo 'v=spf1 ip6:2001:db8:4860::/32 -all' > spf.txt"}`,
	})
	if !report.Safe() || strings.Count(evidence, "ip6:[USER_IPV6_1]/32") != 2 || strings.Contains(evidence, "REDACTED") {
		t.Fatalf("report=%+v evidence=%s", report, evidence)
	}
}
