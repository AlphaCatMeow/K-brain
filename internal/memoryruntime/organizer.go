package memoryruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

type OrganizerTrigger string

const (
	OrganizerManual    OrganizerTrigger = "manual"
	OrganizerScheduled OrganizerTrigger = "scheduled"
)

type OrganizerRun struct {
	RunID     string            `json:"runId"`
	Epoch     uint64            `json:"epoch"`
	Executor  string            `json:"executorId"`
	Trigger   OrganizerTrigger  `json:"trigger"`
	Phase     string            `json:"phase"`
	Status    string            `json:"status"`
	Workdir   string            `json:"workdir,omitempty"`
	Model     string            `json:"model,omitempty"`
	Scanned   int               `json:"scanned"`
	Clusters  int               `json:"clusters"`
	Applied   int               `json:"applied"`
	Risk      string            `json:"risk"`
	Pending   []memory.Decision `json:"pending,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// RunOrganizer executes the auditable scan -> cluster -> plan -> gate -> apply flow.
// Manual runs stop at gate and can be applied later with ApplyOrganizerRun.
func (r *Runtime) RunOrganizer(ctx context.Context, workdir string, trigger OrganizerTrigger) (OrganizerRun, error) {
	if err := ctx.Err(); err != nil {
		return OrganizerRun{}, err
	}
	if trigger == "" {
		trigger = OrganizerManual
	}
	now := r.now()
	epoch, err := r.store.OrganizerEpoch()
	if err != nil {
		return OrganizerRun{}, err
	}
	run := OrganizerRun{RunID: randomRunID(), Epoch: epoch, Executor: newExecutorID(), Trigger: trigger, Phase: "scan", Status: "running", Workdir: workdir, Model: r.organizer, CreatedAt: now, UpdatedAt: now}
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	list, err := r.store.List(memory.ListArgs{Workdir: workdir, IncludeDaily: false, Limit: 10000})
	if err != nil {
		run.Status = "failed"
		run.Warnings = []string{err.Error()}
		if saveErr := r.saveRun(run); saveErr != nil {
			return run, errors.Join(err, saveErr)
		}
		return run, err
	}
	run.Scanned = len(list.Entries)
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "cluster"
	run.UpdatedAt = r.now()
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	clusters := clusterEntries(list.Entries)
	run.Clusters = len(clusters)
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "plan"
	run.UpdatedAt = r.now()
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	decisions := make([]memory.Decision, 0)
	for _, cluster := range clusters {
		// The model round is optional: deterministic keep decisions are safe when no
		// organizer model is configured, while configured models are K-brain-only.
		if len(cluster) > 1 {
			first := cluster[0]
			for _, duplicate := range cluster[1:] {
				if duplicate.Description != "" && strings.EqualFold(duplicate.Description, first.Description) {
					decisions = append(decisions, memory.Decision{Op: "delete", Slug: duplicate.Slug, Scope: duplicate.Scope, WorkdirHash: duplicate.WorkdirHash, GroupID: run.RunID, Reason: "duplicate in organizer cluster"})
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "gate"
	run.UpdatedAt = r.now()
	run.Pending = decisions
	if len(decisions) == 0 {
		run.Risk = "none"
	} else {
		run.Risk = "low"
	}
	// Scheduled runs auto-apply deterministic low-risk or no-op plans. Manual runs
	// are retained as pending review even when the proposed action is low-risk.
	run.Status = "pending_review"
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	var applyErr error
	var result memory.BatchResponse
	if trigger == OrganizerScheduled && (run.Risk == "low" || run.Risk == "none") {
		claimed, shouldApply, claimErr := r.store.ClaimOrganizeRunApply(run.RunID)
		if claimErr != nil {
			return run, claimErr
		}
		if !shouldApply {
			return run, fmt.Errorf("organizer run %q was not claimed for scheduled apply", run.RunID)
		}
		claimedRun, decodeErr := organizerRunFromStore(claimed)
		if decodeErr != nil {
			return run, decodeErr
		}
		run = claimedRun
		result, applyErr = r.store.ApplyBatch(memory.BatchArgs{Workdir: workdir, Trigger: "organizer", Model: r.organizer, Decisions: decisions})
		if applyErr != nil {
			run.Status = "failed"
			run.Warnings = []string{applyErr.Error()}
		} else {
			run.Applied = len(result.Deleted) + len(result.Updated) + len(result.Created)
			run.Pending = nil
			run.Status = "completed"
		}
	}
	run.Phase = "complete"
	run.UpdatedAt = r.now()
	if saveErr := r.saveRun(run); saveErr != nil {
		if applyErr != nil {
			return run, errors.Join(applyErr, saveErr)
		}
		return run, saveErr
	}
	return run, applyErr
}

func (r *Runtime) ApplyOrganizerRun(ctx context.Context, runID string) (OrganizerRun, error) {
	stored, storeErr := r.store.ReadOrganizeRun(runID)
	var run OrganizerRun
	if storeErr == nil {
		var decodeErr error
		run, decodeErr = organizerRunFromStore(stored)
		if decodeErr != nil {
			return OrganizerRun{}, decodeErr
		}
	} else {
		r.mu.Lock()
		run, _ = r.history[runID]
		r.mu.Unlock()
		if run.RunID == "" {
			return OrganizerRun{}, fmt.Errorf("organizer run %q not found", runID)
		}
	}
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	claimed, shouldApply, err := r.store.ClaimOrganizeRunApply(runID)
	if err != nil {
		return run, err
	}
	if !shouldApply {
		completed, decodeErr := organizerRunFromStore(claimed)
		if decodeErr != nil {
			return run, decodeErr
		}
		r.rememberRun(completed)
		return completed, nil
	}
	if claimedRun, decodeErr := organizerRunFromStore(claimed); decodeErr == nil {
		run = claimedRun
	} else {
		return run, decodeErr
	}
	result, applyErr := r.store.ApplyBatch(memory.BatchArgs{Workdir: run.Workdir, Trigger: "organizer-manual", Model: run.Model, Decisions: run.Pending})
	if applyErr != nil {
		run.Status = "failed"
		run.Warnings = append(run.Warnings, applyErr.Error())
	} else {
		run.Applied = len(result.Created) + len(result.Updated) + len(result.Deleted)
		run.Pending = nil
		run.Status = "completed"
	}
	run.Phase = "complete"
	run.UpdatedAt = r.now()
	if saveErr := r.saveRun(run); saveErr != nil {
		if applyErr != nil {
			return run, errors.Join(applyErr, saveErr)
		}
		return run, saveErr
	}
	return run, applyErr
}

func (r *Runtime) OrganizerHistory() []OrganizerRun {
	// The runtime is request-scoped, so history must be reconstructed from the
	// store rather than relying on its in-memory cache.
	if result, err := r.store.ListOrganizeRuns(); err == nil {
		if stored, ok := result["runs"].([]memory.OrganizeRun); ok {
			for _, value := range stored {
				if run, decodeErr := organizerRunFromStore(value); decodeErr == nil {
					r.rememberRun(run)
				}
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]OrganizerRun, 0, len(r.history))
	for _, run := range r.history {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (r *Runtime) saveRun(run OrganizerRun) error {
	payload := memory.OrganizeRun{
		"runId": run.RunID, "epoch": run.Epoch, "executorId": run.Executor, "phase": run.Phase, "status": run.Status, "trigger": run.Trigger,
		"workdir": run.Workdir, "model": run.Model, "scanned": run.Scanned, "clusters": run.Clusters,
		"applied": run.Applied, "risk": run.Risk, "pending": run.Pending, "warnings": run.Warnings,
		"createdAt": run.CreatedAt.UnixMilli(), "updatedAt": run.UpdatedAt.UnixMilli(),
	}
	if _, err := r.store.SaveOrganizeRun(payload); err != nil {
		return err
	}
	r.rememberRun(run)
	return nil
}

func (r *Runtime) rememberRun(run OrganizerRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.history == nil {
		r.history = map[string]OrganizerRun{}
	}
	r.history[run.RunID] = run
}

func organizerRunFromStore(stored memory.OrganizeRun) (OrganizerRun, error) {
	data, err := json.Marshal(stored)
	if err != nil {
		return OrganizerRun{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return OrganizerRun{}, err
	}
	var run OrganizerRun
	decode := func(key string, target any) error {
		if value, ok := raw[key]; ok {
			return json.Unmarshal(value, target)
		}
		return nil
	}
	if err := decode("runId", &run.RunID); err != nil {
		return run, err
	}
	if err := decode("epoch", &run.Epoch); err != nil {
		return run, err
	}
	if err := decode("executorId", &run.Executor); err != nil {
		return run, err
	}
	var trigger string
	if err := decode("trigger", &trigger); err != nil {
		return run, err
	}
	run.Trigger = OrganizerTrigger(trigger)
	if err := decode("phase", &run.Phase); err != nil {
		return run, err
	}
	if err := decode("status", &run.Status); err != nil {
		return run, err
	}
	if err := decode("workdir", &run.Workdir); err != nil {
		return run, err
	}
	if err := decode("model", &run.Model); err != nil {
		return run, err
	}
	if err := decode("scanned", &run.Scanned); err != nil {
		return run, err
	}
	if err := decode("clusters", &run.Clusters); err != nil {
		return run, err
	}
	if err := decode("applied", &run.Applied); err != nil {
		return run, err
	}
	if err := decode("risk", &run.Risk); err != nil {
		return run, err
	}
	if err := decode("pending", &run.Pending); err != nil {
		return run, err
	}
	if err := decode("warnings", &run.Warnings); err != nil {
		return run, err
	}
	for key, target := range map[string]*time.Time{"createdAt": &run.CreatedAt, "updatedAt": &run.UpdatedAt} {
		if value, ok := raw[key]; ok {
			var number int64
			if err := json.Unmarshal(value, &number); err == nil {
				*target = time.UnixMilli(number)
				continue
			}
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return run, err
			}
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return run, err
			}
			*target = parsed
		}
	}
	return run, nil
}

// StartOrganizerScheduler is a host-owned wakeup loop. It only invokes the
// K-brain organizer model configured on this Runtime.
func (r *Runtime) StartOrganizerScheduler(ctx context.Context, workdir string, interval time.Duration) func() {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	scheduleCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ticker := time.NewTicker(interval)
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-scheduleCtx.Done():
				return
			case <-ticker.C:
				_, _ = r.RunOrganizer(scheduleCtx, workdir, OrganizerScheduled)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
func (r *Runtime) cancelRun(run OrganizerRun, err error) (OrganizerRun, error) {
	stored, changed, cancelErr := r.store.CancelOrganizeRunOwned(run.RunID, run.Epoch, run.Executor, err.Error())
	if cancelErr != nil {
		return run, errors.Join(err, cancelErr)
	}
	if !changed {
		// Another request may have claimed apply after the context was cancelled.
		// Never overwrite that persisted state with a local cancelled snapshot.
		if stored != nil {
			if current, decodeErr := organizerRunFromStore(stored); decodeErr == nil {
				r.rememberRun(current)
				return current, err
			}
		}
		return run, err
	}
	cancelled, decodeErr := organizerRunFromStore(stored)
	if decodeErr != nil {
		return run, errors.Join(err, decodeErr)
	}
	r.rememberRun(cancelled)
	return cancelled, err
}
func newExecutorID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "executor-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("executor-%d", time.Now().UnixNano())
}

func randomRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "organize-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("organize-%d", time.Now().UnixNano())
}
func clusterEntries(entries []memory.Meta) [][]memory.Meta {
	groups := map[string][]memory.Meta{}
	for _, e := range entries {
		key := e.Scope + "/" + e.WorkdirHash + "/" + e.MemoryType
		groups[key] = append(groups[key], e)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]memory.Meta, 0, len(keys))
	for _, k := range keys {
		out = append(out, groups[k])
	}
	return out
}
