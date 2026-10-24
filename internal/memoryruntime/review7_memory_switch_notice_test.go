package memoryruntime

// Seventh independent review of the memory master switch (through 07e1902): the off
// notice across new_context and compaction, with the real agent and memory runtime.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const review7Notice = "Memory is turned off by the user"

// review7Client records every conversation request as the model sees it. When newContext
// is set, the next request calls the real new_context tool once.
type review7Client struct {
	fixtureClient
	mu         sync.Mutex
	newContext bool
	texts      []string
}

func (c *review7Client) Clone() ai.Client { return c }

func (c *review7Client) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "Objective: review fixture.\nOpen task: none", ai.Usage{}, nil
}

func (c *review7Client) Stream(_ context.Context, request ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	var whole strings.Builder
	for _, m := range request.Messages {
		whole.WriteString(m.TextContent())
		whole.WriteString("\n")
	}
	c.mu.Lock()
	c.texts = append(c.texts, whole.String())
	call := c.newContext
	c.newContext = false
	c.mu.Unlock()
	if call {
		tc := ai.ToolCall{ID: "call-nc", Type: "function"}
		tc.Function.Name = "new_context"
		tc.Function.Arguments = `{}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", Content: "done"}, ai.Usage{}, nil
}

func (c *review7Client) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.texts[len(c.texts)-1]
}

func review7Agent(t *testing.T) (*agent.Agent, *review7Client) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	rt, _ := testRuntime(t, &fixtureClient{})
	t.Cleanup(func() { _ = rt.Close() })
	chat := &review7Client{}
	ag := agent.New(chat, "m", 1000, "sys")
	ag.SetMemoryRuntime(rt)
	return ag, chat
}

// 验证（预期通过）：关闭期间回合内调用 new_context，历史被清空。新上下文窗口里说明重发一次
// （模型已看不到旧说明），之后的关闭回合不再重复。
func TestReview7NoticeResentOnceAfterNewContextWhileOff(t *testing.T) {
	ag, chat := review7Agent(t)
	ag.SetMemoryEnabled(false)
	chat.newContext = true
	if _, err := ag.Turn(context.Background(), "clean up the build script", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(chat.last(), review7Notice); n != 1 {
		t.Fatalf("request in the new context window: want one notice, got %d:\n%s", n, chat.last())
	}
	if _, err := ag.Turn(context.Background(), "and the readme", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(chat.last(), review7Notice); n != 1 {
		t.Fatalf("next off turn after new_context: want one notice, got %d:\n%s", n, chat.last())
	}
}

// 验证（预期通过）：关闭期间手动压缩，带说明的首条用户消息被摘要掉（摘要只保留原话正文）。
// 下一轮关闭回合重发说明一次。
func TestReview7NoticeResentOnceAfterCompactionWhileOff(t *testing.T) {
	ag, chat := review7Agent(t)
	ag.SetMemoryEnabled(false)
	for _, prompt := range []string{"first off message", "second off message", "third off message"} {
		if _, err := ag.Turn(context.Background(), prompt, agent.Events{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(chat.last(), review7Notice); n != 1 {
		t.Fatalf("precondition: want one notice before compaction, got %d", n)
	}
	if err := ag.ManualCompact(context.Background(), agent.Events{}); err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, m := range ag.MessagesSnapshot() {
		for _, s := range m.PromptSnapshots {
			carried = carried || strings.Contains(s.Text, review7Notice)
		}
	}
	if carried {
		t.Fatal("fixture: compaction kept the message that carried the notice")
	}
	if _, err := ag.Turn(context.Background(), "fourth off message", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(chat.last(), review7Notice); n != 1 {
		t.Fatalf("after compaction: want one notice, got %d:\n%s", n, chat.last())
	}
}
