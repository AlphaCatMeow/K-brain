package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunRootsExtendOnlyWidensAccess(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	roots := NewRunRoots([]WorkspaceRoot{{Path: a, Access: "write"}, {Path: b, Access: "read"}})

	got := roots.Extend([]WorkspaceRoot{
		{Path: a + string(filepath.Separator), Access: "read"}, // same path: never downgraded
		{Path: b, Access: "write"},                              // upgraded
	})
	if len(got) != 2 || got[0].Access != "write" || got[1].Access != "write" {
		t.Fatalf("roots=%+v", got)
	}
	c := t.TempDir()
	if got = roots.Extend([]WorkspaceRoot{{Path: c, Access: "read"}}); len(got) != 3 || got[2] != (WorkspaceRoot{Path: c, Access: "read"}) {
		t.Fatalf("roots=%+v", got)
	}
}

func TestRunRootsExtensionVisibleToSameRunContext(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	target := filepath.Join(external, "notes.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	holder := NewRunRoots([]WorkspaceRoot{{Path: workspace, Access: "write"}})
	ctx := WithRunRoots(context.Background(), holder)

	_, err := liveagentPath(ctx, target, "read", "Read", false)
	var denial *PathPolicyError
	if !errors.As(err, &denial) || denial.Code != PathOutsideRoots || denial.Path != target || denial.Access != "read" {
		t.Fatalf("err=%v denial=%+v", err, denial)
	}
	// The model-facing text is unchanged by the structured error.
	if err.Error() != "Read.path is outside the authorized workspace roots" {
		t.Fatalf("text=%q", err.Error())
	}

	holder.Extend([]WorkspaceRoot{{Path: external, Access: "read"}})
	if _, err := liveagentPath(ctx, target, "read", "Read", false); err != nil {
		t.Fatalf("read after extend: %v", err)
	}
	_, err = liveagentPath(ctx, target, "write", "Write", false)
	if !errors.As(err, &denial) || denial.Code != PathReadOnlyRoot || err.Error() != "path belongs to a read-only workspace root" {
		t.Fatalf("write err=%v", err)
	}
}

func TestRunRootsConcurrentExtendAndRead(t *testing.T) {
	holder := NewRunRoots(nil)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		dir := t.TempDir()
		go func() { defer wg.Done(); holder.Extend([]WorkspaceRoot{{Path: dir, Access: "read"}}) }()
		go func() { defer wg.Done(); _ = holder.Snapshot() }()
	}
	wg.Wait()
	if got := len(holder.Snapshot()); got != 32 {
		t.Fatalf("roots=%d", got)
	}
}

func TestExecuteResultCarriesPathDenial(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: workspace, Access: "write"}})
	outside := filepath.Join(t.TempDir(), "x.txt")
	tool := Tool{Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		_, err := liveagentPath(ctx, outside, "read", "Read", false)
		return "", err
	}}
	tool.Def.Function.Name = "Probe"
	result := executeResult(ctx, []Tool{tool}, "Probe", nil)
	if !result.Failed || result.Denial == nil || result.Denial.Code != PathOutsideRoots || !strings.Contains(result.Text, "outside the authorized workspace roots") {
		t.Fatalf("result=%+v", result)
	}
}
