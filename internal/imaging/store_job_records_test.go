package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStoreJobRecordsAtomicPrivateRecovery(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	seedStore(t, s)
	r, err := s.GetJobRecord(storeScope, "ij_one")
	if err != nil || r.Execution.JobID != "ij_one" || len(r.Execution.Items) != 0 {
		t.Fatal("new job execution view", r, err)
	}
	err = s.Update(storeContext(), func(tx *Transaction) error {
		r, err := tx.GetJobRecord(storeScope, "ij_one")
		if err != nil {
			return err
		}
		r.Job.Revision++
		r.Job.Status = JobRunning
		r.Job.Items = []ImageItem{{ID: "ii_one", Status: JobRunning, AttemptCount: 1}}
		r.Execution.Deadline = time.Unix(100, 0)
		r.Execution.Items["ii_one"] = ItemExecution{ProviderHandle: "private-handle", Continuation: []byte("private-continuation"), Attempts: []ImageAttempt{{AttemptNo: 1, SubmissionState: "submitting", ProviderHandle: "private-attempt"}}}
		if err = tx.PutJobRecord(r); err != nil {
			return err
		}
		return tx.AppendEvent(ImageEvent{JobID: r.Job.ID, Type: EventJobStateChanged, Data: ImageEventData{Status: JobRunning}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = mustStore(t, dir)
	r, err = s.GetJobRecord(storeScope, "ij_one")
	if err != nil || r.Job.Revision != 2 || r.Execution.Items["ii_one"].ProviderHandle != "private-handle" || r.Execution.Items["ii_one"].Attempts[0].ProviderHandle != "private-attempt" {
		t.Fatal("checkpoint lost private record", err)
	}
	if _, err = json.Marshal(r); err == nil {
		t.Fatal("private JobRecord serialized")
	}
	page, err := s.ListJobRecords(context.Background(), storeScope, "", PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("private listing", err)
	}
	if _, err = json.Marshal(page); err == nil {
		t.Fatal("private page serialized")
	}
	public, err := json.Marshal(r.Job)
	if err != nil || strings.Contains(string(public), "private-") {
		t.Fatal("private data leaked into public job", err)
	}
	for _, other := range []Scope{{OwnerID: "other", BackendID: storeScope.BackendID}, {OwnerID: storeScope.OwnerID, BackendID: "other"}} {
		if _, err = s.GetJobRecord(other, r.Job.ID); err == nil {
			t.Fatal("private record scope leak")
		}
		if err = s.Update(WithScope(context.Background(), other), func(tx *Transaction) error {
			_, err := tx.GetJobRecord(storeScope, r.Job.ID)
			return err
		}); err == nil {
			t.Fatal("transaction scope bypass")
		}
	}
	r.Execution.Items["ii_one"].Continuation[0] = 'X'
	read, _ := s.GetJobRecord(storeScope, r.Job.ID)
	if string(read.Execution.Items["ii_one"].Continuation) != "private-continuation" {
		t.Fatal("private view aliases durable memory")
	}
	err = s.Update(storeContext(), func(tx *Transaction) error {
		r, err := tx.GetJobRecord(storeScope, "ij_one")
		if err != nil {
			return err
		}
		r.Job.Revision++
		r.Execution.Items["ii_one"] = ItemExecution{ProviderHandle: "rolled-back"}
		if err = tx.PutJobRecord(r); err != nil {
			return err
		}
		if err = tx.AppendEvent(ImageEvent{JobID: r.Job.ID, Type: EventJobStateChanged}); err != nil {
			return err
		}
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("abort ignored")
	}
	r, _ = s.GetJobRecord(storeScope, "ij_one")
	if r.Job.Revision != 2 || r.Execution.Items["ii_one"].ProviderHandle != "private-handle" {
		t.Fatal("private transaction partially committed")
	}
}

func TestPaginationPrivateRecordsScopeAndKind(t *testing.T) {
	s := mustStore(t, t.TempDir())
	seedStore(t, s)
	if err := s.Update(storeContext(), func(tx *Transaction) error {
		if err := tx.PutJobRecord(JobRecord{Job: ImageJob{ID: "ij_two", ProjectID: "ip_one", Revision: 1, Status: JobQueued, CreatedAt: time.Unix(3, 0)}, Execution: ExecutionRecord{JobID: "ij_two"}}); err != nil {
			return err
		}
		return tx.AppendEvent(ImageEvent{JobID: "ij_two", Type: EventJobAccepted})
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	page, err := s.ListJobRecords(ctx, storeScope, "ip_one", PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Job.ID != "ij_one" || page.NextCursor == "" {
		t.Fatal("first private page", err)
	}
	if _, err = s.ListJobs(ctx, storeScope, "ip_one", PageRequest{Limit: 1, Cursor: page.NextCursor}); err == nil {
		t.Fatal("private cursor accepted for public job listing")
	}
	if _, err = s.ListJobRecords(ctx, Scope{OwnerID: "other", BackendID: storeScope.BackendID}, "ip_one", PageRequest{Limit: 1, Cursor: page.NextCursor}); err == nil {
		t.Fatal("private cursor crossed scope")
	}
	page, err = s.ListJobRecords(ctx, storeScope, "ip_one", PageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Items) != 1 || page.Items[0].Job.ID != "ij_two" || page.NextCursor != "" {
		t.Fatal("second private page", err)
	}
}

// A panic skips append rollback and models an abrupt process boundary. Recovery
// must replay a synced record exactly once even if memory was not yet published.
func TestRecoveryAbruptWALAfterSync(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	s.fault = func(point string) error {
		if point == "wal.after_sync" {
			panic("simulated crash")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("crash boundary not reached")
			}
		}()
		seedStore(t, s)
	}()
	s.Close()
	s = mustStore(t, dir)
	job, err := s.ReadJob(storeScope, "ij_one")
	if err != nil {
		t.Fatal("synced job lost", err)
	}
	events, err := s.ReadEvents(context.Background(), storeScope, job.ID, 0, 100)
	if err != nil || len(events) != 1 || events[0].Revision != job.Revision {
		t.Fatal("synced job/event not atomic", err)
	}
	if _, ok := s.GetIdempotency(storeScope, "key"); !ok {
		t.Fatal("synced idempotency lost")
	}
	if err = s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = mustStore(t, dir)
	events, err = s.ReadEvents(context.Background(), storeScope, job.ID, 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal("checkpoint duplicated replayed event", err)
	}
}
