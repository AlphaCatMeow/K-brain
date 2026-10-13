package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestSubagentWorktreeWorkspaceRoots(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	ctx := tools.WithWorkspaceRoots(context.Background(), []tools.WorkspaceRoot{{Path: repo, Access: "write"}})
	parent := New(ai.New("http://unused", "key"), "fixture", 1024, "fixture")
	parent.WorkingDir = repo
	parent.Tools = tools.LiveAgentCatalog()
	wt, err := provisionSubagentWorktreeAt(ctx, repo, "audit")
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt).Run()
	if err := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := parent.newSubContext(ctx, SubModel{})
	ctx = sub.setWorktree(ctx, wt)
	result := tools.ExecuteResult(tools.WithWorkingDir(ctx, sub.WorkingDir), sub.AllTools(), "Read", json.RawMessage(`{"path":"hello.txt"}`), false)
	if result.Failed {
		t.Fatalf("subagent cannot read its own worktree: %s", result.Text)
	}
}
