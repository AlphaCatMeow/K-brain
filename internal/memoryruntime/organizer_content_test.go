package memoryruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func TestOrganizerRejectsLegacyPendingPlanWithoutContentChecks(t *testing.T) {
	runtime, store := testRuntime(t, &fixtureClient{})
	for _, slug := range []string{"one", "two"} {
		if _, err := store.Write(memory.WriteArgs{Slug: slug, Body: "Same content", Scope: "global", MemoryType: "reference", Description: "Notes"}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := runtime.RunOrganizer(context.Background(), "", OrganizerManual)
	if err != nil || len(run.Pending) != 1 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	run.Pending[0].Preconditions = nil
	raw, _ := json.Marshal(run)
	var legacy memory.OrganizeRun
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveOrganizeRun(legacy); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ApplyOrganizerRun(context.Background(), run.RunID)
	if err != nil || result.Applied != 0 || len(result.Warnings) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestOrganizerPreservesDifferentBodiesWithSameDescription(t *testing.T) {
	runtime, store := testRuntime(t, &fixtureClient{})
	for _, item := range []struct{ slug, body string }{{"one", "First distinct fact"}, {"two", "Second distinct fact"}} {
		if _, err := store.Write(memory.WriteArgs{Slug: item.slug, Body: item.body, Scope: "global", MemoryType: "reference", Description: "Project notes"}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := runtime.RunOrganizer(context.Background(), "", OrganizerScheduled)
	if err != nil || run.Applied != 0 || len(run.Pending) != 0 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	entries, err := store.List(memory.ListArgs{Limit: 100})
	if err != nil || len(entries.Entries) != 2 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestOrganizerPreservesEditsAfterReview(t *testing.T) {
	for _, editSurvivor := range []bool{false, true} {
		t.Run(map[bool]string{false: "candidate", true: "survivor"}[editSurvivor], func(t *testing.T) {
			runtime, store := testRuntime(t, &fixtureClient{})
			for _, slug := range []string{"one", "two"} {
				if _, err := store.Write(memory.WriteArgs{Slug: slug, Body: "Same content", Scope: "global", MemoryType: "reference", Description: "Notes"}); err != nil {
					t.Fatal(err)
				}
			}
			run, err := runtime.RunOrganizer(context.Background(), "", OrganizerManual)
			if err != nil || len(run.Pending) != 1 {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			slug := run.Pending[0].Slug
			if editSurvivor {
				slug = run.Pending[0].Preconditions[0].Slug
			}
			body := "Edited after review"
			if _, err := store.Update(memory.UpdateArgs{ReadArgs: memory.ReadArgs{Slug: slug, Scope: "global"}, Body: &body}); err != nil {
				t.Fatal(err)
			}
			applied, err := runtime.ApplyOrganizerRun(context.Background(), run.RunID)
			if err != nil || applied.Applied != 0 || len(applied.Warnings) == 0 {
				t.Fatalf("apply=%+v err=%v", applied, err)
			}
			entries, err := store.List(memory.ListArgs{Limit: 100})
			if err != nil || len(entries.Entries) != 2 {
				t.Fatalf("entries=%+v err=%v", entries, err)
			}
		})
	}
}
