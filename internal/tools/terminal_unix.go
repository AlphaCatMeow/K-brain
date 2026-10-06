//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/creack/pty"
)

type unixTerminal struct {
	*os.File
	cmd *exec.Cmd
	mu  sync.Mutex
}

func terminalArgs(_ string, command string) []string {
	if command != "" {
		return []string{"-c", command}
	}
	return []string{"-i"}
}
func startTerminal(_ context.Context, cmd *exec.Cmd, cols, rows uint16) (terminalProcess, error) {
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &unixTerminal{File: file, cmd: cmd}, nil
}
func (t *unixTerminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return pty.Setsize(t.File, &pty.Winsize{Cols: cols, Rows: rows})
}
func (t *unixTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.File.Close()
}
func (t *unixTerminal) Wait() error { return t.cmd.Wait() }
func (t *unixTerminal) Kill() error { return process.Kill(t.cmd) }
func (t *unixTerminal) PID() int    { return t.cmd.Process.Pid }
