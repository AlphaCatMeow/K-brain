package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func maintenanceWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance worker did not make progress")
	}
}

func TestImageMaintenanceDefaultDelayAndParentCancellation(t *testing.T) {
	if imageMaintenanceInterval < time.Hour {
		t.Fatal("production maintenance runs too frequently")
	}
	// Nil resources would fail immediately if startup attempted maintenance.
	ctx, cancel := context.WithCancel(context.Background())
	m := startImageMaintenance(ctx, nil, nil)
	cancel()
	maintenanceWait(t, m.done)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	m = startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{
		prune:   func(context.Context) error { calls.Add(1); return nil },
		collect: func(context.Context) error { calls.Add(1); return nil },
	})
	if err := m.Close(context.Background()); err != nil || calls.Load() != 0 {
		t.Fatalf("startup did work: calls=%d err=%v", calls.Load(), err)
	}
}

func TestImageMaintenanceCloseWaitsAndCanRetry(t *testing.T) {
	ticks := make(chan time.Time, 1)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var gcCalls atomic.Int32
	m := startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{
		ticks: ticks,
		prune: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release // Model an uninterruptible filesystem operation finishing.
			return ctx.Err()
		},
		collect: func(context.Context) error { gcCalls.Add(1); return nil },
	})
	defer func() { close(release); _ = m.Close(context.Background()) }()
	ticks <- time.Now()
	maintenanceWait(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close returned before worker exited: %v", err)
	}
	maintenanceWait(t, cancelled)
	select {
	case <-m.done:
		t.Fatal("resource safety barrier released before worker exit")
	default:
	}
	if gcCalls.Load() != 0 {
		t.Fatal("GC started after cancellation")
	}
}

func TestImageMaintenanceConcurrentClose(t *testing.T) {
	m := startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestImageMaintenanceErrorsAndPanicsRecoverWithoutLeaking(t *testing.T) {
	ticks := make(chan time.Time)
	finished := make(chan struct{}, 3)
	var logs []string
	calls := 0
	m := startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{
		ticks: ticks,
		prune: func(context.Context) error {
			calls++
			switch calls {
			case 1:
				return errors.New("secret-token private-owner C:/private/path")
			case 2:
				panic("secret-token private-owner C:/private/path")
			}
			return nil
		},
		collect: func(context.Context) error { finished <- struct{}{}; return nil },
		logf:    func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	})
	defer m.Close(context.Background())
	for i := 0; i < 3; i++ {
		ticks <- time.Now()
		maintenanceWait(t, finished)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(logs) != 2 {
		t.Fatalf("worker failed to recover: calls=%d logs=%v", calls, logs)
	}
	for _, line := range logs {
		if strings.Contains(line, "secret-token") || strings.Contains(line, "private") || !strings.Contains(line, "events failed") {
			t.Fatalf("unsafe or unhelpful diagnostic: %q", line)
		}
	}
}

func TestImageMaintenanceShortInterval(t *testing.T) {
	called := make(chan struct{}, 1)
	m := startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{
		interval: time.Millisecond,
		prune:    func(context.Context) error { return nil },
		collect: func(context.Context) error {
			select {
			case called <- struct{}{}:
			default:
			}
			return nil
		},
	})
	defer m.Close(context.Background())
	maintenanceWait(t, called)
}

func TestImageMaintenanceEventRetentionScopePaginationAndIdempotency(t *testing.T) {
	store, err := imaging.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local := imaging.Scope{OwnerID: "local", BackendID: "local"}
	foreign := imaging.Scope{OwnerID: "other", BackendID: "local"}
	now := time.Now().UTC()
	for _, scope := range []imaging.Scope{local, foreign} {
		err := store.Update(imaging.WithScope(context.Background(), scope), func(tx *imaging.Transaction) error {
			project := "ip_" + scope.OwnerID
			if err := tx.PutProject(imaging.ImageProject{ID: project, Title: "test", Revision: 1, CreatedAt: now}); err != nil {
				return err
			}
			for i := 0; i < 101; i++ {
				id := fmt.Sprintf("ij_%s_%03d", scope.OwnerID, i)
				if err := tx.PutJob(imaging.ImageJob{ID: id, ProjectID: project, Revision: 1, Status: imaging.JobSucceeded, CreatedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
					return err
				}
				for n, age := range []time.Duration{31 * 24 * time.Hour, 29 * 24 * time.Hour, 40 * 24 * time.Hour} {
					kind := imaging.EventJobStateChanged
					if n == 0 {
						kind = imaging.EventJobAccepted
					}
					if err := tx.AppendEvent(imaging.ImageEvent{JobID: id, Type: kind, Timestamp: now.Add(-age)}); err != nil {
						return err
					}
				}
				if err := tx.PutIdempotency(scope, id, "hash", id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneImageRuntimeEvents(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []imaging.Scope{local, foreign} {
		for i := 0; i < 101; i++ {
			id := fmt.Sprintf("ij_%s_%03d", scope.OwnerID, i)
			_, position, err := store.ReadJobSnapshot(scope, id)
			wantFirst := uint64(1)
			if scope == local {
				wantFirst = 2
			}
			if err != nil || position.FirstAvailableSeq != wantFirst || position.LastEventSeq != 3 {
				t.Fatalf("retention changed scope, suffix or sequence: %s %+v %v", id, position, err)
			}
			if record, ok := store.GetIdempotency(scope, id); !ok || record.JobID != id {
				t.Fatal("maintenance changed idempotency")
			}
		}
	}
}

func TestImageMaintenanceGCUsesDefaultGrace(t *testing.T) {
	root := t.TempDir()
	store, err := imaging.OpenStore(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := filepath.Join(root, "assets")
	assets, err := imaging.NewAssetStore(store, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	file := filepath.Join(dir, "tmp_"+strings.Repeat("a", 32))
	if err := os.WriteFile(file, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	ticks, finished := make(chan time.Time), make(chan struct{}, 1)
	var result imaging.AssetGCResult
	var gcErr error
	m := startImageMaintenanceWithOptions(context.Background(), imageMaintenanceOptions{
		ticks: ticks,
		prune: func(context.Context) error { return nil },
		collect: func(ctx context.Context) error {
			result, gcErr = assets.CollectGarbage(ctx, imaging.AssetGCOptions{Now: func() time.Time { return now }})
			finished <- struct{}{}
			return gcErr
		},
	})
	defer m.Close(context.Background())
	for i, advance := range []time.Duration{0, 23 * time.Hour, time.Hour} {
		now = now.Add(advance)
		ticks <- time.Now()
		maintenanceWait(t, finished)
		if gcErr != nil {
			t.Fatal(gcErr)
		}
		_, err := os.Stat(file)
		if i < 2 && err != nil {
			t.Fatal("deleted before default grace", err)
		}
		if i == 2 && (!errors.Is(err, os.ErrNotExist) || result.RemovedFiles != 1) {
			t.Fatalf("second-stage sweep missing: %+v %v", result, err)
		}
	}
}
