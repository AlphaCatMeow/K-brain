package memoryruntime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// review4NewContextClient calls new_context on its second request and answers plainly
// otherwise.
type review4NewContextClient struct {
	fixtureClient
	mu sync.Mutex
	n  int
}

func (c *review4NewContextClient) Clone() ai.Client { return c }

func (c *review4NewContextClient) Stream(_ context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.n++
	n := c.n
	c.mu.Unlock()
	if n == 2 {
		call := ai.ToolCall{ID: "call-nc", Type: "function"}
		call.Function.Name = "new_context"
		call.Function.Arguments = `{}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", Content: "done"}, ai.Usage{}, nil
}

// 验证（预期通过）：关闭期间的回合调用 new_context，"Current task" 通知只带一个 memory-off
// 标记（contextSnapshots 已带，quotesOff 分支只在开启时追加）。
func TestReview4NewContextNoticeWhileOffHasOneMarker(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	rt, _ := testRuntime(t, &fixtureClient{})
	t.Cleanup(func() { _ = rt.Close() })
	chat := &review4NewContextClient{}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)
	if _, err := ag.Turn(context.Background(), "hello while on", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	ag.SetMemoryEnabled(false)
	if _, err := ag.Turn(context.Background(), "OFFSECRET-NC4 work while off", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ag.MessagesSnapshot() {
		if m.Role != "user" || !strings.Contains(m.TextContent(), "Current task:") {
			continue
		}
		found = true
		marks := 0
		for _, s := range m.PromptSnapshots {
			if s.Source == "memory-off" {
				marks++
			}
		}
		if marks != 1 {
			t.Fatalf("want one memory-off marker on the notice, got %d: %+v", marks, m.PromptSnapshots)
		}
	}
	if !found {
		t.Fatal("fixture: no new_context notice in history")
	}
}
