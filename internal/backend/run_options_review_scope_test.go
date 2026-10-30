package backend

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// review_scope opts a run into per-run AI review (capability ai_review_per_run); a typo must
// be refused rather than silently running under the global aiApproval mode.
func TestRunOptionsReviewScope(t *testing.T) {
	for scope, ok := range map[string]bool{"": true, "per_run": true, "perrun": false, "global": false} {
		out, err := normalizeRunOptions(&protocol.RunOptions{ReviewScope: scope}, t.TempDir(), nil)
		if (err == nil) != ok {
			t.Fatalf("review_scope %q: err = %v, want ok=%v", scope, err, ok)
		}
		if ok && out.ReviewScope != scope {
			t.Fatalf("review_scope %q normalized to %q", scope, out.ReviewScope)
		}
	}
}
