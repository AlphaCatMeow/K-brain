package bashrun

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDefaultShellPriority(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		env            map[string]string
		installed      []string
		want           string
	}{
		{"unix bash first", "darwin", map[string]string{"SHELL": "/bin/zsh"}, []string{"bash", "/bin/zsh"}, "bash"},
		{"unix GUI path", "linux", map[string]string{"SHELL": "/bin/zsh"}, []string{"/bin/bash", "/bin/zsh"}, "/bin/bash"},
		{"unix fallback", "linux", map[string]string{"SHELL": "/bin/zsh"}, []string{"/bin/zsh", "sh"}, "/bin/zsh"},
		{"unix minimal", "linux", nil, []string{"sh"}, "sh"},
		{"windows ignores SHELL", "windows", map[string]string{"SHELL": "bash"}, []string{"bash", "pwsh.exe", "powershell.exe"}, "pwsh.exe"},
		{"windows installed outside PATH", "windows", map[string]string{"ProgramFiles": "programs"}, []string{filepath.Join("programs", "PowerShell", "7", "pwsh.exe"), "powershell.exe"}, filepath.Join("programs", "PowerShell", "7", "pwsh.exe")},
		{"windows legacy", "windows", nil, []string{"powershell.exe", "cmd.exe"}, "powershell.exe"},
		{"windows Git Bash fallback", "windows", map[string]string{"ProgramFiles": "programs"}, []string{filepath.Join("programs", "Git", "bin", "bash.exe"), "powershell.exe"}, filepath.Join("programs", "Git", "bin", "bash.exe")},
		{"windows pwsh before Git Bash", "windows", map[string]string{"ProgramFiles": "programs"}, []string{"pwsh.exe", filepath.Join("programs", "Git", "bin", "bash.exe")}, "pwsh.exe"},
		{"windows WSL is not Git Bash", "windows", nil, []string{"bash.exe", "powershell.exe"}, "powershell.exe"},
		{"windows minimal", "windows", nil, []string{"cmd.exe"}, "cmd.exe"},
		{"explicit override", "windows", map[string]string{"K_BRAIN_SHELL": "custom-shell"}, []string{"pwsh.exe"}, "custom-shell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(s string) (string, error) {
				for _, installed := range tc.installed {
					if s == installed {
						return s, nil
					}
				}
				return "", errors.New("not installed")
			}
			got := resolveDefaultShell(tc.platform, func(s string) string { return tc.env[s] }, lookup)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
