package backend

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

// newRunReviewer follows the run's permission mode (capability ai_review_per_run): auto
// review reviews every action, while normal ("ask") and auto edit never review, whatever the
// global aiApproval mode says. Runs without a mode, plan and full access keep the global mode.
// The reviewer uses the configured aiApproval model or, without a usable one, the session's.
func (s *Server) newRunReviewer(ctx context.Context, rt *runtimeSession, intent, mode string) *approval.RunReviewer {
	switch mode {
	case permissions.Always, permissions.Normal, permissions.AutoEdit:
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
	var provider, model string
	rt.mu.Lock()
	if rt.agent != nil {
		provider, model = rt.agent.Provider, rt.agent.ModelName
	}
	rt.mu.Unlock()
	return approval.NewRun(cfg.AIApproval.Mode, intent, approval.NewWithFallback(ctx, cfg, provider, model), func(phase, tool string, report approval.Report, automatic bool) {
		s.publish(rt, "approval.reviewed", struct {
			Phase     string          `json:"phase"`
			Tool      string          `json:"tool"`
			Report    approval.Report `json:"report"`
			Automatic bool            `json:"automatic"`
		}{phase, tool, report, automatic}, "")
	})
}
