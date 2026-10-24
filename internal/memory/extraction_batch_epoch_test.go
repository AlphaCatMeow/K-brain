package memory

import (
	"errors"
	"testing"
)

func TestExtractionBatchRejectsPreWipePlan(t *testing.T) {
	store := newStore(t)
	other, err := OpenStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	epoch := store.WipeEpoch()
	if _, err := other.WipeAll(); err != nil {
		t.Fatal(err)
	}
	description, body := "Reply preference", "Use concise answers."
	batch := BatchArgs{Decisions: []Decision{{Op: "upsert", Slug: "reply-style", Scope: "global", MemoryType: "user", Description: &description, Body: &body}}}
	batch.DailyAppend = &struct {
		Bullet string `json:"bullet"`
	}{Bullet: "Old journal entry"}
	_, err = store.ApplyExtractionBatch(batch, epoch)
	var storeErr *StoreError
	if !errors.As(err, &storeErr) || storeErr.Code != "stale_extraction" {
		t.Fatalf("pre-wipe batch error = %v, want stale_extraction", err)
	}
	list, err := store.List(ListArgs{IncludeDaily: true, IncludeAllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 0 {
		t.Fatalf("stale batch restored %d entries after wipe", len(list.Entries))
	}

	batch.DailyAppend = nil
	if _, err := store.ApplyExtractionBatch(batch, store.WipeEpoch()); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Read(ReadArgs{Slug: "reply-style", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Meta.Unreviewed {
		t.Fatal("extraction batch must mark its entries unreviewed")
	}
}
