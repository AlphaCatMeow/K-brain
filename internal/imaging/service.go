package imaging

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AssetSink saves bytes before the engine atomically publishes ownership and references.
type AssetSink interface {
	SaveOutput(context.Context, Scope, OutputSource) (ImageAsset, error)
}
type InputResolver interface {
	ResolveAsset(Scope, string) (ResolvedAsset, error)
}
type AdapterResolver func(context.Context, Scope, TargetSnapshot) (Adapter, error)

// ServiceOptions contains execution dependencies only, never provider credentials.
// AdapterResolver must resolve current authorization against the frozen target.
type ServiceOptions struct {
	AssetSink             AssetSink
	InputResolver         InputResolver
	AdapterResolver       AdapterResolver
	GlobalConcurrency     int
	ConnectionConcurrency func(Scope, string) int
	JobTimeout            time.Duration
	SubmitTimeout         time.Duration
	PollInterval          time.Duration
	MaxPollInterval       time.Duration
	LeaseDuration         time.Duration
	LeaseRenewInterval    time.Duration
}
type realImageClock struct{}

func (realImageClock) Now() time.Time                         { return time.Now() }
func (realImageClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Service struct {
	store     *Store
	registry  Registry
	clock     Clock
	options   ServiceOptions
	mu        sync.Mutex
	stopped   bool
	scheduler *Scheduler
	wake      chan struct{}
}

func NewService(store *Store, registry Registry, clock Clock, options ...ServiceOptions) *Service {
	if clock == nil {
		clock = realImageClock{}
	}
	var o ServiceOptions
	if len(options) > 0 {
		o = options[0]
	}
	if o.GlobalConcurrency < 1 || o.GlobalConcurrency > 16 {
		o.GlobalConcurrency = 4
	}
	if o.JobTimeout <= 0 || o.JobTimeout > 120*time.Minute {
		o.JobTimeout = 30 * time.Minute
	}
	if o.SubmitTimeout <= 0 {
		o.SubmitTimeout = 180 * time.Second
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.MaxPollInterval < o.PollInterval {
		o.MaxPollInterval = 15 * time.Second
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 60 * time.Second
	}
	if o.LeaseRenewInterval <= 0 {
		o.LeaseRenewInterval = 20 * time.Second
	}
	return &Service{store: store, registry: registry, clock: clock, options: o, wake: make(chan struct{}, 1)}
}
func engineID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%s%x-%x-%x-%x-%x", prefix, b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) StopAccepting() { s.mu.Lock(); s.stopped = true; s.mu.Unlock() }
func (s *Service) CreateJob(ctx context.Context, scope Scope, key string, r CreateJobRequest) (ImageJob, bool, error) {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped || s.store == nil || s.registry == nil {
		return ImageJob{}, false, &ImageError{Code: CodeUnavailable}
	}
	if strings.TrimSpace(key) == "" || len(key) > 256 || strings.HasPrefix(key, VerificationKeyPrefix) {
		return ImageJob{}, false, invalid("idempotency key required (max 256 bytes)")
	}
	hash, err := HashRequest(r)
	if err != nil {
		return ImageJob{}, false, err
	}
	// Replay is independent of subsequent target edits, disablement, or credentials.
	if old, ok := s.store.GetIdempotency(scope, key); ok {
		if old.Hash != hash {
			return ImageJob{}, false, &ImageError{Code: CodeIdempotencyConflict}
		}
		j, err := s.store.ReadJob(scope, old.JobID)
		return j, true, err
	}
	// Settings can hold their lock across durable acceptance to serialize deletion.
	if registry, ok := s.registry.(interface {
		WithImageTarget(context.Context, Scope, string, func(TargetSnapshot) error) error
	}); ok {
		var job ImageJob
		var replay bool
		err = registry.WithImageTarget(ctx, scope, r.TargetID, func(target TargetSnapshot) error {
			var acceptErr error
			job, replay, acceptErr = s.acceptJob(ctx, scope, key, hash, r, target)
			return acceptErr
		})
		return job, replay, err
	}
	target, err := s.registry.ResolveTarget(ctx, scope, r.TargetID)
	if err != nil {
		return ImageJob{}, false, err
	}
	return s.acceptJob(ctx, scope, key, hash, r, target)
}
func (s *Service) acceptJob(ctx context.Context, scope Scope, key, hash string, r CreateJobRequest, target TargetSnapshot) (ImageJob, bool, error) {
	// Lock order: registry (optional), service, store. Never resolve settings here.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return ImageJob{}, false, &ImageError{Code: CodeUnavailable}
	}
	var err error
	target = cloneStoreValue(target)
	if err = ValidateRequest(r, target); err != nil {
		return ImageJob{}, false, err
	}
	if _, err = s.store.ReadProject(scope, r.ProjectID); err != nil {
		return ImageJob{}, false, err
	}
	for _, in := range r.Inputs {
		a, e := s.store.ReadAsset(scope, in.AssetID)
		if e != nil {
			return ImageJob{}, false, e
		}
		if !a.Available {
			return ImageJob{}, false, &ImageError{Code: CodeNotFound}
		}
		if s.options.InputResolver == nil {
			return ImageJob{}, false, ErrUnsupported
		}
		if _, e = s.options.InputResolver.ResolveAsset(scope, in.AssetID); e != nil {
			return ImageJob{}, false, e
		}
	}
	if r.ParentAssetID != "" {
		found := false
		for _, in := range r.Inputs {
			found = found || in.AssetID == r.ParentAssetID
		}
		if !found {
			return ImageJob{}, false, invalid("parent must be an input")
		}
	}
	now := s.clock.Now().UTC()
	j := ImageJob{ID: engineID("ij_"), ProjectID: r.ProjectID, Request: cloneRequest(r), TargetSnapshot: target, Status: JobQueued, Revision: 1, CreatedAt: now, UpdatedAt: now}
	ex := ExecutionRecord{JobID: j.ID, Deadline: now.Add(s.options.JobTimeout), Items: map[string]ItemExecution{}}
	for n := 0; n < r.Count; n++ {
		id := engineID("ii_")
		j.Items = append(j.Items, ImageItem{ID: id, Status: JobQueued})
		ex.Items[id] = ItemExecution{SubmissionKey: engineID("is_")}
	}
	replay := false
	err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		if old, ok := tx.GetIdempotency(scope, key); ok {
			if old.Hash != hash {
				return &ImageError{Code: CodeIdempotencyConflict}
			}
			var e error
			j, e = tx.ReadJob(scope, old.JobID)
			replay = true
			return e
		}
		if e := tx.PutJob(j); e != nil {
			return e
		}
		if e := tx.PutExecution(ex); e != nil {
			return e
		}
		if e := tx.PutIdempotency(scope, key, hash, j.ID); e != nil {
			return e
		}
		return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobAccepted, Timestamp: now, Data: ImageEventData{Status: JobQueued}})
	})
	if err == nil {
		s.notify()
	}
	return PublicJob(j), replay, err
}
func (s *Service) GetJob(ctx context.Context, scope Scope, id string) (ImageJob, error) {
	if err := ctx.Err(); err != nil {
		return ImageJob{}, err
	}
	return s.store.ReadJob(scope, id)
}
func (s *Service) ListJobs(ctx context.Context, scope Scope, project string, p PageRequest) (Page[ImageJob], error) {
	if p.Limit == 0 {
		p.Limit = 30
	}
	return s.store.ListJobs(ctx, scope, project, p)
}
func (s *Service) ReadEvents(ctx context.Context, scope Scope, id string, after uint64, limit int) ([]ImageEvent, error) {
	return s.store.ReadEvents(ctx, scope, id, after, limit)
}
func (s *Service) ReadJobSnapshot(ctx context.Context, scope Scope, id string) (ImageJob, EventPosition, error) {
	if err := ctx.Err(); err != nil {
		return ImageJob{}, EventPosition{}, err
	}
	return s.store.ReadJobSnapshot(scope, id)
}
func (s *Service) CancelJob(ctx context.Context, scope Scope, id string) (ImageJob, error) {
	var result ImageJob
	err := s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(scope, id)
		if err != nil {
			return err
		}
		result = j
		if terminalJob(j.Status) || j.Status == JobCancelRequested {
			return nil
		}
		ex, err := tx.ReadExecution(scope, id)
		if err != nil {
			return err
		}
		submitted, remote := false, false
		for i := range j.Items {
			item := &j.Items[i]
			x := ex.Items[item.ID]
			submitted = submitted || len(x.Attempts) > 0
			remote = remote || (x.ProviderHandle != "" && !terminalJob(item.Status))
			if item.Status == JobQueued {
				item.Status = JobCancelled
			}
		}
		j.Status = JobCancelRequested
		if err := s.writeJob(tx, &j, EventJobStateChanged); err != nil {
			return err
		}
		if !submitted || !remote || !j.TargetSnapshot.Capabilities.SupportsCancel {
			j.Status = JobCancelled
			j.CancelEffect = "local_only"
			if !submitted {
				j.CancelEffect = "not_submitted"
			}
			for i := range j.Items {
				if !terminalJob(j.Items[i].Status) {
					j.Items[i].Status = JobCancelled
				}
			}
			if err := s.writeJob(tx, &j, EventJobFinished); err != nil {
				return err
			}
		}
		result = j
		return nil
	})
	if err == nil {
		s.notify()
	}
	return PublicJob(result), err
}
func (s *Service) writeJob(tx *Transaction, j *ImageJob, event string) error {
	j.Revision++
	j.UpdatedAt = s.clock.Now().UTC()
	if err := tx.PutJob(*j); err != nil {
		return err
	}
	return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: event, Timestamp: j.UpdatedAt, Data: ImageEventData{Status: j.Status, Error: j.Error}})
}
