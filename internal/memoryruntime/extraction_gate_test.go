package memoryruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// Without the existing entries the extractor re-created a known fact under a
// new slug on every turn, and it re-read the last four user messages each
// time, extracting the same message again.
func TestExtractionSeesExistingMemoryAndJudgesOnlyLatestMessage(t *testing.T) {
	client := &fixtureClient{}
	rt, store := testRuntime(t, client)
	if _, err := store.Write(memory.WriteArgs{Slug: "reference-agent-skills-install", Scope: "global", MemoryType: "reference", Description: "Agent skills install under ~/.agents/skills", Body: "Skills live in ~/.agents/skills."}); err != nil {
		t.Fatal(err)
	}
	earlier := ai.Message{Role: "user", Content: "在~/.agents/skills下面安装这个技能"}
	latest := ai.Message{ID: "u2", Role: "user", Content: "帮我看看这个报错"}
	rt.Completed(context.Background(), agent.MemoryTurn{SessionID: "s", User: latest, History: []ai.Message{earlier, {Role: "assistant", Content: "ok"}, latest}})
	if !waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.requests) > 0 }) {
		t.Fatal("extractor never asked the model")
	}
	client.mu.Lock()
	request := client.requests[0]
	client.mu.Unlock()
	prompt := request.Messages[len(request.Messages)-1].TextContent()
	if !strings.Contains(prompt, "reference-agent-skills-install") {
		t.Fatalf("existing memory not shown to the extractor:\n%s", prompt)
	}
	latestAt := strings.Index(prompt, "<latest-user-message>")
	if latestAt < 0 || !strings.Contains(prompt[latestAt:], "帮我看看这个报错") || strings.Contains(prompt[latestAt:], "安装这个技能") {
		t.Fatalf("latest message is not singled out for judgement:\n%s", prompt)
	}
}

// New memories need high confidence, or medium backed by the user's words;
// an existing slug may still be updated at lower confidence.
func TestExtractionDropsWeakNewMemories(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[` +
		`{"action":"write","slug":"weak-low","scope":"global","type":"user","description":"d","body":"b","confidence":"low","source_quote":"我觉得可能吧"},` +
		`{"action":"write","slug":"weak-medium","scope":"global","type":"user","description":"d","body":"b","confidence":"medium"},` +
		`{"action":"write","slug":"strong-high","scope":"global","type":"user","description":"d","body":"b","confidence":"high","source_quote":"以后都用中文回答我"},` +
		`{"action":"update","slug":"known-entry","scope":"global","body":"updated","mode":"replace","confidence":"low"}]}`}
	rt, store := testRuntime(t, client)
	if _, err := store.Write(memory.WriteArgs{Slug: "known-entry", Scope: "global", MemoryType: "user", Description: "known", Body: "old"}); err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), extractionTurn("以后都用中文回答我"))
	if !waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "strong-high", Scope: "global"})
		return err == nil
	}) {
		t.Fatal("high-confidence memory was not written")
	}
	waitFor(t, func() bool {
		got, err := store.Read(memory.ReadArgs{Slug: "known-entry", Scope: "global"})
		return err == nil && got.Body == "updated"
	})
	for _, slug := range []string{"weak-low", "weak-medium"} {
		if _, err := store.Read(memory.ReadArgs{Slug: slug, Scope: "global"}); err == nil {
			t.Fatalf("weak new memory %s was written", slug)
		}
	}
	if got, _ := store.Read(memory.ReadArgs{Slug: "known-entry", Scope: "global"}); got.Body != "updated" {
		t.Fatalf("existing entry update was dropped: %q", got.Body)
	}
}
