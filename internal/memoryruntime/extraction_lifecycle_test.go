package memoryruntime

import (
	"context"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// The backend cancels a run's context as soon as the run returns, which is
// right after Completed. Extraction runs after that and must not inherit the
// cancellation, or no turn's memory is ever extracted.
func TestExtractionSurvivesRunContextCancel(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"write","slug":"user-shell","scope":"global","type":"user","description":"Uses Git Bash","body":"Give shell commands for bash.","confidence":"high","source_quote":"the user said so"}]}`}
	rt, store := testRuntime(t, client)
	runCtx, cancel := context.WithCancel(context.Background())
	rt.Completed(runCtx, extractionTurn("我用的是 Windows + Git Bash，给命令的时候都按 bash 写。"))
	cancel()
	if !waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "user-shell", Scope: "global"})
		return err == nil
	}) {
		t.Fatal("extraction was cancelled with the run that triggered it")
	}
}
