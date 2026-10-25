package tui

import (
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type cfgSyncMsg struct {
	mod      time.Time
	theme    string
	language *string
	memory   config.MemoryConfig
}

type cfgSyncTick struct{}

func (m *model) watchConfig() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return cfgSyncTick{} })
}

func (m *model) cfgSync() (tea.Model, tea.Cmd) {
	if m.prog == nil {
		return m, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return m, m.watchConfig()
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil || !fi.ModTime().After(m.cfgMod) {
		return m, m.watchConfig()
	}
	cfg, err := config.Load()
	if err != nil {
		return m, m.watchConfig()
	}
	return m, tea.Sequence(
		func() tea.Msg {
			return cfgSyncMsg{mod: fi.ModTime(), theme: cfg.Theme, language: &cfg.Language, memory: cfg.Memory}
		},
		m.watchConfig(),
	)
}

func (m *model) applyCfgSync(msg cfgSyncMsg) {
	m.cfgMod = msg.mod
	if msg.language != nil && m.cfg != nil && m.cfg.Language != *msg.language {
		m.cfg.Language = *msg.language
		m.input.Placeholder = m.tr(inputPlaceholder)
		m.syncInputPlaceholder()
		m.menu = nil
		m.refreshVP()
	}
	if m.cfg != nil {
		// The switch is changed in LiveAgent; follow it like theme and language.
		changed := m.cfg.Memory.IsEnabled() != msg.memory.IsEnabled()
		m.cfg.AdoptMemory(msg.memory)
		if changed {
			if m.agent != nil {
				m.agent.SetMemoryEnabled(msg.memory.IsEnabled())
			}
			if msg.memory.IsEnabled() {
				m.append(dimStyle.Render("◐ memory was turned back on in LiveAgent"))
			} else {
				m.append(dimStyle.Render(memoryOffNotice))
			}
		}
	}
	if _, pinned := m.cfgExtra["theme"]; pinned {
		return
	}
	if msg.theme != m.cfg.Theme {
		m.cfg.Theme = msg.theme
		m.themeHow = m.applyTheme(msg.theme)
		m.refreshVP()
	}
}
