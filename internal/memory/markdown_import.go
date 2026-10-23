package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ImportMarkdown copies the open remember/forget markdown entries into
// unreviewed global user memory, once per store. Later edits to the file are not
// followed: it stays in place for hosts that still read it (TUI,
// non-interactive kn), and the store is the backend's only writable memory.
// Entries the store rejects are skipped and reported in the returned error; a
// read or I/O failure leaves the import unfinished so the next start retries.
func (s *Store) ImportMarkdown(scope Scope) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state()
	if err != nil || state.MarkdownImported {
		return 0, err
	}
	entries, err := scope.ReadEntries()
	if err != nil {
		return 0, fmt.Errorf("read %s memory: %w", scope.Name, err)
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
		// Claim the entry before writing it: an interrupted import may lose
		// this entry, but a retry never brings back one deleted since.
		state.MarkdownImports = append(state.MarkdownImports, hash)
		if err := s.saveState(state); err != nil {
			return imported, err
		}
		description := []rune(text)
		if len(description) > 80 {
			description = description[:80]
		}
		if _, err := s.writeLocked(WriteArgs{Slug: "imported-" + hash, Scope: "global", MemoryType: "user", Description: string(description), Body: text, Actor: "migration"}); err != nil {
			var rejected *StoreError
			if errors.As(err, &rejected) {
				skipped = errors.Join(skipped, fmt.Errorf("entry %d: %w", entry.N, err))
				continue
			}
			// A failed write leaves no entry file, so releasing the claim lets
			// the next start retry it without resurrecting anything.
			state.MarkdownImports = state.MarkdownImports[:len(state.MarkdownImports)-1]
			_ = s.saveState(state)
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
