package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestCancelOrganizeRunDoesNotOverwriteApplyClaim(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateOrganizeRun(OrganizeRun{"runId": "cancel-claim", "status": "pending_review"}); err != nil {
		t.Fatal(err)
	}
	if _, shouldApply, err := s.ClaimOrganizeRunApply("cancel-claim"); err != nil || !shouldApply {
		t.Fatalf("claim = %v, %v", shouldApply, err)
	}
	stored, changed, err := s.CancelOrganizeRun("cancel-claim", "context canceled")
	if err != nil || changed || stored["status"] != "applying" {
		t.Fatalf("cancel overwrote claim: stored=%v changed=%v err=%v", stored, changed, err)
	}
}

func TestStaleOrganizerExecutorCannotRecreateClearedRun(t *testing.T) {
	s := newStore(t)
	old, err := s.CreateOrganizeRun(OrganizeRun{"runId": "stale-clear", "status": "running"})
	if err != nil {
		t.Fatal(err)
	}
	oldEpoch := old["epoch"]
	if _, err := s.ClearOrganizeHistory(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveOrganizeRun(OrganizeRun{"runId": "stale-clear", "epoch": oldEpoch, "executorId": "old", "status": "completed"}); err == nil {
		t.Fatal("stale executor recreated cleared run")
	}
	if _, err := s.CreateOrganizeRun(OrganizeRun{"runId": "stale-clear", "epoch": uint64(1), "status": "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveOrganizeRun(OrganizeRun{"runId": "stale-clear", "epoch": oldEpoch, "executorId": "old", "status": "completed"}); err == nil {
		t.Fatal("stale executor overwrote a new run with the reused id")
	}
	if _, err := s.SaveOrganizeRun(OrganizeRun{"runId": "legacy-after-reset", "executorId": "old", "status": "completed"}); err == nil {
		t.Fatal("legacy executor recreated a run after reset")
	}
	runs, err := s.ListOrganizeRuns()
	if err != nil || len(runs["runs"].([]OrganizeRun)) != 1 || runs["runs"].([]OrganizeRun)[0]["status"] != "running" {
		t.Fatalf("stale save changed reused run=%v err=%v", runs, err)
	}
}

func TestStaleOrganizerExecutorCannotOverwriteNewExecutor(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateOrganizeRun(OrganizeRun{"runId": "stale-owner", "status": "running", "epoch": uint64(0), "executorId": "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveOrganizeRun(OrganizeRun{"runId": "stale-owner", "epoch": uint64(0), "executorId": "old", "status": "completed"}); err == nil {
		t.Fatal("stale executor overwrote newer executor")
	}
	stored, err := s.ReadOrganizeRun("stale-owner")
	if err != nil || stored["executorId"] != "new" || stored["status"] != "running" {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
}

func TestCancelledOrganizerRunRejectsLateExecutorSave(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateOrganizeRun(OrganizeRun{"runId": "cancel-late", "status": "running", "executorId": "old"}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := s.CancelOrganizeRun("cancel-late", "context canceled"); err != nil || !changed {
		t.Fatalf("cancel changed=%v err=%v", changed, err)
	}
	if _, err := s.SaveOrganizeRun(OrganizeRun{"runId": "cancel-late", "epoch": uint64(0), "executorId": "old", "status": "completed"}); err == nil {
		t.Fatal("late executor overwrote cancellation")
	}
	stored, err := s.ReadOrganizeRun("cancel-late")
	if err != nil || stored["status"] != "cancelled" {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
}

func TestStaleOrganizerExecutorCannotCancelNewRun(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateOrganizeRun(OrganizeRun{"runId": "cancel-owner", "status": "running", "epoch": uint64(0), "executorId": "new"}); err != nil {
		t.Fatal(err)
	}
	stored, changed, err := s.CancelOrganizeRunOwned("cancel-owner", 0, "old", "context canceled")
	if err != nil || changed || stored["status"] != "running" {
		t.Fatalf("stale cancel changed run: stored=%v changed=%v err=%v", stored, changed, err)
	}
}

func TestSaveOrganizeRunPreservesReportFieldsAndClaimsApply(t *testing.T) {
	s := newStore(t)
	created, err := s.CreateOrganizeRun(OrganizeRun{
		"runId":     "organize-save",
		"status":    "pending_review",
		"model":     "saved-model",
		"warnings":  []string{"warning"},
		"createdAt": int64(1700000000123),
		"updatedAt": int64(1700000000123),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created["model"] != "saved-model" {
		t.Fatalf("created=%v", created)
	}
	updated, err := s.SaveOrganizeRun(OrganizeRun{"runId": "organize-save", "status": "completed", "updatedAt": int64(1700000000999)})
	if err != nil {
		t.Fatal(err)
	}
	warnings, ok := updated["warnings"].([]any)
	if updated["model"] != "saved-model" || !ok || len(warnings) != 1 || warnings[0] != "warning" {
		t.Fatalf("fields dropped: %v", updated)
	}
	if fmt.Sprint(updated["createdAt"]) != "1.700000000123e+12" && fmt.Sprint(updated["createdAt"]) != "1700000000123" {
		t.Fatalf("createdAt changed: %v", updated["createdAt"])
	}
	claimed, shouldApply, err := s.ClaimOrganizeRunApply("organize-save")
	if err != nil || shouldApply || claimed["status"] != "completed" {
		t.Fatalf("completed claim=%v %v %v", claimed, shouldApply, err)
	}

	pending, err := s.CreateOrganizeRun(OrganizeRun{"runId": "organize-claim", "status": "pending_review", "createdAt": time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	claimed, shouldApply, err = s.ClaimOrganizeRunApply(pending["runId"].(string))
	if err != nil || !shouldApply || claimed["status"] != "applying" {
		t.Fatalf("claim=%v %v %v", claimed, shouldApply, err)
	}
	if _, _, err = s.ClaimOrganizeRunApply(pending["runId"].(string)); err == nil {
		t.Fatal("concurrent claim accepted")
	}
}
