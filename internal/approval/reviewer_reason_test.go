package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// A real reviewer model reads [USER_*] placeholders as hidden data unless the policy says
// what they are; the run then asks the user about the value they just typed.
func TestPolicyExplainsUserPlaceholders(t *testing.T) {
	var system string
	r := &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		system = req.Messages[0].Content
		return low, ai.Usage{}, nil
	}}
	r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: "echo 2001:db8::1", UserIntent: "echo 2001:db8::1"})
	for _, want := range []string{"[USER_", "user_intent", "not redacted", "[REDACTED_"} {
		if !strings.Contains(system, want) {
			t.Fatalf("policy does not explain user placeholders (missing %q):\n%s", want, system)
		}
	}
}

// The reason names the kind of failure so the user can tell a broken review model from a
// slow one, but never carries the upstream error text.
func TestReviewerFailureReasonNamesTheKind(t *testing.T) {
	failing := func(err error) *Reviewer {
		return &Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
			return "", ai.Usage{}, err
		}}
	}
	cases := map[string]struct {
		r    *Reviewer
		want string
	}{
		"http":      {failing(fmt.Errorf("wrapped: %w", &ai.HTTPError{Status: "400 Bad Request", StatusCode: 400, Body: "model secret-model is unavailable"})), "Reviewer request failed (HTTP 400); manual approval required"},
		"route":     {failing(routeError{errors.New("no provider secret-provider")}), "Reviewer model could not be resolved; manual approval required"},
		"truncated": {failing(&ai.OutputLimitError{Reason: "max_tokens"}), "Reviewer reply was truncated; manual approval required"},
		"other":     {failing(errors.New("secret upstream error")), "Reviewer failed or timed out; manual approval required"},
		"timeout": {&Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
			return "", ai.Usage{}, context.DeadlineExceeded
		}}, "Reviewer timed out; manual approval required"},
	}
	for name, tc := range cases {
		report := tc.r.Review(t.Context(), Evidence{Action: "ls"})
		if report.Safe() || report.Reason != tc.want {
			t.Errorf("%s: reason = %q, want %q", name, report.Reason, tc.want)
		}
		if strings.Contains(report.Reason, "secret") {
			t.Errorf("%s: upstream text leaked: %q", name, report.Reason)
		}
	}
}
