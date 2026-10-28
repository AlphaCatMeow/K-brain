package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestSummaryReviewUsageDetailsDoNotAliasReturnedMetadata(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	if err := store.update(id, func(data *sessionData) error {
		data.Meta.SubUsage = map[string]ai.Usage{"child": {
			PromptTokensDetails: &struct {
				CachedTokens int `json:"cached_tokens"`
			}{CachedTokens: 7},
		}}
		data.Meta.ModelUsage = map[string]ai.Usage{"model": {
			PromptTokensDetails: &struct {
				CachedTokens int `json:"cached_tokens"`
			}{CachedTokens: 11},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	first.Sessions[0].SubUsage["child"].PromptTokensDetails.CachedTokens = 700
	first.Sessions[0].ModelUsage["model"].PromptTokensDetails.CachedTokens = 1100
	second, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Sessions[0].SubUsage["child"].PromptTokensDetails.CachedTokens; got != 7 {
		t.Errorf("returned SubUsage nested pointer polluted cache: got %d want 7", got)
	}
	if got := second.Sessions[0].ModelUsage["model"].PromptTokensDetails.CachedTokens; got != 11 {
		t.Errorf("returned ModelUsage nested pointer polluted cache: got %d want 11", got)
	}
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Items[0].Meta.SubUsage["child"].PromptTokensDetails.CachedTokens = 800
	snapshot.Items[0].Meta.ModelUsage["model"].PromptTokensDetails.CachedTokens = 1200
	afterSnapshot, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if afterSnapshot.Sessions[0].SubUsage["child"].PromptTokensDetails.CachedTokens != 7 || afterSnapshot.Sessions[0].ModelUsage["model"].PromptTokensDetails.CachedTokens != 11 {
		t.Fatal("returned snapshot metadata polluted a later page")
	}
	if err := store.SetTitle(id, "changed after snapshot"); err != nil {
		t.Fatal(err)
	}
	request := SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision}
	changes, err := store.SummaryChanges(context.Background(), request)
	if err != nil || len(changes.Changes) != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if changes.Changes[0].Meta.SubUsage["child"].PromptTokensDetails.CachedTokens != 7 || changes.Changes[0].Meta.ModelUsage["model"].PromptTokensDetails.CachedTokens != 11 {
		t.Fatal("returned snapshot metadata polluted a later change")
	}
	changes.Changes[0].Meta.SubUsage["child"].PromptTokensDetails.CachedTokens = 900
	changes.Changes[0].Meta.ModelUsage["model"].PromptTokensDetails.CachedTokens = 1300
	again, err := store.SummaryChanges(context.Background(), request)
	if err != nil || len(again.Changes) != 1 {
		t.Fatalf("replayed changes=%+v err=%v", again, err)
	}
	if again.Changes[0].Meta.SubUsage["child"].PromptTokensDetails.CachedTokens != 7 || again.Changes[0].Meta.ModelUsage["model"].PromptTokensDetails.CachedTokens != 11 {
		t.Fatal("returned change metadata polluted a replayed change")
	}
}

func TestSummaryReviewThreeBatchesConvergeAfterConcurrentChanges(t *testing.T) {
	store := openPageStore(t)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state := store.summaries
	stamp := time.Now().UTC()
	upsert := func(id, title string) {
		before, existed := state.entries[id]
		after := summaryEntry{meta: Meta{ID: id, Title: title, CreatedAt: stamp, UpdatedAt: stamp}, count: 1}
		store.recordSummaryChangeLocked(id, before, existed, after, true)
		state.entries[id] = after
	}
	for index := range 1201 {
		id := fmt.Sprintf("review-%04d", index)
		upsert(id, id)
	}
	view := map[string]string{}
	since := snapshot.Cursor.Revision
	request := func() SummaryChangeResult {
		result, ready := store.summaryChangesLocked(SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: since}, true)
		if !ready || result.Reset || len(result.Changes) == 0 {
			t.Fatalf("invalid catch-up batch ready=%v result=%+v", ready, result)
		}
		if result.NextSince != result.Changes[len(result.Changes)-1].Revision {
			t.Fatal("next_since is not the final delivered revision")
		}
		if result.More && result.NextSince >= result.Cursor.Revision {
			t.Fatal("partial batch jumped to head")
		}
		for _, change := range result.Changes {
			if change.Revision <= since {
				t.Fatal("catch-up cursor moved backwards")
			}
			since = change.Revision
			if change.Kind == "delete" || !change.Listed {
				delete(view, change.ID)
			} else {
				view[change.ID] = change.Meta.Title
			}
		}
		return result
	}
	first := request()
	if len(first.Changes) != 500 || !first.More || first.Cursor.Revision != 1201 || since != 500 {
		t.Fatalf("first batch length=%d more=%v head=%d next=%d", len(first.Changes), first.More, first.Cursor.Revision, since)
	}
	upsert("review-0000", "changed after its first delivery")
	store.removeSummaryLocked("review-0700")
	upsert("review-1201", "created during catch-up")
	upsert("review-1100", "changed before its first delivery")
	second := request()
	if len(second.Changes) != 500 || !second.More {
		t.Fatalf("second batch length=%d more=%v", len(second.Changes), second.More)
	}
	third := request()
	if third.More || since != state.revision || len(view) != len(state.entries) {
		t.Fatalf("catch-up incomplete more=%v next=%d head=%d visible=%d entries=%d", third.More, since, state.revision, len(view), len(state.entries))
	}
	for id, entry := range state.entries {
		if view[id] != entry.meta.Title {
			t.Fatalf("projection mismatch for %s: got %q want %q", id, view[id], entry.meta.Title)
		}
	}
}

func TestSummaryReviewTombstoneCapacityPreservesFloor(t *testing.T) {
	store := openPageStore(t)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state := store.summaries
	now := time.Now()
	for revision := uint64(1); revision <= maxSummaryTombstones+5; revision++ {
		state.changes[fmt.Sprintf("deleted-%05d", revision)] = summaryChangeEntry{
			revision: revision,
			kind:     "delete",
			created:  now,
		}
		state.revision = revision
	}
	store.pruneSummaryChangesLocked(now)
	if len(state.changes) != maxSummaryTombstones || state.minSince != 5 {
		t.Fatalf("capacity prune retained=%d floor=%d", len(state.changes), state.minSince)
	}
	reset, ready := store.summaryChangesLocked(SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: 4}, true)
	if !ready || !reset.Reset || reset.ResetReason != "expired" {
		t.Fatalf("below-floor cursor did not reset: %+v", reset)
	}
	valid, ready := store.summaryChangesLocked(SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: 5}, true)
	if !ready || valid.Reset || len(valid.Changes) != 500 || valid.Changes[0].Revision != 6 || valid.NextSince != 505 || !valid.More {
		t.Fatalf("floor cursor cannot continue: %+v", valid)
	}
}
