package imaging

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

// AssetGCOptions configures a single maintenance pass. Zero durations select the
// defaults; negative durations are rejected. Now is independent of wall-clock time.
// This API is internal maintenance: it deliberately takes no caller-owned Scope.
type AssetGCOptions struct {
	Now                func() time.Time
	UploadRetention    time.Duration
	TombstoneRetention time.Duration
	OrphanRetention    time.Duration
	GracePeriod        time.Duration
}

type AssetGCResult struct {
	MarkedAssets      int
	MarkedFiles       int
	UnavailableAssets int
	RemovedFiles      int
	DeferredBusy      bool
}

const assetGCManifestName = ".asset-gc.json"
const assetGCManifestTemp = ".asset-gc.pending"
const assetGCManifestLimit = 16 << 20

type assetGCFileMark struct {
	MarkedAt time.Time `json:"markedAt"`
	Modified time.Time `json:"modified"`
	Size     int64     `json:"size"`
}
type assetGCManifest struct {
	Version int                        `json:"version"`
	Assets  map[string]time.Time       `json:"assets"`
	Files   map[string]assetGCFileMark `json:"files"`
}

func newAssetGCManifest() assetGCManifest {
	return assetGCManifest{Version: 1, Assets: map[string]time.Time{}, Files: map[string]assetGCFileMark{}}
}

func (o AssetGCOptions) defaults() (AssetGCOptions, error) {
	values := []*time.Duration{&o.UploadRetention, &o.TombstoneRetention, &o.OrphanRetention, &o.GracePeriod}
	defaults := []time.Duration{7 * 24 * time.Hour, 30 * 24 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, value := range values {
		if *value < 0 {
			return o, invalid("invalid asset retention")
		}
		if *value == 0 {
			*value = defaults[i]
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o, nil
}

func assetGCFailure(code string) error {
	// Never return filesystem errors, filenames, hashes or persisted JSON values.
	return &ImageError{Code: code}
}

// CollectGarbage marks candidates durably and only sweeps candidates seen again
// after GracePeriod. Durable asset records remain as unavailable metadata, keeping
// historical references and tombstones valid. No project WorkspaceRef is opened.
//
// Writers must use the same AssetStore and hold its mutex through import+commit.
// SaveOutput callers must have a durable busy job or a nonempty execution lease
// until their output commit; either defers the entire pass, including otherwise
// unidentifiable staged outputs. Lease deadlines never authorize reclamation.
// Standalone staged output users must finish their commit before running this API.
// Lock order is AssetStore.mu, then Store.mu; never call it inside Store.Update.
func (s *AssetStore) CollectGarbage(ctx context.Context, options AssetGCOptions) (AssetGCResult, error) {
	var result AssetGCResult
	o, err := options.defaults()
	if err != nil {
		return result, err
	}
	now := o.Now().UTC()
	if now.IsZero() {
		return result, invalid("invalid asset GC clock")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.store.closed || s.store.poisoned != nil {
		return result, assetGCFailure(CodeUnavailable)
	}
	if err := validateStoreData(s.store.data); err != nil {
		return result, assetGCFailure(CodeStoreCorrupt)
	}
	previous, err := s.readAssetGCManifest()
	if err != nil {
		return result, err
	}
	keep, busy := assetGCRetained(s.store.data, now, o)
	if busy {
		// Invalidate old grace periods as well as preserving all uncommitted files.
		result.DeferredBusy = true
		return result, s.writeAssetGCManifest(newAssetGCManifest())
	}
	keptHashes := map[string]bool{}
	knownHashes := map[string]bool{}
	for id, record := range s.store.data.Assets {
		if !assetGCHex(record.Value.Hash, 64) {
			return result, assetGCFailure(CodeStoreCorrupt)
		}
		knownHashes[record.Value.Hash] = true
		if keep[id] {
			keptHashes[record.Value.Hash] = true
		}
	}
	next := newAssetGCManifest()
	waitingHashes := map[string]bool{}
	var mutations []storeMutation
	for id, record := range s.store.data.Assets {
		if keep[id] {
			continue
		}
		marked, exists := previous.Assets[id]
		if !exists || marked.After(now) {
			marked = now
			result.MarkedAssets++
		}
		next.Assets[id] = marked
		if now.Before(marked.Add(o.GracePeriod)) {
			waitingHashes[record.Value.Hash] = true
		} else if record.Value.Available {
			r := record
			r.Value.Available = false
			mutations = append(mutations, storeMutation{Kind: "asset", Asset: &r})
		}
	}
	files, err := s.assetGCFiles()
	if err != nil {
		return result, err
	}
	var remove []string
	for name, info := range files {
		hash, temporary, valid := assetGCFileName(name)
		if !valid || keptHashes[hash] {
			continue
		}
		if (temporary || !knownHashes[hash]) && (info.ModTime().IsZero() || now.Before(info.ModTime().Add(o.OrphanRetention))) {
			continue
		}
		mark, exists := previous.Files[name]
		if !exists || mark.MarkedAt.After(now) || !mark.Modified.Equal(info.ModTime()) || mark.Size != info.Size() {
			mark = assetGCFileMark{MarkedAt: now, Modified: info.ModTime(), Size: info.Size()}
			result.MarkedFiles++
		}
		next.Files[name] = mark
		// A shared hash cannot disappear while any of its records is still in
		// its grace period, even if the file had an older orphan mark.
		if !waitingHashes[hash] && !now.Before(mark.MarkedAt.Add(o.GracePeriod)) {
			remove = append(remove, name)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Persist the mark set before changing references or removing any content.
	if err := s.writeAssetGCManifest(next); err != nil {
		return result, err
	}
	if len(mutations) != 0 {
		// This private transaction spans all owners. Publish only after WAL sync,
		// exactly like Store.Update, while retaining both locks through the sweep.
		if err := s.store.appendTransaction(mutations); err != nil {
			return result, assetGCFailure(CodeUnavailable)
		}
		for _, mutation := range mutations {
			s.store.data.Assets[mutation.Asset.Value.ID] = *mutation.Asset
		}
		result.UnavailableAssets = len(mutations)
		if s.store.seq-s.store.checkpointSeq >= s.store.snapshotTransactions || s.store.walBytes >= s.store.snapshotBytes {
			s.store.maintenanceErr = s.store.checkpoint()
		}
	}
	for _, name := range remove {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// Lstat and Root.Remove never follow an entry symlink. Recheck identity
		// immediately before unlinking; directory entries are never traversed.
		info, err := s.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, assetGCFailure(CodeUnavailable)
		}
		mark := next.Files[name]
		if !info.Mode().IsRegular() || info.Size() != mark.Size || !info.ModTime().Equal(mark.Modified) {
			continue
		}
		if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, assetGCFailure(CodeUnavailable)
		}
		result.RemovedFiles++
	}
	// Stale marks are harmless and intentionally left until the next pass. A
	// crash after unlink needs no rollback; missing files simply leave the set.
	return result, nil
}

func assetGCRetained(data storeData, now time.Time, options AssetGCOptions) (map[string]bool, bool) {
	keep := map[string]bool{}
	expiredReferences := map[string]bool{}
	liveJobs := map[string]bool{}
	busy := false
	for id, job := range data.Jobs {
		active := assetGCJobBusy(job.Value.Status)
		unknown := job.Value.Status == JobResultUnknown
		for _, item := range job.Value.Items {
			active = active || assetGCJobBusy(item.Status)
			unknown = unknown || item.Status == JobResultUnknown
		}
		for _, item := range data.Executions[id].Items {
			// A cancelled or slow worker can still be saving output after its
			// lease deadline. Only clearing ownership proves it has finished.
			active = active || item.LeaseOwner != ""
		}
		busy = busy || active
		retained := active || unknown || job.DeletedAt == nil || now.Before(job.DeletedAt.Add(options.TombstoneRetention))
		liveJobs[id] = retained
		ids := jobAssetIDs(job.Value)
		for _, event := range data.Events[id] {
			if event.Data.Asset != nil {
				ids = append(ids, event.Data.Asset.ID)
			}
		}
		for _, assetID := range ids {
			if retained {
				keep[assetID] = true
			} else {
				expiredReferences[assetID] = true
			}
		}
	}
	for id, record := range data.Assets {
		asset := record.Value
		if asset.JobID != "" && liveJobs[asset.JobID] {
			keep[id] = true
		}
		switch asset.Source {
		case "upload":
			if asset.CreatedAt.IsZero() || now.Before(asset.CreatedAt.Add(options.UploadRetention)) {
				keep[id] = true
			}
		case "generated":
			_, knownJob := data.Jobs[asset.JobID]
			if !expiredReferences[id] && (asset.JobID == "" || !knownJob || liveJobs[asset.JobID]) {
				// Unattached generated artwork never automatically expires.
				keep[id] = true
			}
		default:
			keep[id] = true
		}
	}
	// Parent relationships are content references even without a live job.
	queue := make([]string, 0, len(keep))
	for id := range keep {
		queue = append(queue, id)
	}
	for len(queue) != 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, parent := range data.Assets[id].Value.ParentAssetIDs {
			if !keep[parent] {
				keep[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return keep, busy
}

// Unknown work retains its own references without pinning unrelated files. A
// persisted worker lease, checked separately, pins staged files across cancellation.
func assetGCJobBusy(status JobStatus) bool {
	switch status {
	case JobSucceeded, JobPartialSucceeded, JobFailed, JobCancelled, JobResultUnknown:
		return false
	default:
		return true
	}
}

func assetGCHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func assetGCFileName(name string) (hash string, temporary, valid bool) {
	if strings.HasPrefix(name, "tmp_") && assetGCHex(strings.TrimPrefix(name, "tmp_"), 32) {
		return "", true, true
	}
	for _, suffix := range []string{".original", ".thumbnail"} {
		if strings.HasSuffix(name, suffix) {
			hash = strings.TrimSuffix(name, suffix)
			return hash, false, assetGCHex(hash, 64)
		}
	}
	return "", false, false
}
func (s *AssetStore) assetGCFiles() (map[string]os.FileInfo, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, assetGCFailure(CodeUnavailable)
	}
	defer f.Close()
	files := map[string]os.FileInfo{}
	for {
		entries, err := f.ReadDir(128)
		if err != nil && err != io.EOF {
			return nil, assetGCFailure(CodeUnavailable)
		}
		for _, entry := range entries {
			if _, _, valid := assetGCFileName(entry.Name()); !valid {
				continue
			}
			info, statErr := s.root.Lstat(entry.Name())
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return nil, assetGCFailure(CodeUnavailable)
			}
			if info.Mode().IsRegular() {
				files[entry.Name()] = info
			}
		}
		if err == io.EOF {
			return files, nil
		}
	}
}
func (s *AssetStore) readAssetGCManifest() (assetGCManifest, error) {
	out := newAssetGCManifest()
	info, err := s.root.Lstat(assetGCManifestName)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > assetGCManifestLimit {
		return out, assetGCFailure(CodeStoreCorrupt)
	}
	f, err := s.root.Open(assetGCManifestName)
	if err != nil {
		return out, assetGCFailure(CodeUnavailable)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, assetGCManifestLimit+1))
	if err != nil || len(b) > assetGCManifestLimit {
		return out, assetGCFailure(CodeStoreCorrupt)
	}
	record, err := decodeRecord(b)
	if err != nil || record.TxSeq != 1 || decodeStrict(record.Payload, &out) != nil || out.Version != 1 || out.Assets == nil || out.Files == nil {
		return out, assetGCFailure(CodeStoreCorrupt)
	}
	for id, marked := range out.Assets {
		if id == "" || marked.IsZero() {
			return out, assetGCFailure(CodeStoreCorrupt)
		}
	}
	for name, mark := range out.Files {
		if _, _, valid := assetGCFileName(name); !valid || mark.MarkedAt.IsZero() || mark.Size < 0 {
			return out, assetGCFailure(CodeStoreCorrupt)
		}
	}
	return out, nil
}
func (s *AssetStore) writeAssetGCManifest(manifest assetGCManifest) error {
	b, err := encodeRecord(1, manifest)
	if err != nil || len(b) > assetGCManifestLimit {
		return assetGCFailure(CodeUnavailable)
	}
	// A crashed pending file is replaced by a fresh exclusive file. Remove only
	// this fixed internal name, never a path taken from metadata or a manifest.
	if err := s.root.Remove(assetGCManifestTemp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return assetGCFailure(CodeUnavailable)
	}
	f, err := s.root.OpenFile(assetGCManifestTemp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return assetGCFailure(CodeUnavailable)
	}
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return assetGCFailure(CodeUnavailable)
	}
	if err := s.root.Rename(assetGCManifestTemp, assetGCManifestName); err != nil {
		return assetGCFailure(CodeUnavailable)
	}
	if runtime.GOOS != "windows" {
		dir, err := s.root.Open(".")
		if err != nil {
			return assetGCFailure(CodeUnavailable)
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil || closeErr != nil {
			return assetGCFailure(CodeUnavailable)
		}
	}
	return nil
}
