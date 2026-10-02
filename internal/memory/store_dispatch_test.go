package memory

import (
	"encoding/json"
	"testing"
)

func TestDispatchAllowlistAndNativeCommands(t *testing.T) {
	s := newStore(t)
	if _, err := s.Dispatch("exec", json.RawMessage(`{}`)); err == nil {
		t.Fatal("arbitrary command accepted")
	}
	if _, err := s.Dispatch("memory_paths_info", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch("memory_apply_batch", json.RawMessage(`{"dailyAppend":{"bullet":"entry"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch("memory_today_daily", nil); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizerRunPersistence(t *testing.T) {
	s := newStore(t)
	created, err := s.Dispatch("memory_organize_run_create", json.RawMessage(`{"trigger":"manual","status":"pending"}`))
	if err != nil {
		t.Fatal(err)
	}
	run, ok := created.(OrganizeRun)
	if !ok {
		t.Fatalf("%T", created)
	}
	if run["runId"] == nil {
		t.Fatal("missing run id")
	}
	if _, err := s.Dispatch("memory_organize_run_update", mustJSON(t, run)); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Dispatch("memory_organize_run_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.(map[string]any)["runs"].([]OrganizeRun)) != 1 {
		t.Fatal("run not listed")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
