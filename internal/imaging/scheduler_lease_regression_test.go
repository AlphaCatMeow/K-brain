package imaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitLeaseTestSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reach barrier")
	}
}

func TestSchedulerCancelledWorkerBlocksDeletionUntilOutputPublished(t *testing.T) {
	for _, deleteProject := range []bool{false, true} {
		name := "job"
		if deleteProject {
			name = "project"
		}
		t.Run(name, func(t *testing.T) {
			s, q, a, scope, request := engineFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			saving, publish := make(chan struct{}), make(chan struct{})
			var releaseOnce, publishOnce, savingOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); publishOnce.Do(func() { close(publish) }) })
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				close(entered)
				<-release
				return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("output")}}}, nil
			}
			var sink engineSink
			s.options.AssetSink = engineSinkFunc(func(ctx context.Context, scope Scope, output OutputSource) (ImageAsset, error) {
				asset, err := sink.SaveOutput(ctx, scope, output)
				savingOnce.Do(func() { close(saving) })
				<-publish
				return asset, err
			})
			job, _, err := s.CreateJob(context.Background(), scope, "cancel-delete", request)
			if err != nil {
				t.Fatal(err)
			}
			project, err := s.store.ReadProject(scope, request.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if err = q.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitLeaseTestSignal(t, entered)
			job, err = s.CancelJob(context.Background(), scope, job.ID)
			if err != nil || job.Status != JobCancelled || job.CancelEffect != "local_only" {
				t.Fatalf("cancel: %+v, %v", job, err)
			}
			// Expiration alone does not mean a worker has stopped using the job.
			if err = s.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
				ex, err := tx.ReadExecution(scope, job.ID)
				if err != nil {
					return err
				}
				x := ex.Items[job.Items[0].ID]
				x.LeaseExpiresAt = time.Now().Add(-time.Hour)
				ex.Items[job.Items[0].ID] = x
				return tx.PutExecution(ex)
			}); err != nil {
				t.Fatal(err)
			}
			assertBusy := func() {
				t.Helper()
				assertEngineError(t, s.store.DeleteJob(context.Background(), scope, job.ID), CodeProjectBusy)
				assertEngineError(t, s.store.DeleteProject(context.Background(), scope, project.ID, project.Revision), CodeProjectBusy)
				ex, err := s.store.ReadExecution(scope, job.ID)
				if err != nil || ex.Items[job.Items[0].ID].LeaseOwner != q.owner {
					t.Fatalf("lost live lease: %+v %v", ex, err)
				}
			}
			assertBusy()
			releaseOnce.Do(func() { close(release) })
			waitLeaseTestSignal(t, saving)
			assertBusy()
			publishOnce.Do(func() { close(publish) })
			engineEventually(t, func() bool {
				ex, err := s.store.ReadExecution(scope, job.ID)
				return err == nil && ex.Items[job.Items[0].ID].LeaseOwner == ""
			})
			job, err = s.GetJob(context.Background(), scope, job.ID)
			if err != nil || job.Status != JobCancelled || len(job.AssetIDs) != 1 {
				t.Fatalf("late output: %+v %v", job, err)
			}
			if deleteProject {
				err = s.store.DeleteProject(context.Background(), scope, project.ID, project.Revision)
			} else {
				err = s.store.DeleteJob(context.Background(), scope, job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Submit another project through the same scheduler after deletion.
			a.mu.Lock()
			a.submit = nil
			a.mu.Unlock()
			nextProject, err := s.store.CreateProject(context.Background(), scope, "next", "")
			if err != nil {
				t.Fatal(err)
			}
			request.ProjectID = nextProject.ID
			next, _, err := s.CreateJob(context.Background(), scope, "next", request)
			if err != nil {
				t.Fatal(err)
			}
			awaitEngine(t, s, scope, next.ID, JobSucceeded)
		})
	}
}

func TestSchedulerStaleDeletedWorkDoesNotFail(t *testing.T) {
	s, q, _, scope, request := engineFixture(t)
	job, _, err := s.CreateJob(context.Background(), scope, "stale", request)
	if err != nil {
		t.Fatal(err)
	}
	work := s.executionWork()[0]
	if _, err = s.CancelJob(context.Background(), scope, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.store.DeleteJob(context.Background(), scope, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = q.execute(context.Background(), work); err != nil {
		t.Fatalf("stale claim failed: %v", err)
	}
	q.running[work.key()] = work
	if err = q.renew(); err != nil {
		t.Fatalf("stale renewal failed: %v", err)
	}
	delete(q.running, work.key())
}

func TestSchedulerWorkerExitReleasesLeaseOnEarlyReturns(t *testing.T) {
	for _, mode := range []string{"shutdown", "finish-error", "cancel-already-terminal"} {
		t.Run(mode, func(t *testing.T) {
			s, q, a, scope, request := engineFixture(t)
			job, _, err := s.CreateJob(context.Background(), scope, "exit", request)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("adapter shutdown")
			switch mode {
			case "shutdown":
				a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) { cancel(); return RemoteResult{}, sentinel }
			case "finish-error":
				if err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error { return tx.PutAsset(ImageAsset{ID: "ia_collision", Hash: "original"}) }); err != nil {
					t.Fatal(err)
				}
				s.options.AssetSink = engineSinkFunc(func(context.Context, Scope, OutputSource) (ImageAsset, error) {
					return ImageAsset{ID: "ia_collision", Hash: "conflicting"}, nil
				})
			case "cancel-already-terminal":
				err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
					j, err := tx.ReadJob(scope, job.ID)
					if err != nil {
						return err
					}
					ex, err := tx.ReadExecution(scope, job.ID)
					if err != nil {
						return err
					}
					x := ex.Items[j.Items[0].ID]
					x.ProviderHandle = "remote"
					ex.Items[j.Items[0].ID] = x
					j.Status = JobCancelRequested
					j.Items[0].Status = JobRunning
					if err = tx.PutExecution(ex); err != nil {
						return err
					}
					return s.writeJob(tx, &j, EventJobStateChanged)
				})
				if err != nil {
					t.Fatal(err)
				}
				a.cancel = func(context.Context, string) error {
					return s.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
						j, err := tx.ReadJob(scope, job.ID)
						if err != nil {
							return err
						}
						j.Status, j.Items[0].Status = JobCancelled, JobCancelled
						return s.writeJob(tx, &j, EventJobStateChanged)
					})
				}
			}
			err = q.execute(ctx, s.executionWork()[0])
			if mode == "finish-error" {
				if err == nil {
					t.Fatal("finish failure was hidden")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			ex, err := s.store.ReadExecution(scope, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			x := ex.Items[job.Items[0].ID]
			if x.LeaseOwner != "" || !x.LeaseExpiresAt.IsZero() {
				t.Fatalf("lease leaked on %s: %+v", mode, x)
			}
		})
	}
}
