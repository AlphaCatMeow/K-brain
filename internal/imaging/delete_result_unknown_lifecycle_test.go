package imaging

import (
	"context"
	"sync"
	"testing"
	"time"
)

// once a result_unknown job is deleted it can never be reconciled
// (ReadJob hides tombstones), so its files should expire like any other
// tombstoned terminal job instead of being pinned forever.
func TestAssetGCReclaimsDeletedResultUnknownJob(t *testing.T) {
	for _, status := range []JobStatus{JobFailed, JobResultUnknown} {
		t.Run(string(status), func(t *testing.T) {
			g := newAssetGCTest(t)
			input := g.asset(storeScope, "upload", 2)
			output := g.asset(storeScope, "generated", 3)
			p := g.job(storeScope, "ij_deleted_"+string(status), status, []string{input.ID}, []string{output.ID})
			if err := g.store.DeleteProject(context.Background(), storeScope, p.ID, p.Revision); err != nil {
				t.Fatal(err)
			}
			// Past tombstone retention, then one mark pass and one grace period.
			g.now = g.now.Add(31 * 24 * time.Hour)
			g.run()
			g.now = g.now.Add(25 * time.Hour)
			g.run()
			g.files(output, false)
			g.files(input, false)
		})
	}
}

// a job whose items are split between result_unknown and running is
// aggregated as running, so neither delete path may release it.
func TestDeleteBlockedWhileSiblingItemStillRunning(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	request.Count = 2
	job, _, err := s.CreateJob(ctx, scope, "split-unknown", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Items) != 2 {
		t.Fatalf("items = %d", len(job.Items))
	}
	if err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(scope, job.ID)
		if err != nil {
			return err
		}
		ex, err := tx.ReadExecution(scope, job.ID)
		if err != nil {
			return err
		}
		j.Items[0].Status = JobResultUnknown
		j.Items[1].Status = JobRunning
		j.Status = JobRunning
		aggregateJob(&j, ex)
		return s.writeJob(tx, &j, jobEvent(j.Status))
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.store.ReadJob(scope, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobRunning {
		t.Fatalf("split job aggregated as %s", got.Status)
	}
	assertEngineError(t, s.store.DeleteJob(ctx, scope, job.ID), CodeProjectBusy)
	project, err := s.store.ReadProject(scope, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	assertEngineError(t, s.store.DeleteProject(ctx, scope, project.ID, project.Revision), CodeProjectBusy)
}

// ReconcileJob and DeleteJob run under the same Store lock. Exactly one
// of them may win; a reopened job must never also be tombstoned.
func TestReconcileAndDeleteResultUnknownSerialize(t *testing.T) {
	for round := 0; round < 50; round++ {
		s, _, _, scope, request := engineFixture(t)
		ctx := context.Background()
		job, _, err := s.CreateJob(ctx, scope, "race", request)
		if err != nil {
			t.Fatal(err)
		}
		markResultUnknown(t, s, scope, job.ID, false, "provider-handle")
		var wg sync.WaitGroup
		var reconcileErr, deleteErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, reconcileErr = s.ReconcileJob(ctx, scope, job.ID) }()
		go func() { defer wg.Done(); deleteErr = s.store.DeleteJob(ctx, scope, job.ID) }()
		wg.Wait()
		_, readErr := s.store.ReadJob(scope, job.ID)
		switch {
		case deleteErr == nil:
			assertEngineError(t, reconcileErr, CodeNotFound)
			assertEngineError(t, readErr, CodeNotFound)
		case reconcileErr == nil:
			assertEngineError(t, deleteErr, CodeProjectBusy)
			if readErr != nil {
				t.Fatal(readErr)
			}
		default:
			t.Fatalf("reconcile=%v delete=%v", reconcileErr, deleteErr)
		}
	}
}

// Agnes-like work (pending without a handle) reaches result_unknown
// through the real scheduler. Deleting it after the worker exits must leave
// the scheduler quiet and reconcile must report not found.
func TestDeleteSchedulerResultUnknownWithoutHandle(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "pending"}, nil
	}
	j, _, err := s.CreateJob(context.Background(), scope, "agnes-like", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	awaitEngine(t, s, scope, j.ID, JobResultUnknown)
	engineEventually(t, func() bool {
		ex, _ := s.store.ReadExecution(scope, j.ID)
		return ex.Items[j.Items[0].ID].LeaseOwner == ""
	})
	if _, err = s.CancelJob(context.Background(), scope, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.store.DeleteJob(context.Background(), scope, j.ID); err != nil {
		t.Fatalf("delete after worker exit: %v", err)
	}
	before, polls, reconciles, cancels := a.counts()
	s.notify()
	time.Sleep(50 * time.Millisecond)
	after, polls2, reconciles2, cancels2 := a.counts()
	if before != after || polls != polls2 || reconciles != reconciles2 || cancels != cancels2 {
		t.Fatalf("scheduler touched deleted job: %d/%d/%d/%d -> %d/%d/%d/%d", before, polls, reconciles, cancels, after, polls2, reconciles2, cancels2)
	}
	_, err = s.ReconcileJob(context.Background(), scope, j.ID)
	assertEngineError(t, err, CodeNotFound)
	busy, err := s.ImageDependenciesInUse(context.Background(), scope, []string{j.TargetSnapshot.ConnectionID}, []string{j.TargetSnapshot.ID})
	if err != nil || busy {
		t.Fatalf("deleted unknown job still pins its target: %v %v", busy, err)
	}
}
