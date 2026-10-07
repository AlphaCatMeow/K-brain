// Package cua connects K-brain to Cua Driver's native MCP interface.
package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Independent transport sessions do not isolate desktop focus or snapshot caches.
var desktopOwner atomic.Pointer[Client]

type Client struct {
	command      []string
	dir          string
	session      *mcp.ClientSession
	gate         chan struct{}
	tools        map[string]*mcp.Tool
	described    map[string]bool
	ordered      []*mcp.Tool
	instructions string
	closed       bool
	uncertain    bool
	connect      func(context.Context) (mcp.Transport, error)
}

func New(command []string, dir string) *Client {
	return &Client{command: append([]string(nil), command...), dir: dir, gate: make(chan struct{}, 1), described: map[string]bool{}}
}

func ResolveCommand(command []string) ([]string, error) {
	if len(command) > 0 {
		if command[0] == "" {
			return nil, errors.New("computer.command executable is empty")
		}
		path, err := exec.LookPath(command[0])
		if err != nil {
			return nil, fmt.Errorf("computer.command: %w", err)
		}
		return append([]string{path}, command[1:]...), nil
	}
	if path, err := exec.LookPath("cua-driver"); err == nil {
		return []string{path, "mcp"}, nil
	}
	home, _ := os.UserHomeDir()
	name := "cua-driver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	paths := []string{filepath.Join(home, ".local", "bin", name)}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			paths = append(paths, filepath.Join(local, "Programs", "Cua", "cua-driver", "bin", name))
		}
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, "/Applications/CuaDriver.app/Contents/MacOS/cua-driver")
	}
	for _, path := range paths {
		if found, err := exec.LookPath(path); err == nil {
			return []string{found, "mcp"}, nil
		}
	}
	return nil, errors.New("Cua Driver is not installed: install cua-driver or set computer.command to its executable and mcp arguments; no legacy fallback was used")
}

func (c *Client) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) unlock() { <-c.gate }

func (c *Client) start(ctx context.Context) error {
	if c.closed || c.uncertain {
		return errors.New("computer runtime is closed or an action outcome is uncertain; do not replay actions")
	}
	if c.session != nil {
		return nil
	}
	if !desktopOwner.CompareAndSwap(nil, c) {
		return errors.New("computer use is busy in another K-brain turn; separate sessions do not isolate the desktop")
	}
	defer func() {
		if c.session == nil {
			desktopOwner.CompareAndSwap(c, nil)
		}
	}()
	var transport mcp.Transport
	var err error
	if c.connect != nil {
		transport, err = c.connect(ctx)
	} else {
		var argv []string
		argv, err = ResolveCommand(c.command)
		if err == nil {
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir, cmd.Stderr = c.dir, os.Stderr
			transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
		}
	}
	if err != nil {
		return err
	}
	// Standard Cua MCP does not provide the embedding SDK's authorization host.
	client := mcp.NewClient(&mcp.Implementation{Name: "k-brain-computer", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "cancel"}, nil
		},
	})
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := client.Connect(startup, transport, nil)
	if err != nil {
		return fmt.Errorf("Cua MCP initialization failed (on macOS, check the signed CuaDriver service and its permissions): %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = session.Close()
		}
	}()
	tools := map[string]*mcp.Tool{}
	var ordered []*mcp.Tool
	cursor := ""
	for page := 0; ; page++ {
		if page >= 100 {
			return errors.New("Cua tools/list exceeded pagination limit")
		}
		listed, err := session.ListTools(startup, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return err
		}
		for _, tool := range listed.Tools {
			if tool == nil || tool.Name == "" || tools[tool.Name] != nil {
				return errors.New("Cua returned an empty or duplicate tool name")
			}
			tools[tool.Name] = tool
			if !hostTool(tool.Name) {
				ordered = append(ordered, tool)
			}
		}
		if listed.NextCursor == "" {
			break
		}
		cursor = listed.NextCursor
	}
	for _, required := range []string{"get_window_state", "end_session"} {
		if tools[required] == nil {
			return fmt.Errorf("Cua runtime lacks required tool %q", required)
		}
	}
	c.session, c.tools, c.ordered = session, tools, ordered
	c.instructions = session.InitializeResult().Instructions
	ok = true
	return nil
}

func hostTool(name string) bool {
	switch name {
	case "start_session", "end_session", "list_sessions", "get_session", "escalate_session", "get_session_state":
		return true
	default:
		return false
	}
}

func (c *Client) Discover(ctx context.Context) (string, error) {
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.unlock()
	if err := c.start(ctx); err != nil {
		return "", err
	}
	type summary struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	list := make([]summary, 0, len(c.ordered))
	for _, tool := range c.ordered {
		list = append(list, summary{tool.Name, strings.SplitN(tool.Description, "\n", 2)[0]})
	}
	data, err := json.Marshal(struct {
		Instructions string    `json:"instructions"`
		Tools        []summary `json:"tools"`
		Next         string    `json:"next"`
	}{c.instructions, list, "Use action=describe for the exact tool schema before action=call. Use action=guide to read bundled Cua documentation. K-brain owns the implicit session; omit session and all private fields."})
	return string(data), err
}

func (c *Client) Describe(ctx context.Context, name string) (string, error) {
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.unlock()
	if c.closed || c.uncertain || c.session == nil {
		return "", errors.New("discover the Cua runtime in this turn first; uncertain runtimes cannot be reused")
	}
	tool := c.tools[name]
	if tool == nil || hostTool(name) {
		return "", fmt.Errorf("Cua tool %q is unavailable or reserved for host lifecycle", name)
	}
	data, err := json.Marshal(tool)
	if err == nil {
		c.described[name] = true
	}
	return string(data), err
}

// Guide reads only bundled skill resources, never arbitrary server resources.
func (c *Client) Guide(ctx context.Context, file string) (string, error) {
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.unlock()
	if c.closed || c.uncertain || c.session == nil {
		return "", errors.New("discover the Cua runtime first")
	}
	switch file {
	case "SKILL.md", "README.md", "WORKFLOW.md", "RUNTIME.md", "MACOS.md", "WINDOWS.md", "LINUX.md", "BROWSER.md", "RECORDING.md", "EMBEDDING.md":
	default:
		return "", errors.New("unknown bundled Cua guide")
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := c.session.ReadResource(readCtx, &mcp.ReadResourceParams{URI: "skill://cua-driver/" + file})
	if err != nil {
		return "", fmt.Errorf("installed Cua runtime does not provide this guide: %w", err)
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed || c.uncertain {
		return nil, errors.New("Cua action outcome is uncertain or runtime is closed; do not replay the action")
	}
	if c.session == nil || !c.described[name] || hostTool(name) {
		return nil, errors.New("discover then describe an advertised Cua tool before calling it")
	}
	var arguments map[string]any
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	if err := decoder.Decode(&arguments); err != nil || arguments == nil || !json.Valid(args) {
		return nil, errors.New("arguments must be a JSON object")
	}
	if err := validateArguments(arguments); err != nil {
		return nil, err
	}
	if name == "run_actions" {
		if err := c.validateSteps(arguments); err != nil {
			return nil, err
		}
	}
	timeout := 120 * time.Second
	if value, ok := arguments["timeout_ms"].(json.Number); ok {
		ms, err := value.Int64()
		if err != nil || ms < 0 || ms > 86400000 {
			return nil, errors.New("timeout_ms must be an integer between zero and 86400000")
		}
		if extended := time.Duration(ms)*time.Millisecond + 30*time.Second; extended > timeout {
			timeout = extended
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := c.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		c.uncertain = true
		return nil, fmt.Errorf("Cua action failed; outcome may be uncertain, do not automatically retry: %w", err)
	}
	return result, nil
}

func validateArguments(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if key == "session" || strings.HasPrefix(key, "_") {
				return fmt.Errorf("Cua field %q is reserved for the host/transport", key)
			}
			if err := validateArguments(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := validateArguments(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) validateSteps(arguments map[string]any) error {
	steps, _ := arguments["steps"].([]any)
	for _, value := range steps {
		step, _ := value.(map[string]any)
		name, _ := step["tool"].(string)
		if name == "run_actions" || hostTool(name) || !c.described[name] {
			return fmt.Errorf("batch tool %q must be described and cannot be a lifecycle or nested batch tool", name)
		}
	}
	return nil
}

func (c *Client) Close(ctx context.Context) (closeErr error) {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	c.closed = true
	defer desktopOwner.CompareAndSwap(c, nil)
	if c.session == nil {
		return nil
	}
	session := c.session
	c.session = nil
	defer func() { closeErr = errors.Join(closeErr, session.Close()) }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "end_session", Arguments: map[string]any{}})
	if err != nil {
		return fmt.Errorf("Cua end_session cleanup failed: %w", err)
	}
	if result.IsError {
		return errors.New("Cua end_session cleanup returned isError=true")
	}
	return nil
}
