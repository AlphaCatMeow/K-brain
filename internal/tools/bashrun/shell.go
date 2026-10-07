package bashrun

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

type shellKey struct{}

func WithShell(ctx context.Context, shell string) context.Context {
	return context.WithValue(ctx, shellKey{}, shell)
}

func DefaultShell() string { return userShell() }

func resolveDefaultShell(platform string, getenv func(string) string, lookup func(string) (string, error)) string {
	if override := getenv("K_BRAIN_SHELL"); override != "" {
		return override
	}
	candidates := []string{"bash", "/bin/bash", "/usr/bin/bash", getenv("SHELL"), "sh"}
	fallback := "bash"
	if platform == "windows" {
		candidates = []string{"pwsh.exe"}
		for _, root := range []string{getenv("ProgramW6432"), getenv("ProgramFiles"), getenv("ProgramFiles(x86)")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
			}
		}
		candidates = append(candidates, windowsGitBashCandidates(getenv, lookup)...)
		candidates = append(candidates, "powershell.exe")
		if root := getenv("SystemRoot"); root != "" {
			candidates = append(candidates, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
		}
		candidates = append(candidates, getenv("ComSpec"), "cmd.exe")
		fallback = "powershell.exe"
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := lookup(candidate); err == nil {
			return candidate
		}
	}
	return fallback
}

func windowsGitBashCandidates(getenv func(string) string, lookup func(string) (string, error)) []string {
	var candidates []string
	for _, root := range []string{getenv("ProgramW6432"), getenv("ProgramFiles"), getenv("ProgramFiles(x86)")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "Git", "bin", "bash.exe"))
		}
	}
	if root := getenv("LOCALAPPDATA"); root != "" {
		candidates = append(candidates, filepath.Join(root, "Programs", "Git", "bin", "bash.exe"))
	}
	if git, err := lookup("git.exe"); err == nil && filepath.IsAbs(git) {
		root := filepath.Dir(filepath.Dir(git))
		candidates = append(candidates, filepath.Join(root, "bin", "bash.exe"), filepath.Join(root, "usr", "bin", "bash.exe"))
	}
	// Bare bash.exe can be the legacy WSL launcher, not Git Bash.
	return candidates
}

// ResolveShell keeps explicit per-tool selection ahead of the session and platform default.
func ResolveShell(ctx context.Context, shell string) string {
	if shell == "" {
		shell, _ = ctx.Value(shellKey{}).(string)
	}
	if shell == "" {
		shell = DefaultShell()
	}
	return shell
}

// Command shares quoting, encoding and exit status handling across process tools.
func Command(ctx context.Context, shell, command string) (*exec.Cmd, error) {
	return shellCommand(ctx, ResolveShell(ctx, shell), command)
}

func ShellName(shell string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
}

func shellCommand(ctx context.Context, shell, command string) (*exec.Cmd, error) {
	if shell == "" {
		shell = userShell()
	}
	name := ShellName(shell)
	var args []string
	switch name {
	case "powershell7":
		shell = "pwsh"
		fallthrough
	case "powershell", "pwsh":

		script := "$ProgressPreference = 'SilentlyContinue'; [Console]::InputEncoding = [Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new(); " +
			"$ErrorActionPreference = 'Stop'; & {\n" + command + "\n}; " +
			"if (-not $?) { exit 1 }; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }"
		words := utf16.Encode([]rune(script))
		data := make([]byte, len(words)*2)
		for i, word := range words {
			binary.LittleEndian.PutUint16(data[i*2:], word)
		}
		args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}
	case "cmd":
		args = []string{"/D", "/S", "/C", command}
	case "wsl":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("shell wsl requires Windows")
		}

		args = []string{"--exec", "bash", "-c", command}
	case "bash":

		if runtime.GOOS == "windows" && filepath.Base(shell) == shell {
			for _, candidate := range windowsGitBashCandidates(os.Getenv, exec.LookPath) {
				if _, err := exec.LookPath(candidate); err == nil {
					shell = candidate
					break
				}
			}
		}
		args = []string{"-c", command}
	default:
		args = []string{"-c", command}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	configureShellCommand(cmd, name, command)
	return cmd, nil
}
