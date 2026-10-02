package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveAgentManagedCommandForegroundOperators(t *testing.T) {
	for _, command := range []string{
		"printf first && printf second",
		"printf first || printf second",
		"printf first 2>&1",
		"printf first >&2",
		"cat <&0",
		"printf first &>output.log",
		`printf '%s' 'a&b'`,
		`printf a\&b`,
		"printf first # background & in comment",
	} {
		t.Run(command, func(t *testing.T) {
			if err := liveagentValidateManagedCommand(command); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, command := range []string{"sleep 1 &", "sleep 1 >out 2>&1 &", "true && sleep 1 &", "printf foo#bar &"} {
		if err := liveagentValidateManagedCommand(command); err == nil {
			t.Errorf("accepted background command %q", command)
		}
	}
}

func TestLiveAgentManagedProcessCompoundCommand(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}}))
	defer cancel()
	catalog := LiveAgentCatalog()
	tool := findToolForTest(catalog, "ManagedProcess")
	out, err := tool.Run(ctx, mustJSON(map[string]any{"action": "start", "command": "printf first && printf second >&2"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(fieldValue(out, "log")) })
	id := fieldValue(out, "process_id")
	var output strings.Builder
	cursor := int64(0)
	for i := 0; i < 20; i++ {
		result, err := tool.Run(ctx, mustJSON(map[string]any{"action": "wait", "process_id": id, "cursor": cursor, "yield_time_ms": 1000}))
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(result, "\n\n", 2)
		if len(parts) == 2 {
			text := strings.SplitN(parts[1], "\nContinue with", 2)[0]
			output.WriteString(text)
			cursor += int64(len(text))
		}
		if fieldValue(result, "status") == "completed" {
			if output.String() != "firstsecond" {
				t.Fatalf("output=%q", output.String())
			}
			return
		}
	}
	t.Fatal("compound command did not finish")
}

func TestLiveAgentBashYieldKeepsBashPermission(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(WithWorkingDir(context.Background(), root))
	defer cancel()
	calls := 0
	ctx = WithGate(ctx, func(req GateRequest) (GateDecision, string) {
		calls++
		if req.Tool != "Bash" {
			t.Errorf("Bash yielded using a different permission: %+v", req)
			return GateReject, "unexpected process permission"
		}
		return GateAllowOnce, ""
	})
	tool := findToolForTest(LiveAgentCatalog(), "Bash")
	out, err := tool.Run(ctx, mustJSON(map[string]any{"command": "printf ready", "yield_time_ms": 1000}))
	if err != nil || calls != 1 || !strings.Contains(out, "ready") {
		t.Fatalf("calls=%d output=%q err=%v", calls, out, err)
	}
	t.Cleanup(func() { _ = os.Remove(fieldValue(out, "log")) })
}

func TestLiveAgentProcessManagerReusedAcrossCancelledRuns(t *testing.T) {
	manager := newLiveAgentProcessManager()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		p, err := manager.start(ctx, "sleep 30", t.TempDir(), "", 0, false)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(p.log.Name()) })
		cancel()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_, _, _ = manager.stop(p.id)
			t.Fatal("cancelled run retained its managed process")
		}
		if p.snapshot().Status != liveagentProcessCancelled {
			t.Fatalf("status=%s", p.snapshot().Status)
		}
	}
}

func TestLiveAgentProcessLogReadIsBounded(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "process-log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const size = int64(1 << 30)
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("final"), size-5); err != nil {
		t.Fatal(err)
	}
	p := &liveagentProcess{log: file}
	manager := newLiveAgentProcessManager()
	out, next, truncated, n, err := manager.read(p, size-5, 3)
	if err != nil || out != "fin" || next != size-2 || !truncated || n != 3 {
		t.Fatalf("read=%q cursor=%d truncated=%t bytes=%d err=%v", out, next, truncated, n, err)
	}
	out, next, truncated, n, err = manager.read(p, next, 3)
	if err != nil || out != "al" || next != size || truncated || n != 2 {
		t.Fatalf("read=%q cursor=%d truncated=%t bytes=%d err=%v", out, next, truncated, n, err)
	}
	out, next, truncated, n, err = manager.read(p, size+100, 3)
	if err != nil || out != "" || next != size || truncated || n != 0 {
		t.Fatalf("past end=%q cursor=%d truncated=%t bytes=%d err=%v", out, next, truncated, n, err)
	}
}
