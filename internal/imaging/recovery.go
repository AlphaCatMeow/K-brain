package imaging

import (
	"context"
	"time"
)

// Recover reclaims leases held by the previous process. A recorded submitting
// attempt never becomes queued: only a persisted not_sent outcome can retry.
func (q *Scheduler) Recover(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started || q.closed {
		return &ImageError{Code: CodeUnavailable}
	}
	imageSchedulers.Lock()
	defer imageSchedulers.Unlock()
	if other := imageSchedulers.owners[q.service.store]; other != nil && other != q {
		return &ImageError{Code: CodeUnavailable}
	}
	return q.recover(ctx)
}

// recoverLeases also visits tombstoned jobs left by older versions that allowed
// deletion while a cancelled worker was still running. Public job readers hide
// those records, but their stale leases must not block GC after restart.
func (q *Scheduler) recoverLeases(ctx context.Context) error {
	s := q.service.store
	s.mu.RLock()
	scopes := map[Scope]bool{}
	for _, ex := range s.data.Executions {
		for _, item := range ex.Items {
			if item.LeaseOwner != "" || !item.LeaseExpiresAt.IsZero() {
				scopes[Scope{OwnerID: ex.Scope.OwnerID, BackendID: ex.Scope.BackendID}] = true
				break
			}
		}
	}
	s.mu.RUnlock()
	for scope := range scopes {
		if err := s.Update(WithScope(ctx, scope), func(tx *Transaction) error {
			for id, ex := range tx.data.Executions {
				if !ex.Scope.matches(scope) {
					continue
				}
				changed := false
				for itemID, item := range ex.Items {
					if item.LeaseOwner != "" || !item.LeaseExpiresAt.IsZero() {
						item.LeaseOwner = ""
						item.LeaseExpiresAt = time.Time{}
						ex.Items[itemID] = item
						changed = true
					}
				}
				if changed {
					tx.data.Executions[id] = ex
					tx.mutations = append(tx.mutations, storeMutation{Kind: "execution", Execution: &ex})
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (q *Scheduler) recover(ctx context.Context) error {
	s := q.service
	if err := q.recoverLeases(ctx); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, w := range s.executionWork() {
		if seen[w.job.ID] || terminalJob(w.job.Status) {
			continue
		}
		seen[w.job.ID] = true
		if err := s.store.Update(WithScope(ctx, w.scope), func(tx *Transaction) error {
			j, err := tx.ReadJob(w.scope, w.job.ID)
			if err != nil {
				return err
			}
			ex, err := tx.ReadExecution(w.scope, j.ID)
			if err != nil {
				return err
			}
			changed := false
			for n := range j.Items {
				i := &j.Items[n]
				x := ex.Items[i.ID]
				if !terminalJob(j.Status) && !terminalJob(i.Status) {
					if len(x.Attempts) > 0 && i.Status == JobQueued {
						last := x.Attempts[len(x.Attempts)-1]
						if last.Delivery != DeliveryNotSent {
							i.Status = JobRunning
							changed = true
						}
					}
					if i.Status == JobSaving {
						i.Status = JobRunning
						changed = true
					}
					if x.ProviderHandle != "" {
						x.NextPollAt = s.clock.Now()
					}
				}
				ex.Items[i.ID] = x
			}
			if err = tx.PutExecution(ex); err != nil {
				return err
			}
			if changed {
				aggregateJob(&j, ex)
				return s.writeJob(tx, &j, jobEvent(j.Status))
			}
			return nil
		}); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}
func (s *Service) Recover(ctx context.Context) error {
	s.mu.Lock()
	q := s.scheduler
	s.mu.Unlock()
	if q == nil {
		q = NewScheduler(s)
	}
	return q.Recover(ctx)
}

// ReconcileJob explicitly reopens unknown items for read-only provider lookup.
// It preserves submission keys, attempts, assets and handles and never submits.
func (s *Service) ReconcileJob(ctx context.Context, scope Scope, id string) (ImageJob, error) {
	var result ImageJob
	err := s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(scope, id)
		if err != nil {
			return err
		}
		result = j
		if j.Status != JobResultUnknown {
			return nil
		}
		ex, err := tx.ReadExecution(scope, id)
		if err != nil {
			return err
		}
		for n := range j.Items {
			if j.Items[n].Status != JobResultUnknown {
				continue
			}
			x := ex.Items[j.Items[n].ID]
			if x.ProviderHandle == "" && !j.TargetSnapshot.Capabilities.SupportsReconcile {
				return ErrUnsupported
			}
			j.Items[n].Status = JobRunning
			j.Items[n].Error = nil
			x.QueryFailures = 0
			x.NextPollAt = s.clock.Now()
			// A worker may still be finishing the transition to result_unknown.
			// Its lease remains owned until that worker exits.
			ex.Items[j.Items[n].ID] = x
		}
		ex.Deadline = s.clock.Now().Add(s.options.JobTimeout)
		j.Status = JobRunning
		j.Error = nil
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		if err = s.writeJob(tx, &j, EventJobStateChanged); err != nil {
			return err
		}
		result = j
		return nil
	})
	if err == nil {
		s.notify()
	}
	return PublicJob(result), err
}

// ConnectionInUse is safe to call while holding a settings lock: it only takes
// the store's read lock and never calls the registry or an adapter resolver.
func (s *Service) ConnectionInUse(scope Scope, id string) bool {
	busy, _ := s.ImageDependenciesInUse(context.Background(), scope, []string{id}, nil)
	return busy
}

// ImageDependenciesInUse implements the settings integration without importing backend.
func (s *Service) ImageDependenciesInUse(ctx context.Context, scope Scope, connections, targets []string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	wantedConnections, wantedTargets := map[string]bool{}, map[string]bool{}
	for _, id := range connections {
		wantedConnections[id] = true
	}
	for _, id := range targets {
		wantedTargets[id] = true
	}
	for _, w := range s.executionWork() {
		if w.scope != scope || (terminalJob(w.job.Status) && w.job.Status != JobResultUnknown) {
			continue
		}
		if wantedConnections[w.job.TargetSnapshot.ConnectionID] || wantedTargets[w.job.TargetSnapshot.ID] {
			return true, nil
		}
	}
	return false, nil
}
