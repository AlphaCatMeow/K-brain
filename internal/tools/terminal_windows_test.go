package tools

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestConPTYCommandExitAndEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd.exe", terminalArgs("cmd.exe", `echo "%TERMINAL_TEST_VALUE%" & exit /b 7`)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "TERMINAL_TEST_VALUE=terminal value")
	term, err := startTerminal(ctx, cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	output := make(chan string, 1)
	go func() { b, _ := io.ReadAll(term); output <- string(b) }()
	err = term.Wait()
	if exit, ok := err.(interface{ ExitCode() int }); !ok || exit.ExitCode() != 7 {
		t.Fatalf("exit=%v", err)
	}
	term.Close()
	if text := <-output; !strings.Contains(text, `"terminal value"`) {
		t.Fatalf("output=%q", text)
	}
}

func TestConPTYInteractiveResizeAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd.exe", terminalArgs("cmd.exe", "")...)
	cmd.Dir = t.TempDir()
	term, err := startTerminal(ctx, cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(65535, 40); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
	seen := make(chan struct{}, 1)
	go func() {
		var text string
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			text += string(buf[:n])
			if strings.Contains(text, "interactive-marker") {
				select {
				case seen <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	if _, err := term.Write([]byte("echo interactive-marker\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal("interactive output missing")
	}
	cancel()
	finished := make(chan error, 1)
	go func() { finished <- term.Wait() }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop ConPTY")
	}
}
