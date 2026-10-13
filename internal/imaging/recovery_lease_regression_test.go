package imaging

import (
	"context"
	"testing"
	"time"
)

func TestRecoveryClearsCancelledLeasesWithoutResubmitting(t *testing.T) {
	for _, tombstoned := range []bool{false, true} {
		name := "cancelled"
		if tombstoned {
			name = "legacy-tombstone"
		}
		t.Run(name, func(t *testing.T) { testRecoveryCancelledLease(t, tombstoned) })
	}
}

func testRecoveryCancelledLease(t *testing.T, tombstoned bool) {
	s, _, adapter, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "restart-cancelled", request)
	if err != nil {
		t.Fatal(err)
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
		x := ex.Items[j.Items[0].ID]
		x.LeaseOwner = "previous-process"
		x.LeaseExpiresAt = time.Now().Add(time.Hour)
		x.Attempts = []ImageAttempt{{AttemptNo: 1, StartedAt: time.Now(), SubmissionState: "submitting"}}
		ex.Items[j.Items[0].ID] = x
		j.Status, j.Items[0].Status = JobRunning, JobRunning
		j.Items[0].AttemptCount = 1
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		return s.writeJob(tx, &j, EventJobStateChanged)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CancelJob(ctx, scope, job.ID); err != nil {
		t.Fatal(err)
	}
	assertEngineError(t, s.store.DeleteJob(ctx, scope, job.ID), CodeProjectBusy)
	if tombstoned {
		// Reproduce a durable record written by the old deletion implementation.
		if err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
			r := tx.data.Jobs[job.ID]
			r.DeletedAt = &tx.now
			tx.data.Jobs[job.ID] = r
			tx.mutations = append(tx.mutations, storeMutation{Kind: "job", Job: &r})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	root := s.store.dir
	if err = s.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered := NewService(reopened, s.registry, nil, s.options)
	q := NewScheduler(recovered)
	if err = q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Close(ctx)
	reopened.mu.RLock()
	ex := executionFromDisk(reopened.data.Executions[job.ID])
	j := cloneStoreValue(reopened.data.Jobs[job.ID].Value)
	deleted := reopened.data.Jobs[job.ID].DeletedAt != nil
	reopened.mu.RUnlock()
	x := ex.Items[job.Items[0].ID]
	if x.LeaseOwner != "" || !x.LeaseExpiresAt.IsZero() || len(x.Attempts) != 1 {
		t.Fatalf("recovered execution: %+v", x)
	}
	if j.Status != JobCancelled || j.Items[0].Status != JobCancelled || deleted != tombstoned {
		t.Fatalf("cancelled job reopened: %+v, deleted=%v", j, deleted)
	}
	project, err := reopened.ReadProject(scope, request.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeleteProject(ctx, scope, project.ID, project.Revision); err != nil {
		t.Fatal(err)
	}
	if err = q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if submits, polls, reconciles, cancels := adapter.counts(); submits != 0 || polls != 0 || reconciles != 0 || cancels != 0 {
		t.Fatalf("cancelled recovery made provider calls: %d %d %d %d", submits, polls, reconciles, cancels)
	}
}
