package memory

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// OrganizerEpoch returns the current reset generation. A run from an older
// generation cannot be recreated after history or memory has been cleared.
func (s *Store) OrganizerEpoch() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return 0, err
	}
	return state.OrganizerEpoch, nil
}

// SaveOrganizeRun upserts by persisted identity and preserves optional report fields.
// Runs carrying an executor identity are compare-and-set updates: a late
// executor cannot overwrite a reset, cancellation, terminal state, or a newer
// executor's claim.
func (s *Store) SaveOrganizeRun(run OrganizeRun) (OrganizeRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _ := run["runId"].(string)
	if id == "" {
		return nil, storeError("run_id_required", "runId is required")
	}
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	incomingEpoch, hasEpoch := organizerEpoch(run)
	incomingExecutor, _ := run["executorId"].(string)
	incomingStatus, _ := run["status"].(string)
	for i, current := range state.Runs {
		if current["runId"] != id {
			continue
		}
		if err := validateOrganizerUpdate(current, incomingEpoch, hasEpoch, incomingExecutor, incomingStatus); err != nil {
			return current, err
		}
		for key, value := range run {
			current[key] = value
		}
		state.Runs[i] = current
		if err := s.saveState(state); err != nil {
			return nil, err
		}
		return current, nil
	}
	if hasEpoch && incomingEpoch != state.OrganizerEpoch {
		return nil, storeError("stale_organizer_executor", "organizer run belongs to an old reset generation")
	}
	if !hasEpoch {
		if state.OrganizerEpoch != 0 {
			return nil, storeError("stale_organizer_executor", "organizer run lacks the current reset generation")
		}
		run["epoch"] = state.OrganizerEpoch
	}
	state.Runs = append(state.Runs, run)
	if err := s.saveState(state); err != nil {
		return nil, err
	}
	return run, nil
}

func organizerEpoch(run OrganizeRun) (uint64, bool) {
	value, ok := run["epoch"]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case uint64:
		return v, true
	case uint:
		return uint64(v), true
	case int:
		return uint64(v), v >= 0
	case int64:
		return uint64(v), v >= 0
	case float64:
		return uint64(v), v >= 0
	default:
		return 0, false
	}
}

func validateOrganizerUpdate(current OrganizeRun, epoch uint64, hasEpoch bool, executor, status string) error {
	currentEpoch, currentHasEpoch := organizerEpoch(current)
	if currentHasEpoch && ((hasEpoch && currentEpoch != epoch) || (!hasEpoch && currentEpoch != 0)) {
		return storeError("stale_organizer_executor", "organizer executor belongs to an old reset generation")
	}
	if currentExecutor, _ := current["executorId"].(string); currentExecutor != "" && (executor == "" || currentExecutor != executor) {
		return storeError("stale_organizer_executor", "organizer executor is no longer current")
	}
	currentStatus, _ := current["status"].(string)
	if isOrganizerTerminal(currentStatus) {
		return storeError("stale_organizer_executor", "organizer run is already terminal")
	}
	return nil
}

func isOrganizerTerminal(status string) bool {
	switch status {
	case "cancelled", "completed", "failed":
		return true
	default:
		return false
	}
}

func organizerExecutor(run OrganizeRun) string {
	executor, _ := run["executorId"].(string)
	return executor
}

func organizerStatus(run OrganizeRun) string {
	status, _ := run["status"].(string)
	return status
}

// CancelOrganizeRun changes only a reviewable run. Once apply ownership has
// been claimed, cancellation leaves that state untouched.
func (s *Store) CancelOrganizeRun(id, warning string) (OrganizeRun, bool, error) {
	return s.cancelOrganizeRun(id, 0, "", false, warning)
}

// CancelOrganizeRunOwned cancels only when the caller still owns the run.
func (s *Store) CancelOrganizeRunOwned(id string, epoch uint64, executor, warning string) (OrganizeRun, bool, error) {
	return s.cancelOrganizeRun(id, epoch, executor, true, warning)
}

func (s *Store) cancelOrganizeRun(id string, epoch uint64, executor string, owned bool, warning string) (OrganizeRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, false, err
	}
	for i, run := range state.Runs {
		if run["runId"] != id {
			continue
		}
		if owned {
			currentEpoch, hasEpoch := organizerEpoch(run)
			if (hasEpoch && currentEpoch != epoch) || organizerExecutor(run) != executor {
				return run, false, nil
			}
		}
		status, _ := run["status"].(string)
		if status != "pending_review" && status != "running" {
			return run, false, nil
		}
		run["status"] = "cancelled"
		run["phase"] = "cancelled"
		run["warnings"] = []string{warning}
		run["updatedAt"] = time.Now().UnixMilli()
		state.Runs[i] = run
		if err := s.saveState(state); err != nil {
			return nil, false, err
		}
		return run, true, nil
	}
	return nil, false, storeError("not_found", "organizer run not found")
}

func newOrganizerExecutorID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "executor-" + hex.EncodeToString(b[:])
	}
	return "executor-" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
}

// ClaimOrganizeRunApply persists ownership before any batch side effects.
// An interrupted applying run is not replayed automatically.
func (s *Store) ClaimOrganizeRunApply(id string) (OrganizeRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, false, err
	}
	for i, run := range state.Runs {
		if run["runId"] != id {
			continue
		}
		switch run["status"] {
		case "completed":
			return run, false, nil
		case "applying":
			return nil, false, storeError("already_applying", "organizer run is already being applied")
		case "pending_review":
			run["executorId"] = newOrganizerExecutorID()
			run["status"] = "applying"
			run["phase"] = "apply"
			run["updatedAt"] = time.Now().UnixMilli()
			state.Runs[i] = run
			if err := s.saveState(state); err != nil {
				return nil, false, err
			}
			return run, true, nil
		default:
			return nil, false, storeError("not_pending_review", "organizer run is not pending review")
		}
	}
	return nil, false, storeError("not_found", "organizer run not found")
}

func (s *Store) ClaimOrganizeDue() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	for i, run := range state.Runs {
		status, _ := run["status"].(string)
		if status != "pending" && status != "scheduled" {
			continue
		}
		due, _ := run["dueAt"].(float64)
		if due > 0 && int64(due) > now {
			continue
		}
		run["status"] = "running"
		run["claimedAt"] = now
		state.Runs[i] = run
		if err := s.saveState(state); err != nil {
			return nil, err
		}
		return map[string]any{"run": run, "skippedReason": nil}, nil
	}
	return map[string]any{"run": nil, "skippedReason": "no_due_run"}, nil
}
