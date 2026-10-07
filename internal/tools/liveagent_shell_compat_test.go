package tools

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func TestLiveAgentShellBackgroundDialects(t *testing.T) {
	for _, tc := range []struct {
		shell, command string
		reject         bool
	}{
		{"pwsh", `& 'C:\Program Files\tool.exe' '中文'`, false},
		{"powershell", `$x = & 'C:\tool.exe'; Write-Output $x`, false},
		{"pwsh", `Write-Output 'a & b'; & { Write-Output ok }`, false},
		{"pwsh", "Write-Output first\n& 'C:\\tool.exe'", false},
		{"pwsh", `Write-Output first && Write-Output second`, false},
		{"pwsh", `Write-Output ok &`, true},
		{"pwsh", `Write-Output ok & Write-Output next`, true},
		{"powershell", "Write-Output 'it''s & quoted' # & comment", false},
		{"cmd", `echo first & echo second`, false},
		{"bash", `sleep 10 &`, true},
		{"bash", `echo first && echo second`, false},
	} {
		for _, managed := range []bool{false, true} {
			err := liveagentValidateShellBackground(bashrun.WithShell(t.Context(), tc.shell), tc.command, managed)
			if (err != nil) != tc.reject {
				t.Errorf("%s managed=%t command=%q err=%v", tc.shell, managed, tc.command, err)
			}
		}
	}
}

func TestLiveAgentSharedShellCommand(t *testing.T) {
	for _, shell := range []string{"pwsh", "powershell7", "cmd", "bash"} {
		command := `echo '中文 & "quotes"'; exit 7`
		ctx := bashrun.WithShell(t.Context(), shell)
		got, err := liveagentShellCommand(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		want, err := bashrun.Command(ctx, shell, command)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Args, want.Args) || !reflect.DeepEqual(got.SysProcAttr, want.SysProcAttr) {
			t.Fatalf("%s synchronous and managed launch differ", shell)
		}
	}
}

func TestLiveAgentShellOverrideSyncYieldManaged(t *testing.T) {
	shell, command := "bash", `printf 'override-ok 中文'; exit 7`
	if runtime.GOOS == "windows" {
		shell, command = "powershell", `[Console]::Write('override-ok 中文'); exit 7`
	}
	t.Setenv("K_BRAIN_SHELL", "missing-default-shell")
	manager := NewManagedProcessManager()
	t.Cleanup(manager.CloseAll)
	terminal := NewTerminalManager()
	t.Cleanup(terminal.CloseAll)
	catalog := LiveAgentCatalogWithManagers(nil, terminal, manager)
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(t.Context(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	for _, name := range []string{"Bash", "ManagedProcess"} {
		tool := findToolForTest(catalog, name)
		if !strings.Contains(tool.Def.Function.Description, "Default shell: missing-default-shell") {
			t.Fatal("model has no shell context")
		}
		for _, yield := range []bool{false, true} {
			args := map[string]any{"command": command, "shell": shell}
			if name == "ManagedProcess" {
				args["action"] = "start"
			}
			if name == "Bash" && yield {
				args["yield_time_ms"] = 1
			}
			raw, _ := json.Marshal(args)
			out, err := tool.Run(ctx, raw)
			if name == "Bash" && !yield {
				if err == nil || !strings.Contains(err.Error(), "7") || !strings.Contains(out, "override-ok 中文") {
					t.Fatalf("sync output=%q err=%v", out, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			id := fieldValue(out, "process_id")
			result, err := findToolForTest(catalog, "ProcessWait").Run(ctx, mustJSON(map[string]any{"session_id": id, "yield_time_ms": 10000, "cursor": 0}))
			if err != nil || !strings.Contains(result, "exit_code=7") || !strings.Contains(result, "override-ok 中文") {
				t.Fatalf("%s output=%q err=%v", name, result, err)
			}
		}
	}
}
