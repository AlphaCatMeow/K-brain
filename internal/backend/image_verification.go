package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// Receipts share the existing private, atomically replaced secret file. Reserving
// one before job acceptance closes the HTTP-loss window without adding a second
// job engine or a new Store schema. The Store's durable idempotency record is the
// authoritative job link, so a crash between acceptance and HTTP reply is safe.
type imageVerificationRecord struct {
	ID            string                      `json:"id"`
	Scope         imaging.Scope               `json:"-"`
	OwnerID       string                      `json:"ownerId"`
	BackendID     string                      `json:"backendId"`
	TargetID      string                      `json:"targetId"`
	Input         imaging.VerifyTargetRequest `json:"input"`
	BindingDigest string                      `json:"bindingDigest"`
	Target        imaging.TargetSnapshot      `json:"target"`
}

type ImageVerificationManager struct {
	settings *ImageSettingsManager
	service  *imaging.Service
	assets   ImageAssets
}

func NewImageVerificationManager(settings *ImageSettingsManager, service *imaging.Service, assets ImageAssets) *ImageVerificationManager {
	return &ImageVerificationManager{settings: settings, service: service, assets: assets}
}

func verificationID(scope imaging.Scope, key string) string {
	raw, _ := json.Marshal([]string{scope.OwnerID, scope.BackendID, key})
	sum := sha256.Sum256(raw)
	return "iv_" + hex.EncodeToString(sum[:])
}
func verificationJobKey(id string) string { return imaging.VerificationKeyPrefix + id }

// Called only while holding settings.mu. Private material is hashed, never
// returned. Resolving provider expressions catches env/file credential rotation
// even when the provider reference and expression themselves stay unchanged.
func (m *ImageSettingsManager) imageBindingDigestLocked(ctx context.Context, s imaging.ImageSettings, t imaging.ImageTarget) (string, error) {
	var c *imaging.ImageConnection
	for i := range s.Connections {
		if s.Connections[i].ID == t.ConnectionID {
			c = &s.Connections[i]
			break
		}
	}
	if c == nil || !c.Enabled {
		return "", imageError(imaging.CodeInvalidInput)
	}
	secrets := map[string]any{}
	var provider any
	if c.Binding.Kind == "provider_ref" {
		p, ok := m.store.cfg.Providers[c.Binding.ProviderID]
		if !ok {
			return "", imageError(imaging.CodeAuthRequired)
		}
		value, err := m.resolveProviderSecret(ctx, p.APIKey)
		if err != nil || value == "" {
			return "", imageError(imaging.CodeAuthRequired)
		}
		secrets["apiKey"] = value
		for _, h := range p.CustomHeaders {
			value, err := m.resolveProviderSecret(ctx, h.Value)
			if err != nil {
				return "", imageError(imaging.CodeAuthRequired)
			}
			secrets["header:"+h.Key] = value
		}
		provider = struct {
			URL, API, Expression string
			Proxy                bool
			Headers              any
		}{p.BaseURL, p.API, p.APIKey, p.UseSystemProxy, p.CustomHeaders}
	} else {
		ref := c.Binding.SecretRefs["apiKey"]
		if ref == "" {
			return "", imageError(imaging.CodeAuthRequired)
		}
		for purpose, ref := range c.Binding.SecretRefs {
			record, ok := m.secrets[ref]
			if !ok || !secretOwned(record, imaging.Scope{OwnerID: "local", BackendID: "local"}, c.ID, purpose) || record.Value == "" {
				return "", imageError(imaging.CodeAuthRequired)
			}
			secrets[purpose] = record
		}
	}
	snapshot := imaging.SnapshotTarget(t)
	snapshot.Name = ""
	raw, err := json.Marshal(struct {
		Target   imaging.TargetSnapshot
		Binding  imaging.ConnectionBinding
		Provider any
		Secrets  map[string]any
	}{snapshot, c.Binding, provider, secrets})
	if err != nil {
		return "", imageError(imaging.CodeInvalidInput)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func verificationTarget(s imaging.ImageSettings, id string) (imaging.ImageTarget, error) {
	for _, t := range s.Targets {
		if t.ID == id {
			return t, nil
		}
	}
	return imaging.ImageTarget{}, imageError(imaging.CodeNotFound)
}

func (v *ImageVerificationManager) Start(ctx context.Context, scope imaging.Scope, id, key string, input imaging.VerifyTargetRequest) (imaging.VerificationReceipt, bool, error) {
	empty := imaging.VerificationReceipt{}
	if err := checkImageScope(scope); err != nil {
		return empty, false, err
	}
	if v == nil || v.settings == nil || v.service == nil || v.assets == nil {
		return empty, false, imageError(imaging.CodeUnavailable)
	}
	if strings.TrimSpace(key) == "" || len(key) > 256 || input.ExpectedRevision == nil || input.TargetRevision == nil || *input.ExpectedRevision >= imaging.MaxSafeInteger || *input.TargetRevision > imaging.MaxSafeInteger || !input.ConfirmPaid || input.Operation != imaging.OperationGenerate || input.Count != 1 || strings.TrimSpace(input.Prompt) == "" {
		return empty, false, imageError(imaging.CodeInvalidInput)
	}
	m := v.settings
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	rid := verificationID(scope, key)
	record, replay := m.verifications[rid]
	if replay {
		if record.Scope != scope || record.TargetID != id || !reflect.DeepEqual(record.Input, input) {
			return empty, false, imageError(imaging.CodeIdempotencyConflict)
		}
		if job, err := v.service.VerificationJob(ctx, scope, verificationJobKey(rid)); err == nil {
			return receiptFor(record, job, m.currentLocked().Revision), true, nil
		} else {
			var ie *imaging.ImageError
			if !errors.As(err, &ie) || ie.Code != imaging.CodeNotFound {
				return empty, true, err
			}
		}
	}
	s := m.currentLocked()
	if s.Revision != *input.ExpectedRevision {
		return empty, replay, imageError(imaging.CodeRevisionConflict)
	}
	t, err := verificationTarget(s, id)
	if err != nil {
		return empty, replay, err
	}
	if input.ConnectionID != t.ConnectionID || input.RemoteID != t.RemoteID || *input.TargetRevision != t.Revision {
		return empty, replay, imageError(imaging.CodeRevisionConflict)
	}
	if err := imaging.ValidateVerificationProfile(imaging.SnapshotTarget(t)); err != nil {
		return empty, replay, err
	}
	digest, err := m.imageBindingDigestLocked(ctx, s, t)
	if err != nil {
		return empty, replay, err
	}
	request := imaging.CreateJobRequest{ProjectID: input.ProjectID, TargetID: id, Operation: imaging.OperationGenerate, Count: 1, Prompt: input.Prompt, Parameters: map[string]any{}}
	if err := imaging.ValidateRequest(request, imaging.SnapshotTarget(t)); err != nil {
		return empty, replay, err
	}
	if replay && record.BindingDigest != digest {
		return empty, true, imageError(imaging.CodeRevisionConflict)
	}
	if !replay {
		record = imageVerificationRecord{ID: rid, Scope: scope, OwnerID: scope.OwnerID, BackendID: scope.BackendID, TargetID: id, Input: input, BindingDigest: digest, Target: imaging.SnapshotTarget(t)}
		record.Target.VerificationID = rid
		next := maps.Clone(m.verifications)
		next[rid] = record
		if err := saveImageSecrets(m.secretPath, imageSecretFile{Version: 1, Secrets: m.secrets, Verifications: next}); err != nil {
			return empty, false, imageError(imaging.CodeUnavailable)
		}
		m.verifications = next
	}
	job, jobReplay, err := v.service.CreateVerificationJob(ctx, scope, verificationJobKey(rid), request, record.Target)
	if err != nil {
		return empty, replay, err
	}
	return receiptFor(record, job, s.Revision), replay || jobReplay, nil
}

func receiptFor(record imageVerificationRecord, job imaging.ImageJob, revision uint64) imaging.VerificationReceipt {
	state := "pending"
	switch job.Status {
	case imaging.JobFailed, imaging.JobCancelled, imaging.JobPartialSucceeded:
		state = "failed"
	case imaging.JobResultUnknown:
		state = "unknown"
	}
	return imaging.VerificationReceipt{ID: record.ID, TargetID: record.TargetID, JobID: job.ID, State: state, JobStatus: job.Status, SettingsRevision: revision}
}

// Get never submits work. Finalization is idempotent and settings publication is
// atomic. A failed settings save can be retried without touching the upstream.
func (v *ImageVerificationManager) Get(ctx context.Context, scope imaging.Scope, targetID, id string) (imaging.VerificationReceipt, error) {
	if err := checkImageScope(scope); err != nil {
		return imaging.VerificationReceipt{}, err
	}
	m := v.settings
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	record, ok := m.verifications[id]
	if !ok || record.Scope != scope || record.TargetID != targetID {
		return imaging.VerificationReceipt{}, imageError(imaging.CodeNotFound)
	}
	job, err := v.service.VerificationJob(ctx, scope, verificationJobKey(id))
	if err != nil {
		return imaging.VerificationReceipt{}, err
	}
	s := m.currentLocked()
	receipt := receiptFor(record, job, s.Revision)
	t, err := verificationTarget(s, targetID)
	if err != nil {
		receipt.State = "stale"
		receipt.Reason = "target_changed"
		return receipt, nil
	}
	digest, err := m.imageBindingDigestLocked(ctx, s, t)
	if err != nil || digest != record.BindingDigest {
		receipt.State = "stale"
		receipt.Reason = "binding_changed"
		return receipt, nil
	}
	if t.Verification.JobID == job.ID && t.Verification.BindingDigest == digest {
		receipt.State = "verified"
		return receipt, nil
	}
	if s.Revision != *record.Input.ExpectedRevision {
		receipt.State = "stale"
		receipt.Reason = "revision_changed"
		return receipt, nil
	}
	if job.Status != imaging.JobSucceeded {
		return receipt, nil
	}
	if len(job.Items) != 1 || job.Items[0].Status != imaging.JobSucceeded || len(job.AssetIDs) != 1 || len(job.Items[0].OutputAssetIDs) != 1 || job.Items[0].OutputAssetIDs[0] != job.AssetIDs[0] || job.TargetSnapshot.VerificationID != id {
		receipt.State = "blocked"
		receipt.Reason = "missing_output"
		return receipt, nil
	}
	// Open the committed original: metadata alone is insufficient evidence.
	reader, asset, err := v.assets.OpenAsset(ctx, scope, job.AssetIDs[0], "original")
	if err != nil {
		receipt.State = "blocked"
		receipt.Reason = "missing_output"
		return receipt, nil
	}
	closeErr := reader.Close()
	if closeErr != nil || !asset.Available || asset.JobID != job.ID || asset.Bytes <= 0 || asset.MIME != "image/png" || asset.Width < 1 || asset.Height < 1 {
		receipt.State = "blocked"
		receipt.Reason = "invalid_output"
		return receipt, nil
	}
	for i := range s.Targets {
		if s.Targets[i].ID == targetID {
			s.Targets[i].Verification = imaging.TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Now().UTC(), ConnectionID: t.ConnectionID, AdapterVersion: t.AdapterVersion, TargetRevision: t.Revision, BindingDigest: digest, JobID: job.ID}
			s.Targets[i].VerificationState = nil
		}
	}
	s.Revision++
	if _, err := m.saveImageSettingsLocked(scope, s); err != nil {
		return receipt, err
	}
	receipt.State = "verified"
	receipt.SettingsRevision = s.Revision
	return receipt, nil
}

// CheckExecution prevents a queued verification from using a changed binding.
// Finalization independently checks again after persisted output is available.
func (v *ImageVerificationManager) CheckExecution(ctx context.Context, scope imaging.Scope, target imaging.TargetSnapshot) error {
	if target.VerificationID == "" {
		return nil
	}
	m := v.settings
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	record, ok := m.verifications[target.VerificationID]
	if !ok || record.Scope != scope || !reflect.DeepEqual(record.Target, target) {
		return imageError(imaging.CodeForbidden)
	}
	s := m.currentLocked()
	t, err := verificationTarget(s, record.TargetID)
	if err != nil {
		return err
	}
	digest, err := m.imageBindingDigestLocked(ctx, s, t)
	if err != nil || digest != record.BindingDigest || s.Revision != *record.Input.ExpectedRevision {
		return imageError(imaging.CodeRevisionConflict)
	}
	return imaging.ValidateVerificationProfile(target)
}

func (s *Server) imageVerification(w http.ResponseWriter, r *http.Request, auth ImageAuthorization, parts []string) {
	if !auth.CanConfigure {
		imageHTTPError(w, r, imageError(imaging.CodeForbidden))
		return
	}
	if s.image.Verification == nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	if len(parts) == 3 && parts[2] == "verify" {
		if !imageMethod(w, r, http.MethodPost) {
			return
		}
		var input imaging.VerifyTargetRequest
		if !imageDecode(w, r, &input) {
			return
		}
		receipt, replay, err := s.image.Verification.Start(r.Context(), auth.Scope, parts[1], r.Header.Get("X-Idempotency-Key"), input)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, http.StatusAccepted, map[string]any{"verification": receipt, "replay": replay})
		return
	}
	if len(parts) == 4 && parts[2] == "verifications" {
		if !imageMethod(w, r, http.MethodGet) {
			return
		}
		receipt, err := s.image.Verification.Get(r.Context(), auth.Scope, parts[1], parts[3])
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, http.StatusOK, map[string]any{"verification": receipt})
		return
	}
	imageHTTPError(w, r, imageError(imaging.CodeNotFound))
}
