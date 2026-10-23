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
//
// Each entry is claimed as pending before its write and recorded as done after
// it. The import runs before the backend serves, so a pending entry found on the
// next start cannot have been deleted by the user: it is kept if its write
// landed and written again if not. Done entries are never written again, so a
// deletion in the store sticks.
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
	finish := func(hash string) error {
		state.MarkdownPending = slices.DeleteFunc(state.MarkdownPending, func(h string) bool { return h == hash })
		state.MarkdownImports = append(state.MarkdownImports, hash)
		return s.saveState(state)
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
		slug := "imported-" + hash
		if slices.Contains(state.MarkdownImports, hash) {
			continue
		}
		if slices.Contains(state.MarkdownPending, hash) {
			current, err := s.load()
			if err != nil {
				return imported, err
			}
			if _, err := findEntry(current, ReadArgs{Slug: slug, Scope: "global"}); err == nil {
				if err := finish(hash); err != nil {
					return imported, err
				}
				imported++
				continue
			}
		} else {
			state.MarkdownPending = append(state.MarkdownPending, hash)
			if err := s.saveState(state); err != nil {
				return imported, err
			}
		}
		description := []rune(text)
		if len(description) > 80 {
			description = description[:80]
		}
		if _, err := s.writeLocked(WriteArgs{Slug: slug, Scope: "global", MemoryType: "user", Description: string(description), Body: text, Actor: "migration"}); err != nil {
			var rejected *StoreError
			if errors.As(err, &rejected) {
				// A rejected entry (too long, over quota) is not retried.
				skipped = errors.Join(skipped, fmt.Errorf("entry %d: %w", entry.N, err))
				if err := finish(hash); err != nil {
					return imported, errors.Join(skipped, err)
				}
				continue
			}
			// The failed write left no entry file; if dropping the claim cannot
			// be saved either, the pending claim still makes the next start
			// retry the write.
			state.MarkdownPending = slices.DeleteFunc(state.MarkdownPending, func(h string) bool { return h == hash })
			return imported, errors.Join(err, s.saveState(state))
		}
		if err := finish(hash); err != nil {
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
