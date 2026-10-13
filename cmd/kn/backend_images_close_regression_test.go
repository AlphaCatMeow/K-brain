package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type closeRuntimeRegistry struct{}

func (closeRuntimeRegistry) ResolveTarget(context.Context, imaging.Scope, string) (imaging.TargetSnapshot, error) {
	return imaging.SnapshotTarget(imaging.ImageTarget{ID: "it_close", ConnectionID: "ic_close", Adapter: "test", AdapterVersion: "1", Kind: "model", RemoteID: "model", Revision: 1, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, MaxCount: 1, OutputFormats: []string{"png"}}}), nil
}

type closeRuntimeAdapter struct{ entered, release chan struct{} }

func (a closeRuntimeAdapter) Submit(context.Context, imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	close(a.entered)
	<-a.release
	return imaging.RemoteResult{State: "completed", Outputs: []imaging.OutputSource{{Kind: "bytes", Data: []byte("test")}}}, nil
}
func (closeRuntimeAdapter) Poll(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}
func (closeRuntimeAdapter) Reconcile(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}
func (closeRuntimeAdapter) Cancel(context.Context, string) error { return imaging.ErrUnsupported }

type closeRuntimeSink struct{}

func (closeRuntimeSink) SaveOutput(context.Context, imaging.Scope, imaging.OutputSource) (imaging.ImageAsset, error) {
	// A durable publication conflict produces a real scheduler failure without
	// closing or poisoning the store that runtime.Close must later release.
	return imaging.ImageAsset{ID: "ia_collision", Hash: "conflicting"}, nil
}

func TestImageRuntimeCloseReleasesWriterAfterSchedulerFailure(t *testing.T) {
	for _, timeoutFirst := range []bool{false, true} {
		name := "historical-error"
		if timeoutFirst {
			name = "timeout-then-error"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, err := imaging.OpenStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			assets, err := imaging.NewAssetStore(store, filepath.Join(root, "assets"))
			if err != nil {
				t.Fatal(err)
			}
			defer assets.Close()
			adapter := closeRuntimeAdapter{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			service := imaging.NewService(store, closeRuntimeRegistry{}, nil, imaging.ServiceOptions{AssetSink: closeRuntimeSink{}, AdapterResolver: func(context.Context, imaging.Scope, imaging.TargetSnapshot) (imaging.Adapter, error) {
				return adapter, nil
			}})
			q := imaging.NewScheduler(service)
			runtime := &imageRuntime{scheduler: q, assets: assets, store: store}
			defer func() {
				once.Do(func() { close(adapter.release) })
				closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				_ = runtime.Close(closeCtx)
			}()
			scope := imaging.Scope{OwnerID: "close-owner", BackendID: "close-backend"}
			project, err := store.CreateProject(ctx, scope, "close", "")
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Update(imaging.WithScope(ctx, scope), func(tx *imaging.Transaction) error {
				return tx.PutAsset(imaging.ImageAsset{ID: "ia_collision", Hash: "original"})
			}); err != nil {
				t.Fatal(err)
			}
			job, _, err := service.CreateJob(ctx, scope, "close", imaging.CreateJobRequest{ProjectID: project.ID, TargetID: "it_close", Operation: imaging.OperationGenerate, Prompt: "test", Count: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err = q.Start(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-adapter.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("submit did not start")
			}
			if timeoutFirst {
				deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				err = runtime.Close(deadline)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("close before worker exit: %v", err)
				}
				if _, err = store.CreateProject(ctx, scope, "still open", ""); err != nil {
					t.Fatalf("store closed under live worker: %v", err)
				}
				if second, err := imaging.OpenStore(root); err == nil {
					second.Close()
					t.Fatal("writer released under live worker")
				}
			}
			once.Do(func() { close(adapter.release) })
			select {
			case <-q.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("scheduler did not stop after publication failure")
			}
			ex, err := store.ReadExecution(scope, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if ex.Items[job.Items[0].ID].LeaseOwner != "" {
				t.Fatal("failed worker leaked lease")
			}
			// Even an expired Close context must preserve a completed scheduler's error.
			deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			err = runtime.Close(deadline)
			cancel()
			var imageErr *imaging.ImageError
			if !errors.As(err, &imageErr) || imageErr.Code != imaging.CodeRevisionConflict {
				t.Fatalf("historical error lost: %v", err)
			}
			if _, err = store.CreateProject(ctx, scope, "closed", ""); err == nil || !strings.Contains(err.Error(), "IMAGE_STORE_CLOSED") {
				t.Fatalf("store was not closed: %v", err)
			}
			reopened, err := imaging.OpenStore(root)
			if err != nil {
				t.Fatalf("writer lock leaked: %v", err)
			}
			if err = reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
