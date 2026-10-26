package imaging

import (
	"context"
	"testing"
	"time"
)

// Dropping the deleted-unknown retention must not change anything for jobs that
// are not deleted, or for content shared with live jobs.

func (g *assetGCTest) attach(scope Scope, a ImageAsset, jobID string, parents ...string) ImageAsset {
	g.t.Helper()
	a.JobID = jobID
	a.ParentAssetIDs = parents
	if err := g.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error { return tx.PutAsset(a) }); err != nil {
		g.t.Fatal(err)
	}
	return a
}

func (g *assetGCTest) eventAsset(scope Scope, jobID string, a ImageAsset) {
	g.t.Helper()
	if err := g.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
		return tx.AppendEvent(ImageEvent{JobID: jobID, Type: EventAssetReady, Data: ImageEventData{Asset: &a}})
	}); err != nil {
		g.t.Fatal(err)
	}
}

// An undeleted unknown job (and a cancelled job whose item stayed unknown) must
// keep its inputs, outputs, event-only assets and their ancestry indefinitely.
func TestUndeletedResultUnknownKeepsAllReferences(t *testing.T) {
	cases := []struct{ name, job, item JobStatus }{
		{"job_and_item_unknown", JobResultUnknown, JobResultUnknown},
		{"cancelled_job_unknown_item", JobCancelled, JobResultUnknown},
	}
	for _, c := range cases {
		t.Run(string(c.name), func(t *testing.T) {
			g := newAssetGCTest(t)
			id := "ij_live_" + string(c.name)
			input := g.asset(storeScope, "upload", 2)
			parent := g.asset(storeScope, "generated", 4)
			output := g.asset(storeScope, "generated", 3)
			eventOnly := g.asset(storeScope, "generated", 5)
			p, err := g.store.CreateProject(context.Background(), storeScope, id, g.dir+"/ws-"+id)
			if err != nil {
				t.Fatal(err)
			}
			j := ImageJob{ID: id, ProjectID: p.ID, Revision: 1, Status: c.job, CreatedAt: g.now,
				Items:   []ImageItem{{ID: id + "_item", Status: c.item, OutputAssetIDs: []string{output.ID}}},
				Request: CreateJobRequest{Inputs: []InputAsset{{AssetID: input.ID, Role: "reference"}, {AssetID: parent.ID, Role: "reference"}}}}
			if err := g.store.Update(WithScope(context.Background(), storeScope), func(tx *Transaction) error {
				if err := tx.PutJob(j); err != nil {
					return err
				}
				return tx.AppendEvent(ImageEvent{JobID: id, Type: EventJobAccepted})
			}); err != nil {
				t.Fatal(err)
			}
			if got := g.store.data.Jobs[id].Value.Items[0].Status; got != c.item {
				t.Fatalf("item status = %s", got)
			}
			// parent belongs to an already expired, deleted job: only this live job keeps it.
			pp := g.job(storeScope, "ij_parent_owner", JobSucceeded, nil, []string{parent.ID})
			parent = g.attach(storeScope, parent, "ij_parent_owner")
			output = g.attach(storeScope, output, id, parent.ID)
			eventOnly = g.attach(storeScope, eventOnly, id)
			g.eventAsset(storeScope, id, eventOnly)
			if err := g.store.DeleteProject(context.Background(), storeScope, pp.ID, pp.Revision); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				g.now = g.now.Add(31 * 24 * time.Hour)
				if r := g.run(); r.DeferredBusy {
					t.Fatalf("pass %d deferred: %+v", i, r)
				}
			}
			for _, a := range []ImageAsset{input, parent, output, eventOnly} {
				g.files(a, true)
				g.available(storeScope, a, true)
			}
		})
	}
}

// Content of a deleted unknown job that a live job still uses (as input, or as
// the parent of its output) must survive tombstone expiry.
func TestDeletedResultUnknownSharedWithLiveJob(t *testing.T) {
	g := newAssetGCTest(t)
	shared := g.asset(storeScope, "generated", 2)
	ancestor := g.asset(storeScope, "generated", 3)
	child := g.asset(storeScope, "generated", 4)
	dead := g.job(storeScope, "ij_dead_unknown", JobResultUnknown, nil, []string{shared.ID, ancestor.ID})
	shared = g.attach(storeScope, shared, "ij_dead_unknown")
	ancestor = g.attach(storeScope, ancestor, "ij_dead_unknown")
	g.job(storeScope, "ij_live_user", JobSucceeded, []string{shared.ID, ancestor.ID}, []string{child.ID})
	child = g.attach(storeScope, child, "ij_live_user", ancestor.ID)
	if err := g.store.DeleteProject(context.Background(), storeScope, dead.ID, dead.Revision); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(31 * 24 * time.Hour)
	g.run()
	g.now = g.now.Add(25 * time.Hour)
	g.run()
	for _, a := range []ImageAsset{shared, ancestor, child} {
		g.files(a, true)
		g.available(storeScope, a, true)
	}
}

// Replaying the idempotency key of a deleted unknown job after its files were
// reclaimed must not hand back (or rebuild) a job pointing at reclaimed content.
func TestReplayDeletedResultUnknownAfterGC(t *testing.T) {
	s, _, _, scope, request := engineFixture(t)
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, scope, "replay-unknown", request)
	if err != nil {
		t.Fatal(err)
	}
	markResultUnknown(t, s, scope, job.ID, false, "")
	if err = s.store.DeleteJob(ctx, scope, job.ID); err != nil {
		t.Fatal(err)
	}
	got, replay, err := s.CreateJob(ctx, scope, "replay-unknown", request)
	assertEngineError(t, err, CodeNotFound)
	if !replay && got.ID != "" {
		t.Fatalf("replay created a new job %s", got.ID)
	}
	if _, err = s.ReconcileJob(ctx, scope, job.ID); err == nil {
		t.Fatal("deleted unknown job reconciled")
	}
	if err = s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		j := tx.data.Jobs[job.ID].Value
		j.Revision++
		return tx.PutJob(j)
	}); err == nil {
		t.Fatal("tombstoned job rewritten")
	}
}
