package imaging

import (
	"context"
	"strings"
)

const VerificationKeyPrefix = "image-verification:"

// ValidateVerificationProfile deliberately certifies only the minimal generate
// profile. Extra operations/parameters need their own contract and live tests.
func ValidateVerificationProfile(t TargetSnapshot) error {
	if t.Adapter == "openai-images" && t.RemoteID != "gpt-image-2" && t.RemoteID != "gpt-image-2-2026-04-21" {
		return ErrUnsupported
	}
	if t.Adapter == "volcengine-ark-image" && t.RemoteID != "doubao-seedream-5-0-pro-260628" {
		return ErrUnsupported
	}
	if t.Adapter == "google-generate-content-image" && t.RemoteID != "gemini-nano-banana-2.1" {
		return ErrUnsupported
	}
	if t.Adapter == "rolldek-images" && t.RemoteID != "gpt-image-2.5" && t.RemoteID != "gpt-image-2" {
		return ErrUnsupported
	}
	if t.Adapter == "runninghub-model" && t.RemoteID != "rhart-image-n-g31-flash-lite-official" {
		return ErrUnsupported
	}
	c := t.Capabilities
	if (t.Adapter != "runninghub-model" && t.Adapter != "rolldek-images" && t.Adapter != "openai-images" && t.Adapter != "volcengine-ark-image" && t.Adapter != "google-generate-content-image") || t.AdapterVersion != "1" || t.Kind != "model" || t.WorkflowMapping != nil || len(t.Parameters) != 0 || len(c.Operations) != 1 || c.Operations[0] != OperationGenerate || c.MaxCount != 1 || c.MaxInputs != 0 || c.SupportsCancel || c.SupportsReconcile || c.SupportsContinuation || c.SupportsPreview || len(c.OutputFormats) != 1 || c.OutputFormats[0] != "png" {
		return ErrUnsupported
	}
	return nil
}

// CreateVerificationJob is a trusted backend entry point, never a public job
// request option. The caller must hold its configuration lock, validate consent
// and persist its receipt before calling. Execution uses the ordinary scheduler,
// submission journal and asset transaction; it does not change target enablement.
func (s *Service) CreateVerificationJob(ctx context.Context, scope Scope, key string, r CreateJobRequest, target TargetSnapshot) (ImageJob, bool, error) {
	if s.store == nil || !strings.HasPrefix(key, VerificationKeyPrefix) || len(key) > 256 || len(key) == len(VerificationKeyPrefix) {
		return ImageJob{}, false, invalid("invalid verification key")
	}
	if err := ValidateVerificationProfile(target); err != nil {
		return ImageJob{}, false, err
	}
	if r.Operation != OperationGenerate || r.Count != 1 || len(r.Inputs) != 0 || len(r.Parameters) != 0 || r.ParentAssetID != "" || r.TargetID != target.ID {
		return ImageJob{}, false, ErrUnsupported
	}
	hash, err := HashRequest(r)
	if err != nil {
		return ImageJob{}, false, err
	}
	return s.acceptJob(ctx, scope, key, hash, r, target)
}

// VerificationJob reads the durable idempotency journal, including after restart.
func (s *Service) VerificationJob(ctx context.Context, scope Scope, key string) (ImageJob, error) {
	if err := ctx.Err(); err != nil {
		return ImageJob{}, err
	}
	if s.store == nil || !strings.HasPrefix(key, VerificationKeyPrefix) {
		return ImageJob{}, notFound()
	}
	record, ok := s.store.GetIdempotency(scope, key)
	if !ok {
		return ImageJob{}, notFound()
	}
	return s.store.ReadJob(scope, record.JobID)
}
