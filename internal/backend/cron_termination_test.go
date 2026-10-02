package backend

import (
	"context"
	"testing"
)

func TestCronTerminationOutputPreservesDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, output, reason, want string
	}{
		{"cancel", "context canceled\ncontext canceled", "Cron run cancelled by user.", "Cron run cancelled by user."},
		{"timeout", context.DeadlineExceeded.Error(), "Cron run timed out.", "Cron run timed out."},
		{"persistence", "persist run: permission denied\ncontext canceled", "Cron run cancelled by user.", "Cron run cancelled by user.\npersist run: permission denied"},
		{"partial output", "compiled 3 files\ncontext deadline exceeded", "Cron run timed out.", "Cron run timed out.\ncompiled 3 files"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronTerminationOutput(tt.output, tt.reason); got != tt.want {
				t.Fatalf("output=%q want=%q", got, tt.want)
			}
		})
	}
}
