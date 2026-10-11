package backend

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/approval"
)

func (s *Server) newRunReviewer(ctx context.Context, rt *runtimeSession, intent string) *approval.RunReviewer {
	if s.settings == nil {
		return nil
	}
	cfg := s.settings.Snapshot()
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
