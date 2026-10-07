package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var updateTerminalAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

type terminalExitError uint32

func (e terminalExitError) Error() string { return fmt.Sprintf("terminal exited with code %d", e) }
func (e terminalExitError) ExitCode() int { return int(e) }

type windowsTerminal struct {
	input, output         *os.File
	console, process, job windows.Handle
	pid                   int
	done                  chan struct{}
	mu                    sync.Mutex
	closed                bool
}

func terminalArgs(shell, command string) []string {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(shell), ".exe"))
	if name == "cmd" {
		if command != "" {
			return []string{"/D", "/S", "/C", command}
		}
		return []string{"/D"}
	}
	if name == "powershell" || name == "pwsh" {
		if command != "" {
			return []string{"-NoLogo", "-NoProfile", "-Command", command}
		}
		return []string{"-NoLogo", "-NoProfile", "-NoExit"}
	}
	if command != "" {
		return []string{"-c", command}
	}
	return []string{"-i"}
}

func startTerminal(ctx context.Context, cmd *exec.Cmd, cols, rows uint16) (terminalProcess, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cols == 0 || rows == 0 || cols > 32767 || rows > 32767 {
		return nil, fmt.Errorf("terminal dimensions must be between 1 and 32767")
	}
	inputR, inputW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outputR, outputW, err := os.Pipe()
	if err != nil {
		inputR.Close()
		inputW.Close()
		return nil, err
	}
	defer inputR.Close()
	defer outputW.Close()
	t := &windowsTerminal{input: inputW, output: outputR, done: make(chan struct{})}
	success := false
	defer func() {
		if !success {
			t.Close()
		}
	}()
	if err = windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, windows.Handle(inputR.Fd()), windows.Handle(outputW.Fd()), 0, &t.console); err != nil {
		return nil, fmt.Errorf("ConPTY requires Windows 10 1809 or later: %w", err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// PSEUDOCONSOLE takes the handle value, not a pointer to a handle.
	result, _, attrErr := updateTerminalAttribute.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(t.console), unsafe.Sizeof(t.console), 0, 0)
	runtime.KeepAlive(attrs)
	if result == 0 {
		return nil, attrErr
	}
	// Prevent inheriting the parent's console handles instead of ConPTY's streams.
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES}, ProcThreadAttributeList: attrs.List()}
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	commandLine := windows.ComposeCommandLine(cmd.Args)
	if strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") && len(cmd.Args) == 5 && cmd.Args[3] == "/C" {
		commandLine = windows.EscapeArg(cmd.Path) + ` /D /S /C "` + cmd.Args[4] + `"`
	}
	line, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, err
	}
	dir, err := windows.UTF16PtrFromString(cmd.Dir)
	if err != nil {
		return nil, err
	}
	var environment []uint16
	for _, entry := range cmd.Environ() {
		v, e := windows.UTF16FromString(entry)
		if e != nil {
			return nil, e
		}
		environment = append(environment, v...)
	}
	environment = append(environment, 0)
	if len(environment) == 1 {
		environment = append(environment, 0)
	}
	t.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	var info windows.ProcessInformation
	err = windows.CreateProcess(app, line, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED, &environment[0], dir, &si.StartupInfo, &info)
	if err != nil {
		return nil, err
	}
	t.process = info.Process
	t.pid = int(info.ProcessId)
	defer windows.CloseHandle(info.Thread)
	if err = windows.AssignProcessToJobObject(t.job, t.process); err != nil {
		windows.TerminateProcess(t.process, 1)
		return nil, err
	}
	if _, err = windows.ResumeThread(info.Thread); err != nil {
		return nil, err
	}
	success = true
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Kill()
		case <-t.done:
		}
	}()
	return t, nil
}

func (t *windowsTerminal) Read(p []byte) (int, error)  { return t.output.Read(p) }
func (t *windowsTerminal) Write(p []byte) (int, error) { return t.input.Write(p) }
func (t *windowsTerminal) PID() int                    { return t.pid }
func (t *windowsTerminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	if cols == 0 || rows == 0 || cols > 32767 || rows > 32767 {
		return fmt.Errorf("terminal dimensions must be between 1 and 32767")
	}
	return windows.ResizePseudoConsole(t.console, windows.Coord{X: int16(cols), Y: int16(rows)})
}
func (t *windowsTerminal) Wait() error {
	defer close(t.done)
	if _, err := windows.WaitForSingleObject(t.process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(t.process, &code); err != nil {
		return err
	}
	if code != 0 {
		return terminalExitError(code)
	}
	return nil
}
func (t *windowsTerminal) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}
func (t *windowsTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.job != 0 {
		windows.CloseHandle(t.job)
	}
	// Closing the pipes before ConPTY avoids waiting for an undrained output pipe.
	err := errors.Join(t.input.Close(), t.output.Close())
	if t.console != 0 {
		windows.ClosePseudoConsole(t.console)
	}
	if t.process != 0 {
		windows.CloseHandle(t.process)
	}
	return err
}
