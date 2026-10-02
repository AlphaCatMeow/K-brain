package memory

import (
	"encoding/json"
	"fmt"
	"time"
)

// Dispatch executes the explicitly supported native memory command set.
func (s *Store) Dispatch(command string, raw json.RawMessage) (any, error) {
	var args struct {
		Args json.RawMessage `json:"args"`
	}
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &args)
	}
	if len(args.Args) == 0 {
		args.Args = raw
	}
	decode := func(v any) error {
		if len(args.Args) == 0 || string(args.Args) == "null" {
			return nil
		}
		return json.Unmarshal(args.Args, v)
	}
	switch command {
	case "memory_list":
		var v ListArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.List(v)
	case "memory_read":
		var v ReadArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Read(v)
	case "memory_search":
		var v SearchArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Search(v)
	case "memory_write":
		var v WriteArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Write(v)
	case "memory_update":
		var v UpdateArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Update(v)
	case "memory_delete":
		var v DeleteArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Delete(v)
	case "memory_accept":
		var v ReadArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Accept(v)
	case "memory_apply_batch":
		var v BatchArgs
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.ApplyBatch(v)
	case "memory_overview", "memory_index_overview":
		var v struct {
			Workdir string `json:"workdir"`
		}
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.Overview(v.Workdir)
	case "memory_quota_summary":
		var v struct {
			Workdir string `json:"workdir"`
		}
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.QuotaSummary(v.Workdir)
	case "memory_recent_rejections":
		return s.RecentRejections()
	case "memory_delete_project":
		var v struct {
			Workdir string `json:"workdir"`
			Actor   string `json:"actor"`
			Reason  string `json:"reason"`
		}
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.DeleteProject(v.Workdir, v.Actor, v.Reason)
	case "memory_paths_info":
		return s.PathsInfo()
	case "memory_today_local_date":
		return s.TodayLocalDate(), nil
	case "memory_today_daily":
		v, err := s.TodayDaily()
		if err != nil && isNotFound(err) {
			return nil, nil
		}
		return v, err
	case "memory_wipe_all":
		return s.WipeAll()
	case "memory_organize_due_claim":
		return s.ClaimOrganizeDue()
	case "memory_organize_due_complete":
		var v OrganizeRun
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.UpdateOrganizeRun(v)
	case "memory_organize_run_create", "memory_organizer_run_create":
		var v OrganizeRun
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.CreateOrganizeRun(v)
	case "memory_organize_run_update", "memory_organizer_run_update":
		var v OrganizeRun
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.UpdateOrganizeRun(v)
	case "memory_organize_run_list", "memory_organizer_run_list":
		return s.ListOrganizeRuns()
	case "memory_organize_run_read", "memory_organizer_run_read":
		var v struct {
			RunID string `json:"runId"`
		}
		if err := decode(&v); err != nil {
			return nil, err
		}
		return s.ReadOrganizeRun(v.RunID)
	case "memory_organize_run_clear_history", "memory_organizer_run_clear_history":
		return s.ClearOrganizeHistory()
	default:
		return nil, storeError("unsupported_command", fmt.Sprintf("unsupported memory command %q", command))
	}
}

func (s *Store) RecentRejections() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": state.Rejections}, nil
}

func (s *Store) CreateOrganizeRun(run OrganizeRun) (OrganizeRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == nil {
		run = OrganizeRun{}
	}
	if _, ok := run["runId"]; !ok {
		run["runId"] = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	if _, ok := run["createdAt"]; !ok {
		run["createdAt"] = time.Now().UnixMilli()
	}
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	if epoch, ok := organizerEpoch(run); ok {
		if epoch != state.OrganizerEpoch {
			return nil, storeError("stale_organizer_executor", "organizer run belongs to an old reset generation")
		}
	} else {
		if state.OrganizerEpoch != 0 {
			return nil, storeError("stale_organizer_executor", "organizer run lacks the current reset generation")
		}
		run["epoch"] = state.OrganizerEpoch
	}
	for _, current := range state.Runs {
		if current["runId"] == run["runId"] {
			return nil, storeError("already_exists", "organizer run already exists")
		}
	}
	state.Runs = append(state.Runs, run)
	if err := s.saveState(state); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Store) UpdateOrganizeRun(run OrganizeRun) (OrganizeRun, error) {
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
	for i := range state.Runs {
		if state.Runs[i]["runId"] == id {
			epoch, hasEpoch := organizerEpoch(run)
			if err := validateOrganizerUpdate(state.Runs[i], epoch, hasEpoch, organizerExecutor(run), organizerStatus(run)); err != nil {
				return state.Runs[i], err
			}
			state.Runs[i] = run
			if err := s.saveState(state); err != nil {
				return nil, err
			}
			return run, nil
		}
	}
	return nil, storeError("not_found", "organizer run not found")
}

func (s *Store) ListOrganizeRuns() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": state.Runs}, nil
}

func (s *Store) ReadOrganizeRun(id string) (OrganizeRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	for _, run := range state.Runs {
		if run["runId"] == id {
			return run, nil
		}
	}
	return nil, storeError("not_found", "organizer run not found")
}

func (s *Store) ClearOrganizeHistory() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	deleted := len(state.Runs)
	state.Runs = nil
	state.OrganizerEpoch++
	if err := s.saveState(state); err != nil {
		return nil, err
	}
	return map[string]any{"deletedCount": deleted, "retainedActiveCount": 0, "organizerEpoch": state.OrganizerEpoch}, nil
}
