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
// it. A pending entry found on the next start is kept if its write landed and
// written again if not; deleting a pending entry, or wiping the store, records
// it as done first, so a deletion sticks even when the import did not finish.
// Done entries are never written again. State is re-read before every save so
// a concurrent writer of the state file (another host on the same store) loses
// as little as possible.
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
		return s.updateStateLocked(func(state *storeState) {
			state.MarkdownPending = without(state.MarkdownPending, hash)
			if !slices.Contains(state.MarkdownImports, hash) {
				state.MarkdownImports = append(state.MarkdownImports, hash)
			}
		})
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
		state, err := s.state()
		if err != nil {
			return imported, err
		}
		if state.MarkdownImported || slices.Contains(state.MarkdownImports, hash) {
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
		} else if err := s.updateStateLocked(func(state *storeState) {
			state.MarkdownPending = append(state.MarkdownPending, hash)
		}); err != nil {
			return imported, err
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
			return imported, errors.Join(err, s.updateStateLocked(func(state *storeState) {
				state.MarkdownPending = without(state.MarkdownPending, hash)
			}))
		}
		if err := finish(hash); err != nil {
			return imported, err
		}
		imported++
	}
	if err := s.updateStateLocked(func(state *storeState) { state.MarkdownImported = true }); err != nil {
		return imported, err
	}
	return imported, skipped
}

// settleImportClaimLocked records a pending memory.md import as done when its
// entry is deleted, before the file goes, so no later import writes it back.
func (s *Store) settleImportClaimLocked(slug string) error {
	hash, ok := strings.CutPrefix(slug, "imported-")
	if !ok {
		return nil
	}
	state, err := s.state()
	if err != nil || !slices.Contains(state.MarkdownPending, hash) {
		return err
	}
	return s.updateStateLocked(func(state *storeState) {
		state.MarkdownPending = without(state.MarkdownPending, hash)
		state.MarkdownImports = append(state.MarkdownImports, hash)
	})
}

// updateStateLocked applies change to a freshly read state and saves it.
func (s *Store) updateStateLocked(change func(*storeState)) error {
	state, err := s.state()
	if err != nil {
		return err
	}
	change(&state)
	return s.saveState(state)
}

func without(hashes []string, hash string) []string {
	return slices.DeleteFunc(hashes, func(h string) bool { return h == hash })
}
