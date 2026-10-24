package memoryruntime

import (
	"context"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// 8e5bc94: "Extraction writes and updates always leave the entry unreviewed, so
// model-written text never shows as user-approved." The typed schema still
// advertises action "accept", so one plan can write a new memory and accept it
// in the same batch; the model-written entry ends up reviewed.
func TestExtractorCannotApproveItsOwnWrite(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[` +
		`{"action":"write","slug":"user-pkg-manager","scope":"global","type":"user","description":"Uses pnpm","body":"User uses pnpm.","source_quote":"I use pnpm","confidence":"high","source_quote":"the user said so"},` +
		`{"action":"accept","slug":"user-pkg-manager","scope":"global"}]}`}
	rt, store := testRuntime(t, client)
	rt.Completed(context.Background(), extractionTurn("I use pnpm"))
	if !waitFor(t, func() bool {
		_, err := store.Read(memory.ReadArgs{Slug: "user-pkg-manager", Scope: "global"})
		return err == nil
	}) {
		t.Fatal("extraction never applied the write")
	}
	waitFor(t, func() bool {
		got, _ := store.Read(memory.ReadArgs{Slug: "user-pkg-manager", Scope: "global"})
		return !got.Meta.Unreviewed
	})
	got, err := store.Read(memory.ReadArgs{Slug: "user-pkg-manager", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Meta.Unreviewed {
		t.Fatalf("model-written memory approved by the extractor itself: body=%q unreviewed=%v", got.Body, got.Meta.Unreviewed)
	}
}

// The one-time import leaves memory.md facts unreviewed so the user reviews
// them in the panel. A hidden extraction plan can accept them without the user.
func TestExtractorCannotApproveImportedMemory(t *testing.T) {
	client := &fixtureClient{payload: `{"decisions":[{"action":"accept","slug":"imported-abc","scope":"global"}]}`}
	rt, store := testRuntime(t, client)
	if _, err := store.Write(memory.WriteArgs{Slug: "imported-abc", Scope: "global", MemoryType: "user", Description: "prefers tabs", Body: "prefers tabs", Actor: "migration"}); err != nil {
		t.Fatal(err)
	}
	rt.Completed(context.Background(), extractionTurn("I prefer tabs over spaces"))
	waitFor(t, func() bool {
		got, _ := store.Read(memory.ReadArgs{Slug: "imported-abc", Scope: "global"})
		return !got.Meta.Unreviewed
	})
	got, err := store.Read(memory.ReadArgs{Slug: "imported-abc", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Meta.Unreviewed {
		t.Fatal("unreviewed imported memory accepted by the hidden extractor")
	}
}
