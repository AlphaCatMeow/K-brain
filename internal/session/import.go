package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// ImportHistory writes a canonical history under a caller-supplied stable ID.
// Existing imports are accepted only when their source fingerprint matches.
func filepathForImport(s *Store, id, cwd string) string {
	if !s.projectScoped {
		return filepath.Join(s.filesDir, id)
	}
	return filepath.Join(s.filesDir, projectDir(cwd), id)
}

func makeImportDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func (s *Store) ImportHistory(id, sourceID, fingerprint, contentFingerprint, metadata, cwd, model, provider, title string, createdAt, updatedAt time.Time, pinned, shared, redact bool, shareToken string, messages []ai.Message, boundary *Compaction) (bool, error) {
	id = strings.TrimSpace(id)
	sourceID = strings.TrimSpace(sourceID)
	fingerprint = strings.TrimSpace(fingerprint)
	contentFingerprint = strings.TrimSpace(contentFingerprint)
	if !validID(id) || sourceID == "" || fingerprint == "" || contentFingerprint == "" {
		return false, errors.New("invalid import identity")
	}
	returnValue := false
	err := s.withLock(func() error {
		existing, readErr := s.read(id)
		if readErr == nil {
			if existing.Meta.ImportSourceID != sourceID || existing.Meta.ImportFingerprint != fingerprint || existing.Meta.ImportContentFingerprint != contentFingerprint {
				return fmt.Errorf("import source %q conflicts with existing session", sourceID)
			}
			returnValue = true
			return nil
		}
		if !errors.Is(readErr, ErrNotFound) {
			return readErr
		}
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		data := newData(Meta{
			ID: id, Title: title, CWD: cwd, Model: model, Provider: provider,
			CreatedAt: createdAt, UpdatedAt: updatedAt, Pinned: pinned, Shared: shared,
			ShareToken: shareToken, ShareRedactTool: redact,
			ImportSourceID: sourceID, ImportFingerprint: fingerprint, ImportContentFingerprint: contentFingerprint, ImportMetadata: metadata,
		})
		data.CreatedAt = createdAt
		for index, message := range messages {
			data.Messages[index] = message
		}
		if boundary != nil {
			data.Compactions[1] = *boundary
		}
		dir := filepathForImport(s, id, cwd)
		if err := makeImportDir(dir); err != nil {
			return err
		}
		if err := s.write(data); err != nil {
			return err
		}
		return nil
	})
	return returnValue, err
}
