package imaging

import "context"

// JobRecord is the private service-facing view of a job and its execution state.
// A missing execution entry is represented by an empty Execution with the job ID.
// Only Job may be returned to a transport, after PublicJob projection.
type JobRecord struct {
	Job       ImageJob
	Execution ExecutionRecord
}

func (JobRecord) MarshalJSON() ([]byte, error) {
	return nil, invalid("job record is private")
}

func readJobRecord(d storeData, scope Scope, id string) (JobRecord, error) {
	job, err := readJob(d, scope, id)
	if err != nil {
		return JobRecord{}, err
	}
	record := JobRecord{Job: job, Execution: ExecutionRecord{JobID: id, Items: map[string]ItemExecution{}}}
	if execution, ok := d.Executions[id]; ok {
		if !execution.Scope.matches(scope) {
			return JobRecord{}, notFound()
		}
		record.Execution = executionFromDisk(execution)
	}
	return record, nil
}

// GetJobRecord returns a detached, scope-checked consistent view for execution.
func (s *Store) GetJobRecord(scope Scope, id string) (JobRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return JobRecord{}, errStoreClosed
	}
	return readJobRecord(s.data, scope, id)
}

func (tx *Transaction) GetJobRecord(scope Scope, id string) (JobRecord, error) {
	if err := tx.check(); err != nil {
		return JobRecord{}, err
	}
	if scope != tx.scope {
		return JobRecord{}, notFound()
	}
	return readJobRecord(tx.data, scope, id)
}

// PutJobRecord stages the public job and its private execution in one transaction.
// The caller must also AppendEvent for the new job revision. For private-only
// changes (poll schedules, leases), use PutExecution without a public revision.
func (tx *Transaction) PutJobRecord(record JobRecord) error {
	if record.Execution.JobID != record.Job.ID {
		return invalid("execution/job identity mismatch")
	}
	if err := tx.PutJob(record.Job); err != nil {
		return err
	}
	return tx.PutExecution(record.Execution)
}

// ListJobRecords is the private service recovery listing. Cursors are distinct
// from public job cursors and bound to owner, backend, and project filter.
func (s *Store) ListJobRecords(ctx context.Context, scope Scope, projectID string, p PageRequest) (Page[JobRecord], error) {
	out := Page[JobRecord]{Items: []JobRecord{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return out, errStoreClosed
	}
	jobs, err := s.listJobs(scope, projectID, p, "jobRecords")
	if err != nil {
		return out, err
	}
	out.NextCursor = jobs.NextCursor
	for _, job := range jobs.Items {
		record, err := readJobRecord(s.data, scope, job.ID)
		if err != nil {
			return Page[JobRecord]{}, err
		}
		out.Items = append(out.Items, record)
	}
	return out, nil
}
