package session

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxExistsIDs bounds one existence query.
const MaxExistsIDs = 5000

// Exists reports, for each id, whether Load(id) would find exactly one session, using the
// same id listing and prefix rule as Load but only file metadata, so callers can confirm many
// sessions without reading their transcripts.
func (s *Store) Exists(ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	err := s.withLock(func() error {
		all, err := s.ids()
		if errors.Is(err, os.ErrNotExist) {
			all = nil
		} else if err != nil {
			return err
		}
		sort.Strings(all)
		for _, id := range ids {
			if !validID(id) || out[id] {
				continue
			}
			i := sort.SearchStrings(all, id)
			if i >= len(all) || !strings.HasPrefix(all[i], id) ||
				(i+1 < len(all) && strings.HasPrefix(all[i+1], id)) {
				continue // no match, or ambiguous like Load
			}
			path := s.TranscriptPath(all[i])
			if path == "" {
				continue
			}
			ok, err := transcriptPresent(filepath.Dir(path))
			if err != nil {
				return err
			}
			out[id] = ok
		}
		return nil
	})
	return out, err
}

// transcriptPresent mirrors the directory and file checks read applies before opening.
func transcriptPresent(dir string) (bool, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	info, err = os.Lstat(filepath.Join(dir, "session.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
