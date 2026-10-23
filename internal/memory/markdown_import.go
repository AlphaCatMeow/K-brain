package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ImportMarkdown copies the open remember/forget markdown entries into
// unreviewed global user memory, once per store. Later edits to the file are not
// followed: it stays in place for hosts that still read it (TUI,
// non-interactive kn), and the store is the backend's only writable memory.
// Entries the store rejects are skipped and reported in the returned error.
func (s *Store) ImportMarkdown(scope Scope) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil || state.MarkdownImported {
		return 0, err
	}
	imported := 0
	var skipped error
	for _, entry := range scope.Entries() {
		text := strings.TrimSpace(entry.Text)
		if entry.Done || text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		description := []rune(text)
		if len(description) > 80 {
			description = description[:80]
		}
		// Slugs derive from the text, so a retry after an interrupted import
		// overwrites instead of duplicating.
		if _, err := s.writeLocked(WriteArgs{Slug: "imported-" + hex.EncodeToString(sum[:6]), Scope: "global", MemoryType: "user", Description: string(description), Body: text, Actor: "migration"}); err != nil {
			var rejected *StoreError
			if errors.As(err, &rejected) {
				skipped = errors.Join(skipped, fmt.Errorf("entry %d: %w", entry.N, err))
				continue
			}
			return imported, err
		}
		imported++
	}
	state.MarkdownImported = true
	if err := s.saveState(state); err != nil {
		return imported, err
	}
	return imported, skipped
}
