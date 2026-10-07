package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func liveagentShellDefinition(def ai.Tool) ai.Tool {
	var schema map[string]any
	_ = json.Unmarshal(def.Function.Parameters, &schema)
	schema["properties"].(map[string]any)["shell"] = map[string]any{
		"type": "string", "description": "Optional shell executable name or full path without arguments: bash, pwsh, powershell, cmd or wsl. Overrides the default; match command syntax to this shell.",
	}
	def.Function.Parameters, _ = json.Marshal(schema)
	def.Function.Description += " Default shell: " + bashrun.DefaultShell() + ". Prefer this command tool over interactive TerminalSession for non-interactive work. macOS/Linux prefer bash; Windows prefers pwsh, then Git Bash, then Windows PowerShell, then cmd. In PowerShell use native PowerShell syntax and & to invoke quoted executable paths; in Git Bash use Bash syntax. Use shell to override."
	return def
}

func liveagentValidateShellBackground(ctx context.Context, command string, managed bool) error {
	switch bashrun.ShellName(bashrun.ResolveShell(ctx, "")) {
	case "powershell", "pwsh", "powershell7":
		return liveagentValidatePowerShellBackground(command)
	case "cmd":
		// CMD's & separates foreground commands; it is not a POSIX background operator.
		return nil
	default:
		if managed {
			return liveagentValidateManagedCommand(command)
		}
		return liveagentValidateBackground(command)
	}
}

func liveagentValidatePowerShellBackground(command string) error {
	var quote, previous byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		if c == '`' && quote != '\'' {
			i++
			continue
		}
		if quote != 0 {
			if c == quote {
				if i+1 < len(command) && command[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '#' {
			for i+1 < len(command) && command[i+1] != '\n' {
				i++
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote, previous = c, c
			continue
		}
		if c == '&' {
			if i+1 < len(command) && command[i+1] == '&' {
				i++
			} else if previous != 0 && !strings.ContainsRune(";\n|=({,>", rune(previous)) {
				return errors.New("PowerShell background jobs are not supported here; use a foreground command with Bash.yield_time_ms or ManagedProcess")
			}
		}
		if c != ' ' && c != '\t' && c != '\r' {
			previous = c
		}
	}
	return nil
}

// Shell pipe inheritance must be detached before background execution.
func liveagentValidateBackground(command string) error {
	var quote byte
	escaped, stdout, stderr := false, false, false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '#' {
			for i+1 < len(command) && command[i+1] != '\n' {
				i++
			}
			continue
		}
		if c == ';' || c == '\n' {
			stdout = false
			stderr = false
			continue
		}
		if c == '&' {
			if i+1 < len(command) && command[i+1] == '&' {
				i++
				stdout = false
				stderr = false
				continue
			}
			if i+1 < len(command) && command[i+1] == '>' {
				i++
				stdout = true
				stderr = true
				continue
			}
			if i > 0 && command[i-1] == '>' {
				continue
			}
			if !stdout || !stderr {
				return errors.New("background Bash commands must detach stdout and stderr before using &: redirect both streams to a log file")
			}
			stdout = false
			stderr = false
		}
		if c == '|' {
			stdout = false
			stderr = false
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
		}
		if c == '>' {
			if i > 0 && command[i-1] == '>' {
				continue
			}
			if i > 0 && command[i-1] == '2' {
				stderr = true
			} else {
				stdout = true
			}
		}
	}
	return nil
}
