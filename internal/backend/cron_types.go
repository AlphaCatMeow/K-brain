package backend

import "encoding/json"

type CronTask struct {
	ID string `json:"id"`
	// SessionID binds prompt tasks to one durable canonical conversation.
	SessionID           string            `json:"sessionId,omitempty"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	Cron                string            `json:"cron"`
	Enabled             bool              `json:"enabled"`
	RemainingExecutions *uint64           `json:"remainingExecutions,omitempty"`
	TimeoutSeconds      uint64            `json:"timeoutSeconds"`
	Type                string            `json:"type"`
	Script              string            `json:"script,omitempty"`
	Requests            []CronHTTPRequest `json:"requests,omitempty"`
	Prompt              string            `json:"prompt,omitempty"`
	SelectedModel       *CronModelRef     `json:"selectedModel,omitempty"`
	Reasoning           string            `json:"reasoning,omitempty"`
	Workdir             string            `json:"workdir,omitempty"`
	LastError           string            `json:"lastError,omitempty"`
}

type CronHTTPRequest struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type CronModelRef struct {
	CustomProviderID string `json:"customProviderId"`
	Model            string `json:"model"`
}

type CronSnapshot struct {
	Revision uint64      `json:"revision"`
	Tasks    []*CronTask `json:"tasks"`
}

type CronApplyInput struct {
	BaseRevision uint64          `json:"baseRevision"`
	Ops          []CronOperation `json:"ops"`
}

type CronOperation struct {
	Op    string         `json:"op"`
	ID    string         `json:"id,omitempty"`
	Item  map[string]any `json:"item,omitempty"`
	Patch map[string]any `json:"patch,omitempty"`
	IDs   []string       `json:"ids,omitempty"`
}

type CronApplyResponse struct {
	Status string       `json:"status"`
	Cron   CronSnapshot `json:"cron"`
}

type CronRunRecord struct {
	SessionID         string `json:"sessionId,omitempty"`
	RunID             string `json:"runId,omitempty"`
	Counted           bool   `json:"counted"`
	ID                string `json:"id"`
	TaskID            string `json:"taskId"`
	State             string `json:"state"`
	Success           bool   `json:"success"`
	StartedAt         int64  `json:"startedAt"`
	FinishedAt        *int64 `json:"finishedAt,omitempty"`
	DurationMs        uint64 `json:"durationMs"`
	ExitCode          *int   `json:"exitCode,omitempty"`
	Output            string `json:"output"`
	TerminationReason string `json:"terminationReason,omitempty"`
}

type CronRunNowResponse struct {
	ExecutionID string `json:"executionId"`
	StartedAt   int64  `json:"startedAt"`
}

type cronDisk struct {
	Revision uint64          `json:"revision"`
	Tasks    []*CronTask     `json:"tasks"`
	Runs     []CronRunRecord `json:"runs"`
}
