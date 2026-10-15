package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type acceptingEngineRegistry struct {
	engineRegistry
	mu     sync.Mutex
	called bool
}

func (r *acceptingEngineRegistry) WithImageTarget(ctx context.Context, scope Scope, id string, accept func(TargetSnapshot) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = true
	return accept(r.target)
}
func TestImageJobCreateSettingsAcceptanceSnapshotAndRequestLifetime(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	registry := &acceptingEngineRegistry{engineRegistry: *s.registry.(*engineRegistry)}
	s.registry = registry
	ctx, cancel := context.WithCancel(context.Background())
	j, _, err := s.CreateJob(ctx, scope, "snapshot", r)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	registry.target.RemoteID = "changed"
	registry.err = &ImageError{Code: CodeAuthRequired}
	if !registry.called {
		t.Fatal("atomic registry was not used")
	}
	a.submit = func(_ context.Context, req ExecuteRequest) (RemoteResult, error) {
		if req.Target.RemoteID != j.TargetSnapshot.RemoteID {
			t.Error("snapshot changed")
		}
		return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
	}
	replay, exists, err := s.CreateJob(context.Background(), scope, "snapshot", r)
	if err != nil || !exists || replay.ID != j.ID {
		t.Fatal(replay, exists, err)
	}
	q.Start(context.Background())
	awaitEngine(t, s, scope, j.ID, JobSucceeded)
}
func TestImageJobCreateDurableFailureDoesNotAccept(t *testing.T) {
	s, _, a, scope, r := engineFixture(t)
	s.store.fault = func(point string) error {
		if point == "wal.before_append" {
			return errors.New("disk full")
		}
		return nil
	}
	if _, _, err := s.CreateJob(context.Background(), scope, "failed", r); err == nil {
		t.Fatal("accepted without commit")
	}
	s.store.fault = nil
	if _, ok := s.store.GetIdempotency(scope, "failed"); ok {
		t.Fatal("failed acceptance retained key")
	}
	page, err := s.ListJobs(context.Background(), scope, r.ProjectID, PageRequest{Limit: 100})
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	sub, _, _, _ := a.counts()
	if sub != 0 {
		t.Fatal(sub)
	}
}
func TestImageJobCreateConcurrentIdempotency(t *testing.T) {
	s, _, _, scope, r := engineFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, _, err := s.CreateJob(context.Background(), scope, "parallel", r)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatal(unique)
	}
}

func TestImageJobCreateIdempotencyBatch(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	r.Count = 3
	j, replay, err := s.CreateJob(context.Background(), scope, "key", r)
	if err != nil || replay || len(j.Items) != 3 {
		t.Fatalf("%+v %v %v", j, replay, err)
	}
	again, replay, err := s.CreateJob(context.Background(), scope, "key", r)
	if err != nil || !replay || again.ID != j.ID {
		t.Fatal(again, replay, err)
	}
	r.Prompt = "other"
	_, _, err = s.CreateJob(context.Background(), scope, "key", r)
	assertEngineError(t, err, CodeIdempotencyConflict)
	if err = q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	j = awaitEngine(t, s, scope, j.ID, JobSucceeded)
	if len(j.AssetIDs) != 3 || j.Usage != nil {
		t.Fatal(j)
	}
	sub, _, _, _ := a.counts()
	if sub != 3 {
		t.Fatal(sub)
	}
	events, err := s.ReadEvents(context.Background(), scope, j.ID, 0, 100)
	if err != nil || len(events) < 5 {
		t.Fatal(events, err)
	}
	raw, _ := json.Marshal(j)
	if strings.Contains(string(raw), "SubmissionKey") {
		t.Fatal(string(raw))
	}
}
func TestImageJobCreateValidationAndScope(t *testing.T) {
	s, _, a, scope, r := engineFixture(t)
	for _, mutate := range []func(*CreateJobRequest){func(r *CreateJobRequest) { r.Count = 17 }, func(r *CreateJobRequest) { r.Operation = OperationEdit }, func(r *CreateJobRequest) { r.Parameters = map[string]any{"secret": 1} }, func(r *CreateJobRequest) { r.TargetID = "bad" }} {
		bad := r
		mutate(&bad)
		if _, _, err := s.CreateJob(context.Background(), scope, "bad", bad); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
	r.Inputs = []InputAsset{{AssetID: engineID("ia_"), Role: "reference"}}
	r.Operation = OperationReference
	if _, _, err := s.CreateJob(context.Background(), scope, "cross", r); err == nil {
		t.Fatal("accepted foreign asset")
	}
	sub, _, _, _ := a.counts()
	if sub != 0 {
		t.Fatal(sub)
	}
}
func TestImageJobCreateShutdown(t *testing.T) {
	s, q, _, scope, r := engineFixture(t)
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateJob(context.Background(), scope, "closed", r)
	assertEngineError(t, err, CodeUnavailable)
}
