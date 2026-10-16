package backend

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/prompts/templates"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

// desktopSlashActions are the commands the desktop composer handles by name. The TUI
// keeps its own, larger registry; terminal-only commands such as /new or /help are not
// offered here because the desktop has buttons for them.
var desktopSlashActions = []protocol.SlashCommand{
	{Name: "goal", Description: "Keep working until the goal is met", ArgumentHint: "<goal> | resume | clear", Kind: "action", Source: "builtin"},
	{Name: "loop", Description: "Run a prompt in this conversation on an interval", ArgumentHint: "<interval> <prompt> | list | cancel <id>", Kind: "action", Source: "builtin"},
	{Name: "plan", Description: "Switch to plan mode, or plan one task", ArgumentHint: "[task]", Kind: "action", Source: "builtin"},
	{Name: "compact", Description: "Compact the conversation context", Kind: "action", Source: "builtin"},
	{Name: "model", Description: "Switch the model", ArgumentHint: "[name]", Kind: "action", Source: "builtin"},
	{Name: "rewind", Description: "Rewind file changes from the latest turn", Kind: "action", Source: "builtin"},
}

// builtinPromptCommands are prompt commands every workspace has. They expand like user
// templates and take precedence over templates with the same name.
var builtinPromptCommands = []templates.Template{
	{
		Name:         "init",
		Description:  "Generate AGENTS.md for this project",
		ArgumentHint: "[notes]",
		Body: "Analyze this repository and create an AGENTS.md file at the project root (update it if one already exists). " +
			"It should help future agents work here: a short project overview, the directory layout, how to build, test and lint, " +
			"coding conventions observed in the code, and any gotchas. Keep it concise and base every statement on files you actually read.\n\n$ARGUMENTS",
	},
	{
		Name:         "review",
		Description:  "Review local code changes",
		ArgumentHint: "[base] [notes]",
		Body: "Review the local code changes in this repository against ${1:-the working tree (staged and unstaged changes)}. " +
			"Collect the diff with git, read the surrounding code, and report concrete bugs, regressions and risky changes first, " +
			"ordered by severity, each with file:line and a short failure scenario. Then list smaller issues. " +
			"Do not modify files unless asked.\n\n${@:2}",
	},
}

var promptCommandPattern = regexp.MustCompile(`(?s)^/([A-Za-z0-9][A-Za-z0-9_-]*)(?:\s+(.*))?$`)

func (rt *runtimeSession) workingDir() string {
	if rt.agent == nil {
		return ""
	}
	return rt.agent.WorkingDir
}

// promptTemplateSources returns the user prompt directory and, for trusted projects,
// the project prompt directory, with the source label of each.
func promptTemplateSources(workdir string) ([]string, map[string]string) {
	root, err := config.Dir()
	if err != nil {
		return nil, nil
	}
	user := filepath.Join(root, "prompts")
	dirs, sources := []string{user}, map[string]string{user: "user"}
	if workdir = strings.TrimSpace(workdir); workdir != "" {
		if canonical, err := resources.CanonicalWorkdir(workdir); err == nil && config.Trusted(canonical) {
			if config.MigrateProjectDir(canonical) == nil {
				project := filepath.Join(config.ProjectDir(canonical), "prompts")
				dirs = append(dirs, project)
				sources[project] = "project"
			}
		}
	}
	return dirs, sources
}

func reservedSlashName(name string) bool {
	for _, action := range desktopSlashActions {
		if strings.EqualFold(action.Name, name) {
			return true
		}
	}
	for _, builtin := range builtinPromptCommands {
		if strings.EqualFold(builtin.Name, name) {
			return true
		}
	}
	return false
}

func (s *Server) handleSlashCommands(w http.ResponseWriter, r *http.Request) {
	commands := append([]protocol.SlashCommand(nil), desktopSlashActions...)
	for _, builtin := range builtinPromptCommands {
		commands = append(commands, protocol.SlashCommand{Name: builtin.Name, Description: builtin.Description, ArgumentHint: builtin.ArgumentHint, Kind: "prompt", Source: "builtin"})
	}
	dirs, sources := promptTemplateSources(r.URL.Query().Get("workdir"))
	items, _ := templates.Load(dirs...)
	for _, item := range items {
		if reservedSlashName(item.Name) {
			continue
		}
		commands = append(commands, protocol.SlashCommand{
			Name: item.Name, Description: item.Description, ArgumentHint: item.ArgumentHint,
			Kind: "prompt", Source: sources[filepath.Dir(item.Path)],
		})
	}
	writeJSON(w, http.StatusOK, protocol.SlashCommandList{Version: protocol.Version, Commands: commands})
}

// expandPromptCommand resolves "/name args" against the built-in prompt commands and
// the Markdown templates visible from workdir. The result is request-only text.
func (s *Server) expandPromptCommand(prompt, workdir string) (string, error) {
	match := promptCommandPattern.FindStringSubmatch(strings.TrimSpace(prompt))
	if match == nil {
		return "", fmt.Errorf("prompt command must look like /name [args]")
	}
	name, args := match[1], strings.TrimSpace(match[2])
	var found *templates.Template
	for i := range builtinPromptCommands {
		if strings.EqualFold(builtinPromptCommands[i].Name, name) {
			found = &builtinPromptCommands[i]
			break
		}
	}
	if found == nil {
		dirs, _ := promptTemplateSources(workdir)
		items, _ := templates.Load(dirs...)
		for i := range items {
			if items[i].Name == name && !reservedSlashName(name) {
				found = &items[i]
				break
			}
		}
	}
	if found == nil {
		return "", fmt.Errorf("prompt command not found: /%s", name)
	}
	expanded, err := found.Expand(args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("<kbrain-prompt-command name=%q>\nThe user invoked the prompt command /%s. Treat the expanded command below as the user's request for this turn.\n\n%s\n</kbrain-prompt-command>", name, name, strings.TrimSpace(expanded)), nil
}
