package imaging

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecoverCrashBoundariesNeverResubmit(t *testing.T) {
	for _, point := range []string{"accepted", "submitting", "handle-unsaved", "pending", "file-unreferenced", "terminal"} {
		t.Run(point, func(t *testing.T) {
			s, _, a, scope, r := engineFixture(t)
			j, _, err := s.CreateJob(context.Background(), scope, "key", r)
			if err != nil {
				t.Fatal(err)
			}
			id := j.Items[0].ID
			if point != "accepted" {
				err = s.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
					j, _ = tx.ReadJob(scope, j.ID)
					ex, _ := tx.ReadExecution(scope, j.ID)
					x := ex.Items[id]
					x.Attempts = []ImageAttempt{{AttemptNo: 1, SubmissionState: "submitting"}}
					j.Items[0].AttemptCount = 1
					j.Items[0].Status = JobRunning
					j.Status = JobRunning
					x.LeaseOwner = "dead-process"
					x.LeaseExpiresAt = time.Now().Add(time.Hour)
					if point == "pending" {
						x.ProviderHandle = "opaque-handle"
						x.NextPollAt = time.Now().Add(time.Hour)
					}
					if point == "terminal" {
						j.Items[0].Status = JobFailed
						j.Status = JobFailed
					}
					if point == "file-unreferenced" {
						j.Items[0].Status = JobSaving
						j.Status = JobSaving
					}
					ex.Items[id] = x
					if err := tx.PutExecution(ex); err != nil {
						return err
					}
					return s.writeJob(tx, &j, jobEvent(j.Status))
				})
				if err != nil {
					t.Fatal(err)
				}
				a.submits = 1
			}
			a.reconcile = func(context.Context, string) (RemoteResult, error) {
				return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("recovered")}}}, nil
			}
			dir := s.store.dir
			if err = s.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			fresh := NewService(reopened, s.registry, nil, s.options)
			q := NewScheduler(fresh)
			if err = q.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer q.Close(context.Background())
			want := JobSucceeded
			if point == "terminal" {
				want = JobFailed
			}
			result := awaitEngine(t, fresh, scope, j.ID, want)
			sub, polls, reconcile, _ := a.counts()
			if sub != 1 {
				t.Fatalf("duplicate charge: %d", sub)
			}
			if point == "pending" && (polls != 1 || reconcile != 0) {
				t.Fatal(polls, reconcile)
			}
			if point == "terminal" && (polls != 0 || reconcile != 0) {
				t.Fatal(polls, reconcile)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "opaque-handle") || strings.Contains(string(raw), "dead-process") {
				t.Fatal(string(raw))
			}
		})
	}
}
func TestRecoverShutdownSubmittingUnknown(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	entered := make(chan struct{})
	a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		close(entered)
		<-ctx.Done()
		return RemoteResult{}, ctx.Err()
	}
	j, _, err := s.CreateJob(context.Background(), scope, "key", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	<-entered
	if err = q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ex, err := s.store.ReadExecution(scope, j.ID)
	if err != nil || len(ex.Items[j.Items[0].ID].Attempts) != 1 {
		t.Fatal(ex, err)
	}
	fresh := NewService(s.store, s.registry, nil, s.options)
	other := NewScheduler(fresh)
	if err = other.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	awaitEngine(t, fresh, scope, j.ID, JobResultUnknown)
	sub, _, rec, _ := a.counts()
	if sub != 1 || rec != 1 {
		t.Fatal(sub, rec)
	}
}
