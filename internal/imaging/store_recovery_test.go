package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreExecutionPrivateAndDurable(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	seedStore(t, s)
	err := s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobRunning
		j.Items = []ImageItem{{ID: "ii_one", Status: JobRunning, AttemptCount: 1}}
		if e = tx.PutJob(j); e != nil {
			return e
		}
		if e = tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobStateChanged, Data: ImageEventData{Status: JobRunning}}); e != nil {
			return e
		}
		return tx.PutExecution(ExecutionRecord{JobID: j.ID, Deadline: time.Unix(1000, 0), Items: map[string]ItemExecution{"ii_one": {ProviderHandle: "private-handle", SubmissionKey: "private-key", Continuation: []byte("private-continuation"), LeaseOwner: "worker", LeaseExpiresAt: time.Unix(60, 0), NextPollAt: time.Unix(20, 0), QueryFailures: 2, DownloadAttempts: 1, Attempts: []ImageAttempt{{AttemptNo: 1, StartedAt: time.Unix(10, 0), SubmissionState: "submitting", ProviderHandle: "private-attempt-handle", RequestID: "private-request"}}}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = mustStore(t, dir)
	e, err := s.ReadExecution(storeScope, "ij_one")
	if err != nil {
		t.Fatal(err)
	}
	item := e.Items["ii_one"]
	if item.ProviderHandle != "private-handle" || string(item.Continuation) != "private-continuation" || item.Attempts[0].ProviderHandle != "private-attempt-handle" || item.QueryFailures != 2 || item.LeaseOwner != "worker" || !e.Deadline.Equal(time.Unix(1000, 0)) {
		t.Fatalf("lost execution %+v", e)
	}
	item.Continuation[0] = 'x'
	again, _ := s.ReadExecution(storeScope, "ij_one")
	if string(again.Items["ii_one"].Continuation) != "private-continuation" {
		t.Fatal("execution aliased")
	}
	if _, err = json.Marshal(e); err == nil {
		t.Fatal("private record serialized")
	}
	job, _ := s.ReadJob(storeScope, "ij_one")
	b, err := json.Marshal(job)
	if err != nil || strings.Contains(string(b), "private-") || strings.Contains(string(b), "leaseOwner") {
		t.Fatal("private data escaped", string(b), err)
	}
}
func TestStoreRollbackDeepCopy(t *testing.T) {
	s := mustStore(t, t.TempDir())
	seedStore(t, s)
	var captured *Transaction
	err := s.Update(storeContext(), func(tx *Transaction) error {
		captured = tx
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Revision++
		j.Request.Parameters["nested"].(map[string]any)["v"] = "rollback"
		if e = tx.PutJob(j); e != nil {
			return e
		}
		if e = tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobStateChanged}); e != nil {
			return e
		}
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("abort ignored")
	}
	if err = captured.PutProject(ImageProject{ID: "ip_late", Revision: 1}); err == nil {
		t.Fatal("retained transaction usable")
	}
	j, _ := s.ReadJob(storeScope, "ij_one")
	ev, _ := s.ReadEvents(context.Background(), storeScope, j.ID, 0, 100)
	if j.Revision != 1 || len(ev) != 1 || j.Request.Parameters["nested"].(map[string]any)["v"] != "original" {
		t.Fatal("rollback polluted nested state")
	}
}
func TestStoreFaultBoundariesReopen(t *testing.T) {
	for _, point := range []string{"wal.before_append", "wal.after_append", "wal.before_sync", "wal.after_sync"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			s := mustStore(t, dir)
			seedStore(t, s)
			s.fault = func(p string) error {
				if p == point {
					return errors.New("fault")
				}
				return nil
			}
			e := s.Update(storeContext(), func(tx *Transaction) error { return tx.PutProject(ImageProject{ID: "ip_failed", Revision: 1}) })
			if e == nil {
				t.Fatal("fault hidden")
			}
			s.Close()
			s = mustStore(t, dir)
			if _, e = s.ReadProject(storeScope, "ip_failed"); e == nil {
				t.Fatal("failed tx replayed")
			}
			if _, e = s.ReadJob(storeScope, "ij_one"); e != nil {
				t.Fatal("earlier commit lost", e)
			}
		})
	}
}
func TestSnapshotThresholds(t *testing.T) {
	for _, mode := range []string{"transactions", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s := mustStore(t, dir)
			if s.snapshotTransactions != 1000 || s.snapshotBytes != 64<<20 {
				t.Fatal("wrong default thresholds")
			}
			if mode == "bytes" {
				s.snapshotBytes = 1
			} else {
				s.snapshotTransactions = 2
			}
			seedStore(t, s)
			if mode == "transactions" {
				if s.checkpointSeq != 0 {
					t.Fatal("early checkpoint")
				}
				if e := s.Update(storeContext(), func(tx *Transaction) error { return tx.PutProject(ImageProject{ID: "ip_two", Revision: 1}) }); e != nil {
					t.Fatal(e)
				}
			}
			if s.checkpointSeq != s.seq || s.walBytes != 0 {
				t.Fatal("threshold checkpoint absent")
			}
			s.Close()
			s = mustStore(t, dir)
			if _, e := s.ReadJob(storeScope, "ij_one"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestSnapshotActualThousandTransactions(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	for i := 0; i < 1000; i++ {
		if err := s.Update(storeContext(), func(tx *Transaction) error {
			p := ImageProject{ID: "ip_counter", Revision: uint64(i + 1)}
			return tx.PutProject(p)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if s.checkpointSeq != 1000 || s.walBytes != 0 {
		t.Fatalf("checkpoint seq %d bytes %d", s.checkpointSeq, s.walBytes)
	}
	s.Close()
	s = mustStore(t, dir)
	p, e := s.ReadProject(storeScope, "ip_counter")
	if e != nil || p.Revision != 1000 {
		t.Fatal(p, e)
	}
}
func TestSnapshotFailureDoesNotFailDurableTransaction(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	s.snapshotTransactions = 1
	s.fault = func(p string) error {
		if p == "snapshot.before_rename" {
			return errors.New("fault")
		}
		return nil
	}
	seedStore(t, s)
	if s.MaintenanceError() == nil {
		t.Fatal("maintenance fault hidden")
	}
	s.Close()
	s = mustStore(t, dir)
	if _, e := s.ReadJob(storeScope, "ij_one"); e != nil {
		t.Fatal(e)
	}
}
func TestRecoveryRejectsCompleteCorruptRecords(t *testing.T) {
	for _, mode := range []string{"checksum-last", "checksum-middle", "complete-malformed", "version", "sequence", "snapshot", "missing-snapshot", "missing-wal", "bad-mutation"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s := mustStore(t, dir)
			seedStore(t, s)
			if mode == "missing-wal" {
				if e := s.Checkpoint(); e != nil {
					t.Fatal(e)
				}
			}
			s.Close()
			path := filepath.Join(dir, "transactions.wal")
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "checksum-last", "checksum-middle":
				b = []byte(strings.Replace(string(b), "one", "bad", 1))
				if mode == "checksum-middle" {
					extra, _ := encodeRecord(2, transactionPayload{Version: 1, Mutations: []storeMutation{{Kind: "project", Project: &projectRecord{Scope: owner(storeScope), Value: ImageProject{ID: "ip_two", Revision: 1}}}}})
					b = append(b, extra...)
				}
			case "complete-malformed":
				b = append(b, []byte("{bad}\n")...)
			case "version":
				extra, _ := encodeRecord(2, transactionPayload{Version: 2, Mutations: []storeMutation{{Kind: "future"}}})
				b = append(b, extra...)
			case "sequence":
				extra, _ := encodeRecord(3, transactionPayload{Version: 1, Mutations: []storeMutation{{Kind: "project", Project: &projectRecord{Scope: owner(storeScope), Value: ImageProject{ID: "ip_two", Revision: 1}}}}})
				b = append(b, extra...)
			case "bad-mutation":
				extra, _ := encodeRecord(2, transactionPayload{Version: 1, Mutations: []storeMutation{{Kind: "future"}}})
				b = append(b, extra...)
			case "snapshot":
				path = filepath.Join(dir, "snapshot.json")
				b = []byte("{}\n")
			case "missing-snapshot":
				if e = os.Remove(filepath.Join(dir, "snapshot.json")); e != nil {
					t.Fatal(e)
				}
			case "missing-wal":
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
			}
			if mode != "missing-snapshot" && mode != "missing-wal" {
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			broken, e := OpenStore(dir)
			if e == nil {
				broken.Close()
				t.Fatal("corrupt store opened")
			}
			if !strings.Contains(e.Error(), CodeStoreCorrupt) {
				t.Fatal(e)
			}
			if mode != "missing-snapshot" && mode != "missing-wal" {
				after, _ := os.ReadFile(path)
				if string(after) != string(b) {
					t.Fatal("corrupt evidence rewritten")
				}
			}
		})
	}
}
func TestIdempotencyConcurrentCreatesOneJob(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	p, e := s.CreateProject(context.Background(), storeScope, "one", "")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := s.Update(storeContext(), func(tx *Transaction) error {
				if r, ok := tx.GetIdempotency(storeScope, "same"); ok {
					if r.Hash != "hash" {
						return errors.New("hash changed")
					}
					return nil
				}
				id := fmt.Sprintf("ij_%d", i)
				if e := tx.PutJob(ImageJob{ID: id, ProjectID: p.ID, Revision: 1, Status: JobQueued}); e != nil {
					return e
				}
				if e := tx.AppendEvent(ImageEvent{JobID: id, Type: EventJobAccepted}); e != nil {
					return e
				}
				return tx.PutIdempotency(storeScope, "same", "hash", id)
			})
			if e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	s.Close()
	s = mustStore(t, dir)
	jobs, e := s.ListJobs(context.Background(), storeScope, p.ID, PageRequest{Limit: 100})
	if e != nil || len(jobs.Items) != 1 {
		t.Fatal("job count", len(jobs.Items), e)
	}
	events, e := s.ReadEvents(context.Background(), storeScope, jobs.Items[0].ID, 0, 100)
	if e != nil || len(events) != 1 || events[0].Type != EventJobAccepted {
		t.Fatal("accepted count", events, e)
	}
}
func TestEventsGapAndSnapshotPosition(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	seedStore(t, s)
	base = base.Add(31 * 24 * time.Hour)
	e := s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobRunning
		if e = tx.PutJob(j); e != nil {
			return e
		}
		if e = tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobStateChanged}); e != nil {
			return e
		}
		return tx.PruneEvents(j.ID)
	})
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	s = mustStore(t, dir)
	_, pos, e := s.ReadJobSnapshot(storeScope, "ij_one")
	if e != nil || pos.FirstAvailableSeq != 2 || pos.LastEventSeq != 2 {
		t.Fatal(pos, e)
	}
	_, e = s.ReadEvents(context.Background(), storeScope, "ij_one", 0, 100)
	var imageErr *ImageError
	if !errors.As(e, &imageErr) || imageErr.Code != CodeEventGap || *imageErr.Details.FirstAvailableSeq != 2 {
		t.Fatal("gap absent", e)
	}
	ev, e := s.ReadEvents(context.Background(), storeScope, "ij_one", 1, 100)
	if e != nil || len(ev) != 1 || ev[0].Seq != 2 {
		t.Fatal(ev, e)
	}
	if _, e = s.ReadEvents(context.Background(), storeScope, "ij_one", 3, 100); e == nil {
		t.Fatal("future seq accepted")
	}
}
func TestEventsRequireMatchingJobTransaction(t *testing.T) {
	s := mustStore(t, t.TempDir())
	seedStore(t, s)
	err := s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobRunning
		return tx.PutJob(j)
	})
	if err == nil {
		t.Fatal("job without event accepted")
	}
	j, _ := s.ReadJob(storeScope, "ij_one")
	if j.Revision != 1 {
		t.Fatal("failed tx published")
	}
	if err = s.Update(storeContext(), func(tx *Transaction) error {
		return tx.AppendEvent(ImageEvent{JobID: "ij_one", Type: EventJobAccepted})
	}); err == nil {
		t.Fatal("duplicate accepted event")
	}
}
