package memoryruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// The extractor only ever reads 2000 runes of a message (extractionMessages
// truncateRunes), but hold stores the whole text in .kbrain-state.json, which
// every Completed (TakeUnjudged), organizer and import call reads and
// re-serialises under the store lock. One stopped turn on a pasted log keeps
// the whole log in the shared state file (up to 8 per session, never taken
// for sessions that are not continued).
func TestHeldMessageTextIsBoundedInState(t *testing.T) {
	rt, store := testRuntime(t, &journalClient{dailyText: "never"})
	t.Cleanup(func() { _ = rt.Close() })
	log := "帮我看下这个报错日志：\n" + strings.Repeat("panic: runtime error: index out of range [3] with length 3\n", 20000)
	rt.Abandoned("s", ai.Message{ID: "u1", Role: "user", Content: log})
	info, err := os.Stat(filepath.Join(store.Root(), ".kbrain-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 2000 runes of CJK text is at most ~6 KB of UTF-8 plus JSON framing.
	if info.Size() > 16*1024 {
		t.Fatalf(".kbrain-state.json is %d bytes after holding one %d-byte message; the prompt uses at most 2000 runes", info.Size(), len(log))
	}
}
