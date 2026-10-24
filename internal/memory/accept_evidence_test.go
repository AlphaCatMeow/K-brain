package memory

import (
	"path/filepath"
	"testing"
)

// Accepting only marks an entry reviewed. It used to rebuild the evidence
// from the confidence alone, so the source-quote rule downgraded high to low
// and dropped the quote and reasoning.
func TestAcceptKeepsEvidence(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := &Evidence{Confidence: "high", SourceQuote: "我平时都用 pnpm 管依赖，别给我 npm 命令。", Reasoning: "explicit lasting preference"}
	if _, err := store.Write(WriteArgs{Slug: "package-manager-pnpm", Scope: "global", MemoryType: "user", Description: "Prefer pnpm", Body: "Use pnpm, not npm.", Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(ReadArgs{Slug: "package-manager-pnpm", Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(ReadArgs{Slug: "package-manager-pnpm", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Unreviewed {
		t.Fatal("accept did not mark the entry reviewed")
	}
	if got.Meta.Confidence != "high" {
		t.Fatalf("accept changed confidence high -> %q", got.Meta.Confidence)
	}
	entries, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Slug == "package-manager-pnpm" && (e.evidence == nil || e.evidence.SourceQuote != evidence.SourceQuote) {
			t.Fatalf("accept dropped the source quote: %+v", e.evidence)
		}
	}
}
