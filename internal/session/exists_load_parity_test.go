package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exists answers whether Load would succeed: Load matches by prefix over exact directory
// names, so a shared prefix is ambiguous and a differently cased id does not match.
func TestExistsAgreesWithLoadForPrefixAndCase(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	short, err := st.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(st.TranscriptPath(short))
	if err != nil {
		t.Fatal(err)
	}
	// A second session whose id starts with the first id (an imported UUID-like id).
	long := short + "-0000-4000-8000-000000000000"
	if err := os.MkdirAll(filepath.Join(dir, long), 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(raw), `"`+short+`"`, `"`+long+`"`, 1)
	if err := os.WriteFile(filepath.Join(dir, long, "session.jsonl"), []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(short)
	got, err := st.Exists([]string{short, long, upper})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{short, long, upper} {
		_, _, loadErr := st.Load(id)
		if got[id] != (loadErr == nil) {
			t.Errorf("Exists(%s)=%v but Load err=%v", id, got[id], loadErr)
		}
	}
}
