package imaging

import (
	"encoding/json"
	"fmt"
	"time"
)

const WireVersion = "kbrain.image.v1"
const MaxSafeInteger uint64 = 1<<53 - 1

type Operation string

const (
	OperationGenerate  Operation = "generate"
	OperationReference Operation = "reference"
	OperationEdit      Operation = "edit"
	OperationMask      Operation = "mask"
)

type JobStatus string

const (
	JobQueued           JobStatus = "queued"
	JobRunning          JobStatus = "running"
	JobSaving           JobStatus = "saving"
	JobSucceeded        JobStatus = "succeeded"
	JobPartialSucceeded JobStatus = "partial_succeeded"
	JobFailed           JobStatus = "failed"
	JobCancelRequested  JobStatus = "cancel_requested"
	JobCancelled        JobStatus = "cancelled"
	JobResultUnknown    JobStatus = "result_unknown"
)

type InputAsset struct {
	AssetID string `json:"assetId"`
	Role    string `json:"role"`
}
type CreateJobRequest struct {
	ProjectID     string         `json:"projectId"`
	TargetID      string         `json:"targetId"`
	Operation     Operation      `json:"operation"`
	Prompt        string         `json:"prompt"`
	Inputs        []InputAsset   `json:"inputs"`
	Parameters    map[string]any `json:"parameters"`
	Count         int            `json:"count"`
	ParentAssetID string         `json:"parentAssetId,omitempty"`
}

// Scope is supplied by trusted authentication, never by a public request.
type Scope struct {
	OwnerID   string `json:"-"`
	BackendID string `json:"-"`
}
type PageRequest struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type Usage struct {
	Amount    *string `json:"amount"`
	Currency  string  `json:"currency"`
	Source    string  `json:"source"`
	Estimated bool    `json:"estimated"`
}
type ImageProject struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	WorkspaceRef string    `json:"workspaceRef,omitempty"`
	Revision     uint64    `json:"revision"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
type ImageJob struct {
	ID             string           `json:"id"`
	ProjectID      string           `json:"projectId"`
	Request        CreateJobRequest `json:"request"`
	TargetSnapshot TargetSnapshot   `json:"targetSnapshot"`
	Status         JobStatus        `json:"status"`
	Revision       uint64           `json:"revision"`
	AssetIDs       []string         `json:"assetIds"`
	Items          []ImageItem      `json:"items"`
	Usage          *Usage           `json:"usage"`
	CancelEffect   string           `json:"cancelEffect,omitempty"`
	Error          *ImageError      `json:"error,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}
type ImageItem struct {
	ID             string      `json:"id"`
	Status         JobStatus   `json:"status"`
	AttemptCount   int         `json:"attemptCount"`
	OutputAssetIDs []string    `json:"outputAssetIds"`
	Error          *ImageError `json:"error,omitempty"`
}

// ImageAttempt is private execution history; it is not a wire object.
type ImageAttempt struct {
	AttemptNo       int         `json:"-"`
	StartedAt       time.Time   `json:"-"`
	SubmissionState string      `json:"-"`
	RequestID       string      `json:"-"`
	ProviderHandle  string      `json:"-"`
	Delivery        Delivery    `json:"-"`
	Error           *ImageError `json:"-"`
	Usage           *Usage      `json:"-"`
}
type ImageAsset struct {
	ID             string    `json:"id"`
	Hash           string    `json:"hash"`
	MIME           string    `json:"mime"`
	Bytes          int64     `json:"bytes"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Source         string    `json:"source"`
	ParentAssetIDs []string  `json:"parentAssetIds"`
	JobID          string    `json:"jobId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	Available      bool      `json:"available"`
}
type ImageEvent struct {
	JobID     string         `json:"jobId"`
	Seq       uint64         `json:"seq"`
	Revision  uint64         `json:"revision"`
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      ImageEventData `json:"data"`
}
type ImageEventData struct {
	Status JobStatus   `json:"status,omitempty"`
	ItemID string      `json:"itemId,omitempty"`
	Asset  *ImageAsset `json:"asset,omitempty"`
	Error  *ImageError `json:"error,omitempty"`
}

const (
	EventJobAccepted      = "job.accepted"
	EventJobStateChanged  = "job.state_changed"
	EventItemStateChanged = "item.state_changed"
	EventAssetReady       = "asset.ready"
	EventJobFinished      = "job.finished"
)

type ImageSecretInput struct {
	ConnectionID string `json:"connectionId"`
	Purpose      string `json:"purpose"`
	Value        string `json:"value"` // Write-only request, never a settings field.
}

// The secret input is decoded only; accidental response/persistence serialization fails.
func (ImageSecretInput) MarshalJSON() ([]byte, error) {
	return nil, invalid("secret input is write-only")
}
func (ImageAttempt) MarshalJSON() ([]byte, error) {
	return nil, invalid("attempt history cannot be serialized")
}

type ImageSecretResult struct {
	Ref        string `json:"ref"`
	Configured bool   `json:"configured"`
}

func safeMarshal(v any, integers ...uint64) ([]byte, error) {
	for _, n := range integers {
		if n > MaxSafeInteger {
			return nil, fmt.Errorf("%s: integer exceeds wire range", CodeInvalidInput)
		}
	}
	return json.Marshal(v)
}
func (p ImageProject) MarshalJSON() ([]byte, error) {
	type wire ImageProject
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return safeMarshal(wire(p), p.Revision)
}
func (j ImageJob) MarshalJSON() ([]byte, error) {
	type wire ImageJob
	j = PublicJob(j)
	return safeMarshal(wire(j), j.Revision)
}
func (e ImageEvent) MarshalJSON() ([]byte, error) {
	type wire ImageEvent
	e = PublicEvent(e)
	if e.Type == EventJobFinished && e.Data.Status == "" {
		return nil, invalid("job.finished requires status")
	}
	return safeMarshal(wire(e), e.Seq, e.Revision)
}
func (a ImageAsset) MarshalJSON() ([]byte, error) {
	type wire ImageAsset
	a.CreatedAt = a.CreatedAt.UTC()
	if a.Bytes < 0 || uint64(a.Bytes) > MaxSafeInteger {
		return nil, invalid("invalid asset bytes")
	}
	return json.Marshal(wire(a))
}

// Public projections list every permitted field instead of reflecting persistence.
func PublicJob(j ImageJob) ImageJob {
	items := make([]ImageItem, len(j.Items))
	for i, v := range j.Items {
		items[i] = ImageItem{ID: v.ID, Status: v.Status, AttemptCount: v.AttemptCount, OutputAssetIDs: append([]string{}, v.OutputAssetIDs...), Error: PublicError(v.Error)}
	}
	return ImageJob{ID: j.ID, ProjectID: j.ProjectID, Request: cloneRequest(j.Request), TargetSnapshot: cloneSnapshot(j.TargetSnapshot), Status: j.Status, Revision: j.Revision, AssetIDs: append([]string{}, j.AssetIDs...), Items: items, Usage: cloneUsage(j.Usage), CancelEffect: j.CancelEffect, Error: PublicError(j.Error), CreatedAt: j.CreatedAt.UTC(), UpdatedAt: j.UpdatedAt.UTC()}
}
func PublicAsset(a ImageAsset) ImageAsset {
	return ImageAsset{ID: a.ID, Hash: a.Hash, MIME: a.MIME, Bytes: a.Bytes, Width: a.Width, Height: a.Height, Source: a.Source, ParentAssetIDs: append([]string{}, a.ParentAssetIDs...), JobID: a.JobID, CreatedAt: a.CreatedAt.UTC(), Available: a.Available}
}
func PublicEvent(e ImageEvent) ImageEvent {
	d := ImageEventData{Status: e.Data.Status, ItemID: e.Data.ItemID, Error: PublicError(e.Data.Error)}
	if e.Data.Asset != nil {
		a := PublicAsset(*e.Data.Asset)
		d.Asset = &a
	}
	return ImageEvent{JobID: e.JobID, Seq: e.Seq, Revision: e.Revision, Type: e.Type, Timestamp: e.Timestamp.UTC(), Data: d}
}
func cloneUsage(u *Usage) *Usage {
	if u == nil {
		return nil
	}
	v := *u
	if u.Amount != nil {
		s := *u.Amount
		v.Amount = &s
	}
	return &v
}
