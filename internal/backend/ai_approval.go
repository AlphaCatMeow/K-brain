package backend

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

// newRunReviewer builds the run's AI reviewer. With review scope "per_run" (capability
// ai_review_per_run, sent by hosts that hide the global setting) only auto review reviews,
// with the configured aiApproval model or, without a usable one, the session's model. Without
// it the global aiApproval mode applies to every mode except always, as before.
func (s *Server) newRunReviewer(ctx context.Context, rt *runtimeSession, intent, mode, scope string) *approval.RunReviewer {
	if mode == permissions.Always {
		return nil
	}
	perRun := scope == "per_run"
	if perRun && mode != permissions.AutoReview {
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
	reviewer := approval.New(ctx, cfg)
	if perRun {
		var provider, model string
		rt.mu.Lock()
		if rt.agent != nil {
			provider, model = rt.agent.Provider, rt.agent.ModelName
		}
		rt.mu.Unlock()
		reviewer = approval.NewWithFallback(ctx, cfg, provider, model)
	}
	return approval.NewRun(cfg.AIApproval.Mode, intent, reviewer, func(phase, tool string, report approval.Report, automatic bool) {
		s.publish(rt, "approval.reviewed", struct {
			Phase     string          `json:"phase"`
			Tool      string          `json:"tool"`
			Report    approval.Report `json:"report"`
			Automatic bool            `json:"automatic"`
		}{phase, tool, report, automatic}, "")
	})
}
