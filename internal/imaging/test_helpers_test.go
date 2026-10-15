package imaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type engineRegistry struct {
	target TargetSnapshot
	err    error
}

func (r *engineRegistry) ResolveTarget(context.Context, Scope, string) (TargetSnapshot, error) {
	return cloneSnapshot(r.target), r.err
}

type engineAdapter struct {
	mu                                  sync.Mutex
	submits, polls, reconciles, cancels int
	submit                              func(context.Context, ExecuteRequest) (RemoteResult, error)
	poll                                func(context.Context, string) (RemoteResult, error)
	reconcile                           func(context.Context, string) (RemoteResult, error)
	cancel                              func(context.Context, string) error
}

func (a *engineAdapter) Submit(c context.Context, r ExecuteRequest) (RemoteResult, error) {
	a.mu.Lock()
	a.submits++
	f := a.submit
	a.mu.Unlock()
	if f != nil {
		return f(c, r)
	}
	return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
}
func (a *engineAdapter) Poll(c context.Context, h string) (RemoteResult, error) {
	a.mu.Lock()
	a.polls++
	f := a.poll
	a.mu.Unlock()
	if f != nil {
		return f(c, h)
	}
	return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
}
func (a *engineAdapter) Reconcile(c context.Context, h string) (RemoteResult, error) {
	a.mu.Lock()
	a.reconciles++
	f := a.reconcile
	a.mu.Unlock()
	if f != nil {
		return f(c, h)
	}
	return RemoteResult{}, ErrUnsupported
}
func (a *engineAdapter) Cancel(c context.Context, h string) error {
	a.mu.Lock()
	a.cancels++
	f := a.cancel
	a.mu.Unlock()
	if f != nil {
		return f(c, h)
	}
	return nil
}
func (a *engineAdapter) counts() (int, int, int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.submits, a.polls, a.reconciles, a.cancels
}

type engineSinkFunc func(context.Context, Scope, OutputSource) (ImageAsset, error)

func (f engineSinkFunc) SaveOutput(ctx context.Context, scope Scope, output OutputSource) (ImageAsset, error) {
	return f(ctx, scope, output)
}

type engineSink struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (a *engineSink) SaveOutput(ctx context.Context, s Scope, o OutputSource) (ImageAsset, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.fail && string(o.Data) == "bad" {
		return ImageAsset{}, ErrUnsupported
	}
	return ImageAsset{ID: engineID("ia_"), Hash: "hash", MIME: "image/png", Bytes: int64(len(o.Data)), Width: 1, Height: 1, Available: true, CreatedAt: time.Now()}, nil
}
func engineFixture(t *testing.T) (*Service, *Scheduler, *engineAdapter, Scope, CreateJobRequest) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	scope := Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}
	p, err := store.CreateProject(context.Background(), scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	target.Capabilities.MaxCount = 16
	target.Capabilities.SupportsReconcile = true
	reg := &engineRegistry{target: target}
	a := &engineAdapter{}
	svc := NewService(store, reg, nil, ServiceOptions{AssetSink: &engineSink{}, AdapterResolver: func(context.Context, Scope, TargetSnapshot) (Adapter, error) { return a, nil }, PollInterval: time.Millisecond, MaxPollInterval: 5 * time.Millisecond})
	scheduler := NewScheduler(svc)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := scheduler.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return svc, scheduler, a, scope, CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1}
}
func awaitEngine(t *testing.T, s *Service, scope Scope, id string, status JobStatus) ImageJob {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		j, err := s.GetJob(context.Background(), scope, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == status {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := s.GetJob(context.Background(), scope, id)
	t.Fatalf("want %s, got %+v", status, j)
	return j
}

type engineTimer struct {
	at time.Time
	ch chan time.Time
}
type engineClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []engineTimer
}

func (c *engineClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *engineClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.timers = append(c.timers, engineTimer{c.now.Add(d), ch})
	}
	return ch
}
func (c *engineClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var pending []engineTimer
	for _, timer := range c.timers {
		if !c.now.Before(timer.at) {
			timer.ch <- c.now
		} else {
			pending = append(pending, timer)
		}
	}
	c.timers = pending
}
func engineEventually(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("engine condition not reached")
}
func assertEngineError(t *testing.T, err error, code string) {
	t.Helper()
	var e *ImageError
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s got %v", code, err)
	}
}
