package imaging

import (
	"context"
	"testing"
	"time"
)

// markResultUnknown puts a created job into result_unknown, optionally with a
// worker lease or a provider handle left on its only item.
func markResultUnknown(t *testing.T, s *Service, scope Scope, id string, lease bool, handle string) {
	t.Helper()
	ctx := context.Background()
	if err := s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(scope, id)
		if err != nil {
			return err
		}
		ex, err := tx.ReadExecution(scope, id)
		if err != nil {
			return err
		}
		x := ex.Items[j.Items[0].ID]
		x.Attempts = []ImageAttempt{{AttemptNo: 1, StartedAt: time.Now(), SubmissionState: "submitted"}}
		x.ProviderHandle = handle
		if lease {
			x.LeaseOwner = "worker-still-exiting"
			x.LeaseExpiresAt = time.Now().Add(time.Hour)
		}
		ex.Items[j.Items[0].ID] = x
		j.Status, j.Items[0].Status = JobResultUnknown, JobResultUnknown
		j.Items[0].AttemptCount = 1
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		return s.writeJob(tx, &j, EventJobFinished)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteJobAllowsResultUnknown(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "unknown-delete", request)
	if err != nil {
		t.Fatal(err)
	}
	markResultUnknown(t, s, scope, job.ID, false, "")
	if err = s.store.DeleteJob(ctx, scope, job.ID); err != nil {
		t.Fatalf("result_unknown job without a lease must be deletable: %v", err)
	}
	if _, err = s.store.ReadJob(scope, job.ID); err == nil {
		t.Fatal("deleted result_unknown job is still visible")
	}
}

func TestDeleteJobKeepsResultUnknownWithLeaseBusy(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "unknown-leased", request)
	if err != nil {
		t.Fatal(err)
	}
	markResultUnknown(t, s, scope, job.ID, true, "")
	assertEngineError(t, s.store.DeleteJob(ctx, scope, job.ID), CodeProjectBusy)
}

func TestDeleteJobKeepsReconcilingJobBusy(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "unknown-reconcile", request)
	if err != nil {
		t.Fatal(err)
	}
	markResultUnknown(t, s, scope, job.ID, false, "provider-handle")
	reopened, err := s.ReconcileJob(ctx, scope, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status == JobResultUnknown {
		t.Fatalf("reconcile did not reopen the job: %s", reopened.Status)
	}
	assertEngineError(t, s.store.DeleteJob(ctx, scope, job.ID), CodeProjectBusy)
}

func TestDeleteProjectNotBlockedByResultUnknownJob(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "unknown-in-project", request)
	if err != nil {
		t.Fatal(err)
	}
	markResultUnknown(t, s, scope, job.ID, false, "")
	project, err := s.store.ReadProject(scope, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.DeleteProject(ctx, scope, project.ID, project.Revision); err != nil {
		t.Fatalf("a result_unknown job must not block deleting its project: %v", err)
	}
	if _, err = s.store.ReadJob(scope, job.ID); err == nil {
		t.Fatal("job of the deleted project is still visible")
	}
}
