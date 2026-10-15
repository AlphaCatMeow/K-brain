package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const imageMaintenanceInterval = time.Hour

// imageMaintenance owns only its worker, never the store or asset handles. Close
// must succeed before either handle is closed; a timeout can be retried safely.
type imageMaintenance struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Test seams are private: production always waits at least an hour before its
// first pass and uses the retention policies implemented by imaging itself.
type imageMaintenanceOptions struct {
	interval time.Duration
	ticks    <-chan time.Time
	prune    func(context.Context) error
	collect  func(context.Context) error
	logf     func(string, ...any)
}

func startImageMaintenance(ctx context.Context, store *imaging.Store, assets *imaging.AssetStore) *imageMaintenance {
	return startImageMaintenanceWithOptions(ctx, imageMaintenanceOptions{
		prune: func(ctx context.Context) error { return pruneImageRuntimeEvents(ctx, store) },
		collect: func(ctx context.Context) error {
			// Keep imaging's defaults: uploads 7d, tombstones 30d, orphan files
			// 24h, and durable mark -> sweep with a separate 24h grace period.
			_, err := assets.CollectGarbage(ctx, imaging.AssetGCOptions{})
			return err
		},
	})
}

func startImageMaintenanceWithOptions(parent context.Context, o imageMaintenanceOptions) *imageMaintenance {
	ctx, cancel := context.WithCancel(parent)
	m := &imageMaintenance{cancel: cancel, done: make(chan struct{})}
	if o.interval <= 0 {
		o.interval = imageMaintenanceInterval
	}
	if o.logf == nil {
		o.logf = log.Printf
	}
	go func() {
		defer close(m.done)
		// A timer restarted after each pass prevents overlap and catch-up storms.
		timer := time.NewTimer(o.interval)
		defer timer.Stop()
		ticks := o.ticks
		if ticks == nil {
			ticks = timer.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticks:
				if !ok || ctx.Err() != nil {
					return
				}
			}
			imageMaintenanceStep(ctx, "events", o.prune, o.logf)
			if ctx.Err() != nil {
				return
			}
			imageMaintenanceStep(ctx, "gc", o.collect, o.logf)
			if o.ticks == nil {
				timer.Reset(o.interval)
			}
		}
	}()
	return m
}

func imageMaintenanceStep(ctx context.Context, phase string, run func(context.Context) error, logf func(string, ...any)) {
	// Neither error strings nor panic values are safe: they can contain paths,
	// provider payloads, owner identities or credentials. Log fixed labels only.
	defer func() {
		if recover() != nil {
			logf("image maintenance %s failed (panic); retrying next interval", phase)
		}
	}()
	if err := run(ctx); err != nil && ctx.Err() == nil {
		logf("image maintenance %s failed; retrying next interval", phase)
	}
}

func (m *imageMaintenance) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		select {
		case <-m.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

// The current runtime authenticates local/local only. ListJobs excludes deleted
// jobs; their events, and other scopes' events, are intentionally retained. A
// future multi-owner runtime needs a trusted store-wide maintenance enumeration,
// not identities inferred from requests or raw on-disk files. GC already has its
// own store-wide reference checks and is deliberately not scoped here.
func pruneImageRuntimeEvents(ctx context.Context, store *imaging.Store) error {
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	page := imaging.PageRequest{Limit: 100}
	for {
		jobs, err := store.ListJobs(ctx, scope, "", page)
		if err != nil {
			return err
		}
		for _, job := range jobs.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := store.Update(imaging.WithScope(ctx, scope), func(tx *imaging.Transaction) error {
				// PruneEvents keeps its existing 30-day contiguous-prefix policy.
				// Do not prune jobs, executions or idempotency records here.
				return tx.PruneEvents(job.ID)
			})
			var imageErr *imaging.ImageError
			if errors.As(err, &imageErr) && imageErr.Code == imaging.CodeNotFound {
				continue // A concurrent deletion is an expected listing race.
			}
			if err != nil {
				return err
			}
		}
		if jobs.NextCursor == "" {
			return nil
		}
		page.Cursor = jobs.NextCursor
	}
}
