package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestSubagentWorktreeFollowupWorkspaceRoots(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	ctx := tools.WithWorkspaceRoots(context.Background(), []tools.WorkspaceRoot{{Path: repo, Access: "write"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" || strings.HasPrefix(last.Content, "Work entirely inside") {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"write","type":"function","function":{"name":"Write","arguments":"{\"path\":\"followup.txt\",\"content\":\"written\"}"}}]}}]}`, `"id":"write"`, fmt.Sprintf(`"id":"write-%d"`, len(req.Messages)))+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	parent := New(ai.New(srv.URL, "key"), "fixture", 1024, "fixture")
	parent.WorkingDir = repo
	parent.Tools = tools.LiveAgentCatalog()
	task := parent.RegisterBackgroundContext(ctx, "worktree followup", "start", SubModel{})
	wt, err := provisionSubagentWorktreeAt(ctx, repo, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt).Run()
	parent.LaunchBackground(task, wt)
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not complete")
	}
	target := filepath.Join(wt, "followup.txt")
	if err := os.Remove(target); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if _, err := parent.FollowupTask(ctx, task.ID, "write again", Events{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "written" {
		t.Fatalf("followup write: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "followup.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent workspace modified: %v", err)
	}
}
