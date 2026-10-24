package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// Two Store instances on one root (backend + agent tool) share the counter.
func TestWipeCounterSharedAcrossInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	a, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenStore(root + string(filepath.Separator) + ".")
	if err != nil {
		t.Fatal(err)
	}
	before := b.WipeEpoch()
	if _, err := a.WipeAll(); err != nil {
		t.Fatal(err)
	}
	if b.WipeEpoch() == before {
		t.Fatalf("b did not see a's wipe: %d", before)
	}
	if err := b.HoldUnjudged("s", before, []UnjudgedMessage{{ID: "u0", Text: "x"}}); err != nil {
		t.Fatal(err)
	}
	held, err := a.TakeUnjudged("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("pre-wipe hold via second instance persisted: %+v", held)
	}
}

// State unreadable at first open: counter starts at 0 although the file says
// 3. A later wipe moves it to 4: still a change, so a captured 0 is a wipe.
func TestWipeCounterFailedLoadThenWipe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".kbrain-state.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.WipeEpoch(); got != 0 {
		t.Fatalf("epoch after failed load = %d", got)
	}
	if err := os.WriteFile(path, []byte(`{"wipeEpoch":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	captured := s.WipeEpoch()
	// Reopening does not reload.
	s2, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if s2.WipeEpoch() != captured {
		t.Fatalf("reopen changed counter %d -> %d", captured, s2.WipeEpoch())
	}
	if err := s.HoldUnjudged("s", captured, []UnjudgedMessage{{ID: "u0", Text: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.WipeAll(); err != nil {
		t.Fatal(err)
	}
	if got := s.WipeEpoch(); got != 4 || got == captured {
		t.Fatalf("epoch after wipe = %d (captured %d)", got, captured)
	}
	if err := s.HoldUnjudged("s", captured, []UnjudgedMessage{{ID: "u1", Text: "y"}}); err != nil {
		t.Fatal(err)
	}
	held, _ := s.TakeUnjudged("s")
	if len(held) != 0 {
		t.Fatalf("held after wipe: %+v", held)
	}
}

// Boundary: Delete with a reason saves a zero state when the state read
// fails, resetting the persisted WipeEpoch; the next wipe then lands on the
// counter value an older caller captured.
func TestWipeCounterPersistedResetHidesWipe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WipeAll(); err != nil { // counter 1
		t.Fatal(err)
	}
	if _, err := s.Write(WriteArgs{Slug: "pref", Scope: "global", MemoryType: "user", Description: "d", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	captured := s.WipeEpoch()
	path := filepath.Join(s.Root(), ".kbrain-state.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(DeleteArgs{ReadArgs: ReadArgs{Slug: "pref", Scope: "global"}, Reason: "wrong"}); err != nil {
		t.Logf("delete: %v", err)
	}
	if _, err := s.WipeAll(); err != nil {
		t.Fatal(err)
	}
	if s.WipeEpoch() == captured {
		t.Fatalf("wipe invisible: counter %d == captured %d", s.WipeEpoch(), captured)
	}
}
