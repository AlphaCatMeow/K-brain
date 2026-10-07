package agent

import (
	"context"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/computer/cua"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func WithComputerConfig(cfg config.ComputerConfig) Option {
	return func(a *Agent) {
		a.ComputerConfig = cfg
		a.ComputerConfig.Command = append([]string(nil), cfg.Command...)
		a.ComputerDisabled = cfg.Enabled != nil && !*cfg.Enabled
	}
}

func (a *Agent) computerTurn(ctx context.Context) (context.Context, func() error) {
	client := cua.New(a.ComputerConfig.Command, tools.WorkingDir(ctx))
	ctx = tools.WithComputerRuntime(ctx, client)
	ctx = tools.WithComputerConfig(ctx, a.ComputerConfig)
	return ctx, func() error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return client.Close(cleanup)
	}
}
