package imaging

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type snapshotPayload struct {
	Version   int       `json:"version"`
	CursorKey []byte    `json:"cursorKey"`
	Data      storeData `json:"data"`
}

func (s *Store) recover() error {
	snapshotPath := filepath.Join(s.dir, "snapshot.json")
	walPath := filepath.Join(s.dir, "transactions.wal")
	fresh := false
	b, err := os.ReadFile(snapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		fresh = true
		// A WAL without its checkpoint/key is never interpreted as a fresh store.
		if _, walErr := os.Stat(walPath); walErr == nil {
			return corrupt("snapshot missing", errors.New("WAL already exists"))
		} else if !errors.Is(walErr, os.ErrNotExist) {
			return walErr
		}
		s.cursorKey = make([]byte, 32)
		if _, err = rand.Read(s.cursorKey); err != nil {
			return err
		}
		if err = s.writeSnapshot(); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		r, err := decodeRecord(b)
		if err != nil {
			return corrupt("snapshot", err)
		}
		var p snapshotPayload
		if err = decodeStrict(r.Payload, &p); err != nil {
			return corrupt("snapshot payload", err)
		}
		if p.Version != 1 || len(p.CursorKey) != 32 {
			return corrupt("snapshot", errors.New("unsupported version or invalid cursor key"))
		}
		if err = validateStoreData(p.Data); err != nil {
			return corrupt("snapshot state", err)
		}
		s.data = p.Data
		s.seq = r.TxSeq
		s.checkpointSeq = r.TxSeq
		s.cursorKey = p.CursorKey
	}
	flags := os.O_RDWR
	if fresh {
		flags |= os.O_CREATE | os.O_EXCL
	}
	s.wal, err = os.OpenFile(walPath, flags, 0600)
	if err != nil {
		return corrupt("open WAL", err)
	}
	if err = s.wal.Sync(); err != nil {
		return err
	}
	if err = syncStoreDirectory(s.dir); err != nil {
		return err
	}
	return s.replayWAL()
}
func (s *Store) Checkpoint() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStoreClosed
	}
	if s.poisoned != nil {
		return s.poisoned
	}
	err := s.checkpoint()
	s.maintenanceErr = err
	return err
}
func (s *Store) writeSnapshot() error {
	b, err := encodeRecord(s.seq, snapshotPayload{Version: 1, CursorKey: s.cursorKey, Data: s.data})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "snapshot-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = s.hit("snapshot.before_write"); err != nil {
		return err
	}
	n, err := f.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if err = s.hit("snapshot.before_sync"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = s.hit("snapshot.after_sync"); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.hit("snapshot.before_rename"); err != nil {
		return err
	}
	if err = replaceStoreFile(name, filepath.Join(s.dir, "snapshot.json")); err != nil {
		return err
	}
	if err = syncStoreDirectory(s.dir); err != nil {
		return err
	}
	if err = s.hit("snapshot.after_rename"); err != nil {
		return err
	}
	return nil
}
func (s *Store) checkpoint() error {
	if err := s.writeSnapshot(); err != nil {
		return err
	}
	// The checkpoint is durable before truncating even one byte of the old WAL.
	// Replay verifies old checksums, skipping only transactions covered by it.
	s.checkpointSeq = s.seq
	if err := s.hit("wal.before_rotate"); err != nil {
		return err
	}
	if err := s.wal.Truncate(0); err != nil {
		s.poisoned = fmt.Errorf("IMAGE_STORE_UNCERTAIN: WAL rotation: %w", err)
		return s.poisoned
	}
	if err := s.wal.Sync(); err != nil {
		s.poisoned = fmt.Errorf("IMAGE_STORE_UNCERTAIN: WAL rotation sync: %w", err)
		return s.poisoned
	}
	if _, err := s.wal.Seek(0, io.SeekStart); err != nil {
		s.poisoned = err
		return err
	}
	s.walBytes = 0
	return s.hit("wal.after_rotate")
}
