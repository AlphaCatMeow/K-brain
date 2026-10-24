package memoryruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// steerFirstClient steers the running turn on its first request, the way a
// user types while the agent works.
type steerFirstClient struct {
	fixtureClient
	ag   *agent.Agent
	once sync.Once
}

func (c *steerFirstClient) Clone() ai.Client { return c }

func (c *steerFirstClient) Stream(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.once.Do(func() { c.ag.Steer("顺便看一下 README") })
	return ai.Message{Role: "assistant", Content: "ok"}, ai.Usage{}, nil
}

// turnPending calls ev.OnSteer while holding msgsMu (agent.go steered loop).
// If that host callback panics, bd95f38's defer runs MessagesSnapshot on the
// still-locked mutex: the panic never surfaces and the goroutine hangs with
// turnMu held. On HEAD~1 the panic propagates to the caller.
func TestPanicInTurnWithMemoryRuntimeStillPropagates(t *testing.T) {
	rt, _ := testRuntime(t, &fixtureClient{})
	t.Cleanup(func() { _ = rt.Close() })
	chat := &steerFirstClient{}
	ag := agent.New(chat, "m", 1000, "sys")
	chat.ag = ag
	ag.SetMemoryRuntime(rt)
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = ag.Turn(context.Background(), "跑一下测试", agent.Events{OnSteer: func(string) { panic("host OnSteer failed") }})
	}()
	select {
	case v := <-recovered:
		if v == nil {
			t.Fatal("fixture: OnSteer panic did not reach the caller")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn hung after a panic under msgsMu: the memory defer's MessagesSnapshot deadlocks instead of letting the panic propagate")
	}
}
