package tui

func (m *model) cyclePermissionMode() {
	switch m.permissionMode {
	case "plan":
		m.setPermissionMode("auto_review")
	case "auto_review":
		m.setPermissionMode("always")
	case "always":
		m.setPermissionMode("normal")
	default:
		m.setPermissionMode("plan")
	}
}

func (m *model) setPermissionMode(mode string) {
	m.permissionMode = mode
	if m.agent != nil {
		m.agent.SetPlanMode(mode == "plan")
	}
	// No feedback line: the mode chip in the footer already shows the change.
}

func (m *model) permissionModeLabel() string {
	switch m.permissionMode {
	case "plan":
		return m.tr("Plan")
	case "always":
		return m.tr("Always allow")
	case "auto_review":
		return m.tr("Auto review")
	case "auto_edit":
		return m.tr("Auto edit")
	case "full_access":
		return m.tr("Full access")
	default:
		return m.tr("Normal")
	}
}

// modeChip renders the permission mode as a filled badge. It keeps
// permissionModeLabel's exact text so the chip stays translated.
func (m *model) modeChip() string {
	style := modeChipNormal
	switch m.permissionMode {
	case "plan":
		style = modeChipPlan
	case "always":
		style = modeChipAlways
	}
	return style.Render(m.permissionModeLabel())
}

func (m *model) permissionCommand(args []string) {
	if len(args) == 0 {
		m.cyclePermissionMode()
		return
	}
	if len(args) == 1 {
		switch args[0] {
		case "normal", "plan", "always", "auto_review", "auto_edit", "full_access":
			m.setPermissionMode(args[0])
			return
		}
	}
	m.append(errStyle.Render(m.tr("usage: /permissions [normal|plan|auto_review|auto_edit|full_access|always]")))
}
