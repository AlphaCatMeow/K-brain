package imaging

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

func newProjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("ip_%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func (s *Store) CreateProject(ctx context.Context, scope Scope, title, workspaceRef string) (ImageProject, error) {
	if strings.TrimSpace(title) == "" {
		return ImageProject{}, invalid("project title required")
	}
	id, err := newProjectID()
	if err != nil {
		return ImageProject{}, err
	}
	var out ImageProject
	err = s.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		out = ImageProject{ID: id, Title: title, WorkspaceRef: workspaceRef, Revision: 1, CreatedAt: tx.now, UpdatedAt: tx.now}
		return tx.PutProject(out)
	})
	if err != nil {
		return ImageProject{}, err
	}
	return out, nil
}
func (s *Store) UpdateProject(ctx context.Context, scope Scope, id, title string, expectedRevision uint64) (ImageProject, error) {
	if strings.TrimSpace(title) == "" {
		return ImageProject{}, invalid("project title required")
	}
	var out ImageProject
	err := s.Update(WithScope(ctx, scope), func(tx *Transaction) error {
		p, err := tx.ReadProject(scope, id)
		if err != nil {
			return err
		}
		if p.Revision != expectedRevision {
			return conflict()
		}
		p.Title = title
		p.Revision++
		p.UpdatedAt = tx.now
		out = p
		return tx.PutProject(p)
	})
	if err != nil {
		return ImageProject{}, err
	}
	return out, nil
}

// result_unknown is terminal: no worker owns the job (the lease check below covers a
// worker still exiting), and reconciling reopens it as running before any lookup.
func projectJobBusy(status JobStatus) bool {
	switch status {
	case JobSucceeded, JobPartialSucceeded, JobFailed, JobCancelled, JobResultUnknown:
		return false
	default:
		return true
	}
}

// A lease protects a worker even after local cancellation or lease expiry.
// Only worker exit (or recovery after restart) proves it no longer uses the job.
func (tx *Transaction) jobHasLease(id string) bool {
	for _, item := range tx.data.Executions[id].Items {
		if item.LeaseOwner != "" {
			return true
		}
	}
	return false
}

func (tx *Transaction) DeleteJob(id string) error {
	if err := tx.check(); err != nil {
		return err
	}
	r, ok := tx.data.Jobs[id]
	if !ok || !r.Scope.matches(tx.scope) {
		return notFound()
	}
	if r.DeletedAt != nil {
		return nil
	}
	if projectJobBusy(r.Value.Status) || tx.jobHasLease(id) {
		return &ImageError{Code: CodeProjectBusy}
	}
	deleted := tx.now
	r.DeletedAt = &deleted
	tx.data.Jobs[id] = r
	tx.mutations = append(tx.mutations, storeMutation{Kind: "job", Job: &r})
	for key, entry := range tx.data.Idempotency {
		if entry.Scope == r.Scope && entry.Value.JobID == id {
			entry.Value.ExpiresAt = tx.now.Add(30 * 24 * time.Hour)
			tx.data.Idempotency[key] = entry
			v := entry
			tx.mutations = append(tx.mutations, storeMutation{Kind: "idempotency", Idempotency: &v})
		}
	}
	return nil
}
func (tx *Transaction) DeleteProject(id string, expectedRevision uint64) error {
	if err := tx.check(); err != nil {
		return err
	}
	r, ok := tx.data.Projects[id]
	if !ok || !r.Scope.matches(tx.scope) {
		return notFound()
	}
	if r.DeletedAt != nil {
		return nil
	}
	if r.Value.Revision != expectedRevision {
		return conflict()
	}
	for _, j := range tx.data.Jobs {
		if j.Scope == r.Scope && j.Value.ProjectID == id && j.DeletedAt == nil && (projectJobBusy(j.Value.Status) || tx.jobHasLease(j.Value.ID)) {
			return &ImageError{Code: CodeProjectBusy}
		}
	}
	for jobID, j := range tx.data.Jobs {
		if j.Scope == r.Scope && j.Value.ProjectID == id && j.DeletedAt == nil {
			if err := tx.DeleteJob(jobID); err != nil {
				return err
			}
		}
	}
	deleted := tx.now
	r.DeletedAt = &deleted
	tx.data.Projects[id] = r
	tx.mutations = append(tx.mutations, storeMutation{Kind: "project", Project: &r})
	return nil
}
func (s *Store) DeleteProject(ctx context.Context, scope Scope, id string, expectedRevision uint64) error {
	return s.Update(WithScope(ctx, scope), func(tx *Transaction) error { return tx.DeleteProject(id, expectedRevision) })
}
func (s *Store) DeleteJob(ctx context.Context, scope Scope, id string) error {
	return s.Update(WithScope(ctx, scope), func(tx *Transaction) error { return tx.DeleteJob(id) })
}
