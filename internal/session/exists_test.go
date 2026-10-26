package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistsMatchesLoadInProjectScopedStore(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	// A transcript-less directory and a duplicated id under two projects both fail Load.
	if err := os.MkdirAll(filepath.Join(dir, "empty-session"), 0o700); err != nil {
		t.Fatal(err)
	}
	dup, err := st.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(st.TranscriptPath(dup))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "another-project-dir", dup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "another-project-dir", dup, "session.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := st.Exists([]string{id, dup, "empty-session", "missing", "../x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{id, dup, "empty-session", "missing"} {
		_, _, loadErr := st.Load(candidate)
		if got[candidate] != (loadErr == nil) {
			t.Fatalf("Exists(%s)=%v but Load err=%v", candidate, got[candidate], loadErr)
		}
	}
	if !got[id] || got[dup] {
		t.Fatalf("exists = %v", got)
	}
}
