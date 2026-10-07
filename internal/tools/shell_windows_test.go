package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsLiveAgentPowerShellInvocationAndExit(t *testing.T) {
	for _, shell := range []string{"pwsh", "powershell", "powershell7"} {
		executable := shell
		if shell == "powershell7" {
			executable = "pwsh"
		}
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s unavailable", executable)
		}
		root := t.TempDir()
		script := filepath.Join(root, "script with spaces 中文.ps1")
		if err := os.WriteFile(script, []byte("\xef\xbb\xbf[Console]::Write('unicode 中文'); exit 7"), 0600); err != nil {
			t.Fatal(err)
		}
		command := "& '" + strings.ReplaceAll(script, "'", "''") + "'"
		manager := NewManagedProcessManager()
		t.Cleanup(manager.CloseAll)
		terminal := NewTerminalManager()
		t.Cleanup(terminal.CloseAll)
		catalog := LiveAgentCatalogWithManagers(nil, terminal, manager)
		ctx := WithWorkspaceRoots(WithWorkingDir(t.Context(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
		for _, yield := range []bool{false, true} {
			args := map[string]any{"command": command, "shell": shell}
			if yield {
				args["yield_time_ms"] = 1
			}
			out, err := findToolForTest(catalog, "Bash").Run(ctx, mustJSON(args))
			if !yield {
				if err == nil || !strings.Contains(err.Error(), "7") || !strings.Contains(out, "unicode 中文") {
					t.Fatalf("%s sync: %q %v", shell, out, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				waitProcessExitForTest(t, manager, fieldValue(out, "process_id"))
				out, err = findToolForTest(catalog, "ProcessWait").Run(ctx, mustJSON(map[string]any{"session_id": fieldValue(out, "process_id"), "yield_time_ms": 10000, "cursor": 0}))
				if err != nil || !strings.Contains(out, "exit_code=7") || !strings.Contains(out, "unicode 中文") {
					t.Fatalf("%s yield: %q %v", shell, out, err)
				}
			}
		}
	}
}

func TestWindowsToolShellSelection(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "missing-shell")
	args := json.RawMessage(`{"shell":"powershell", "command":"Write-Output 'selected-powershell'"}`)
	out, err := bashTool().Run(t.Context(), args)
	if err != nil || !strings.Contains(out, "selected-powershell") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestWindowsToolInteractiveError(t *testing.T) {
	_, err := bashTool().Run(t.Context(), json.RawMessage(`{"shell":"powershell", "command":"Read-Host", "interactive":true}`))
	if err == nil || !strings.Contains(err.Error(), "PTY") {
		t.Fatalf("err=%v", err)
	}
}
