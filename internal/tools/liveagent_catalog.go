package tools

// LiveAgentToolMetadata mirrors the original builtinToolCatalog entries needed
// by provider/UI policy code without importing LiveAgent's TypeScript bundle.
type LiveAgentToolMetadata struct {
	ID            string   `json:"id"`
	Icon          string   `json:"icon"`
	ToolName      string   `json:"toolName"`
	Category      string   `json:"categoryId"`
	ReadOnly      bool     `json:"isReadOnly"`
	RuntimeScope  []string `json:"runtimeScopes"`
	DefaultPolicy string   `json:"defaultPolicy"`
}

func LiveAgentToolCatalogMetadata() []LiveAgentToolMetadata {
	return []LiveAgentToolMetadata{
		{ID: "terminalsession", Icon: "terminal", ToolName: "TerminalSession", Category: "process", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "readterminal", Icon: "terminal", ToolName: "ReadTerminal", Category: "process", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "read", Icon: "fileText", ToolName: "Read", Category: "fs", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "image", Icon: "image", ToolName: "Image", Category: "fs", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "write", Icon: "filePen", ToolName: "Write", Category: "fs", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "edit", Icon: "pencil", ToolName: "Edit", Category: "fs", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "delete", Icon: "trash", ToolName: "Delete", Category: "fs", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "list", Icon: "list", ToolName: "List", Category: "fs", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "glob", Icon: "folderTree", ToolName: "Glob", Category: "fs", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "grep", Icon: "search", ToolName: "Grep", Category: "fs", ReadOnly: true, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "bash", Icon: "terminal", ToolName: "Bash", Category: "process", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "managedprocess", Icon: "terminal", ToolName: "ManagedProcess", Category: "process", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "processwait", Icon: "pause", ToolName: "ProcessWait", Category: "process", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
		{ID: "processstop", Icon: "square", ToolName: "ProcessStop", Category: "process", ReadOnly: false, RuntimeScope: []string{"chat", "cron_auto_prompt"}, DefaultPolicy: "allow"},
	}
}
