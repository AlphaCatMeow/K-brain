package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ImportMarkdown copies open remember/forget markdown entries that were not
// imported before into unreviewed global user memory. The markdown file stays
// in place for hosts that still read it (TUI, non-interactive kn). Entries the
// store rejects are skipped and reported in the returned error.
func (s *Store) ImportMarkdown(scope Scope) (int, error) {
	entries := scope.Entries()
	if len(entries) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil {
		return 0, err
	}
	imported := 0
	var skipped error
	for _, entry := range entries {
		text := strings.TrimSpace(entry.Text)
		if entry.Done || text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:6])
		if slices.Contains(state.MarkdownImports, hash) {
			continue
		}
		description := []rune(text)
		if len(description) > 80 {
			description = description[:80]
		}
		if _, err := s.writeLocked(WriteArgs{Slug: "imported-" + hash, Scope: "global", MemoryType: "user", Description: string(description), Body: text, Actor: "migration"}); err != nil {
			// A line the store rejects (too long, over quota) must not block
			// the entries after it; it is retried on the next start.
			var rejected *StoreError
			if errors.As(err, &rejected) {
				skipped = errors.Join(skipped, fmt.Errorf("entry %d: %w", entry.N, err))
				continue
			}
			return imported, err
		}
		state.MarkdownImports = append(state.MarkdownImports, hash)
		if err := s.saveState(state); err != nil {
			return imported, err
		}
		imported++
	}
	return imported, skipped
}
