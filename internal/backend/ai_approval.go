package backend

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

func (s *Server) newRunReviewer(ctx context.Context, rt *runtimeSession, intent, mode string) *approval.RunReviewer {
	if mode == permissions.Always {
		return nil
	}
	cfg := &config.Config{}
	if s.settings != nil {
		cfg = s.settings.Snapshot()
	}
	if mode == permissions.AutoReview {
		cfg.AIApproval.Mode = "auto"
	}
	if !cfg.AIApproval.Enabled() {
		return nil
	}
	return approval.NewRun(cfg.AIApproval.Mode, intent, approval.New(ctx, cfg), func(phase, tool string, report approval.Report, automatic bool) {
		s.publish(rt, "approval.reviewed", struct {
			Phase     string          `json:"phase"`
			Tool      string          `json:"tool"`
			Report    approval.Report `json:"report"`
			Automatic bool            `json:"automatic"`
		}{phase, tool, report, automatic}, "")
	})
}
