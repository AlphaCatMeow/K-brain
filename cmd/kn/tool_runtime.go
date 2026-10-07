package main

import (
	"fmt"
	"os"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/computer"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Non-TUI entrypoints must initialize the runtimes before advertising their tools.
func initializeToolRuntime(cfg *config.Config) (func(), error) {
	mode := browser.ModeLive
	if cfg.Browser.Mode != "" {
		mode = browser.Mode(cfg.Browser.Mode)
	}
	enabled := cfg.Browser.Enabled == nil || *cfg.Browser.Enabled
	if enabled {
		switch mode {
		case browser.ModeLive, browser.ModeDedicated, browser.ModeHeadless, browser.ModeExtension:
		default:
			return nil, fmt.Errorf("unknown browser.mode %q", mode)
		}
	}
	previous, policy, private := tools.Browser, tools.ComputerPolicy, browser.AllowPrivateURLs
	oldURL, hadURL := os.LookupEnv("K_BRAIN_CDP_URL")
	if cfg.Browser.CDPURL != "" {
		if err := os.Setenv("K_BRAIN_CDP_URL", cfg.Browser.CDPURL); err != nil {
			return nil, err
		}
	}
	var manager *browser.Manager
	if enabled {
		manager = browser.NewManager(mode)
	}
	tools.Browser = manager
	browser.AllowPrivateURLs = cfg.Browser.AllowPrivateURLs
	tools.ComputerPolicy = computer.NewPolicy(cfg.Computer.Allow, cfg.Computer.Deny, cfg.Computer.DefaultDeny != nil && *cfg.Computer.DefaultDeny)
	return func() {
		if manager != nil {
			manager.CloseAll()
		}
		tools.Browser, tools.ComputerPolicy, browser.AllowPrivateURLs = previous, policy, private
		if hadURL {
			_ = os.Setenv("K_BRAIN_CDP_URL", oldURL)
		} else {
			_ = os.Unsetenv("K_BRAIN_CDP_URL")
		}
	}, nil
}

func withToolAvailability(cfg *config.Config) agent.Option {
	return func(a *agent.Agent) {
		a.BrowserDisabled = cfg.Browser.Enabled != nil && !*cfg.Browser.Enabled
		a.ComputerDisabled = cfg.Computer.Enabled != nil && !*cfg.Computer.Enabled
	}
}
