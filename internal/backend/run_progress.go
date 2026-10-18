package backend

import (
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

type runProgress struct {
	mu                                     sync.Mutex
	started                                time.Time
	stage                                  string
	firstThinking, firstText, modelRequest *int64
	publish                                func(string, any, string) (protocol.Event, error)
}

func (p *runProgress) phase(stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stage == p.stage {
		return
	}
	elapsed := time.Since(p.started).Milliseconds()
	if stage == "waiting_model" {
		p.modelRequest = &elapsed
	}
	if stage == "thinking" && p.firstThinking == nil {
		p.firstThinking = &elapsed
	}
	if stage == "responding" && p.firstText == nil {
		p.firstText = &elapsed
	}
	p.stage = stage
	_, _ = p.publish(protocol.EventRunProgress, struct {
		Stage         string `json:"stage"`
		Elapsed       int64  `json:"elapsed_ms"`
		FirstThinking *int64 `json:"first_thinking_ms,omitempty"`
		FirstText     *int64 `json:"first_text_ms,omitempty"`
		ModelRequest  *int64 `json:"model_request_ms,omitempty"`
	}{stage, elapsed, p.firstThinking, p.firstText, p.modelRequest}, "")
}
