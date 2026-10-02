package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type cronPromptExecutor func(context.Context, *CronTask, string, *CronRunRecord) (bool, string)

type cronActiveRun struct {
	id     string
	cancel context.CancelFunc
}

type cronManager struct {
	store          *cronStore
	defaultCWD     string
	promptExecutor cronPromptExecutor
	policy         func(string) *sandbox.Policy
	mu             sync.Mutex
	active         map[string]*cronActiveRun
	stop           chan struct{}
	done           chan struct{}
	closed         bool
	workers        sync.WaitGroup
}

func newCronManager(store *session.Store, defaultCWD string, settings *SettingsStore) (*cronManager, error) {
	persisted, err := newCronStore(store)
	if err != nil {
		return nil, err
	}
	m := &cronManager{store: persisted, defaultCWD: defaultCWD, active: map[string]*cronActiveRun{}, stop: make(chan struct{}), done: make(chan struct{})}
	if settings != nil {
		m.policy = func(cwd string) *sandbox.Policy { return settings.Snapshot().Sandbox.Policy(cwd) }
	}
	return m, nil
}
func (m *cronManager) close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.stop)
		for _, run := range m.active {
			run.cancel()
		}
	}
	m.mu.Unlock()
	<-m.done
	m.workers.Wait()
}
func (m *cronManager) loop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	defer close(m.done)
	last := time.Now().Unix()
	for {
		select {
		case now := <-ticker.C:
			if now.Unix() != last {
				last = now.Unix()
				m.tick(now)
			}
		case <-m.stop:
			return
		}
	}
}
func (m *cronManager) tick(now time.Time) {
	for _, task := range m.store.snapshot().Tasks {
		if !task.Enabled || task.RemainingExecutions != nil && *task.RemainingExecutions == 0 {
			continue
		}
		schedule, err := parseCron(task.Cron)
		if err != nil {
			m.report(m.store.setError(task.ID, err.Error()))
			continue
		}
		if schedule.matches(now) {
			if _, err := m.start(task.ID, true); err != nil {
				if err.Error() == "Cron task is already running." {
					m.report(m.store.recordSkipped(task.ID))
				} else {
					log.Printf("cron %s: %v", task.ID, err)
				}
			}
		}
	}
}
func (m *cronManager) start(taskID string, counted bool) (CronRunNowResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return CronRunNowResponse{}, errors.New("cron scheduler is closed")
	}
	if m.active[taskID] != nil {
		return CronRunNowResponse{}, errors.New("Cron task is already running.")
	}
	task, run, err := m.store.reserve(taskID, counted)
	if err != nil {
		return CronRunNowResponse{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.active[taskID] = &cronActiveRun{id: run.ID, cancel: cancel}
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer cancel()
		defer func() { m.mu.Lock(); delete(m.active, taskID); m.mu.Unlock() }()
		m.execute(ctx, task, counted, run)
	}()
	return CronRunNowResponse{ExecutionID: run.ID, StartedAt: run.StartedAt}, nil
}
func (m *cronManager) cancel(taskID, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.active[taskID]
	if active == nil {
		return errors.New("cron task has no active run")
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, run := range m.store.disk.Runs {
		if run.TaskID != taskID || run.ID != active.id || run.State != "leased" {
			continue
		}
		if runID != "" && runID != run.ID && runID != run.RunID {
			return errors.New("cron run is not the active execution")
		}
		active.cancel()
		return nil
	}
	return errors.New("cron task has no active run")
}
func (m *cronManager) runNow(taskID string) (CronRunNowResponse, error) {
	return m.start(taskID, false)
}

func (m *cronManager) execute(ctx context.Context, task *CronTask, counted bool, run CronRunRecord) {
	defer func() {
		if recovered := recover(); recovered != nil {
			run.Success = false
			run.Output = fmt.Sprintf("Scheduled execution failed: %v", recovered)
		}
		finished := time.Now().UnixMilli()
		if run.State != "expired" {
			run.State = "done"
		}
		run.FinishedAt = &finished
		run.DurationMs = uint64(max(int64(0), finished-run.StartedAt))
		if ctx.Err() != nil {
			run.Success = false
			run.State = "expired"
			if errors.Is(ctx.Err(), context.Canceled) {
				run.TerminationReason = "cancelled"
				run.Output = cronTerminationOutput(run.Output, "Cron run cancelled by user.")
			} else {
				run.TerminationReason = "timeout"
				run.Output = cronTerminationOutput(run.Output, "Cron run timed out.")
			}
		}
		m.report(m.store.recordCompletion(run, counted))
	}()
	cwd := strings.TrimSpace(task.Workdir)
	if cwd == "" {
		cwd = strings.TrimSpace(m.defaultCWD)
	}
	if task.Type != "http" {
		info, err := os.Stat(cwd)
		if cwd == "" || err != nil || !info.IsDir() {
			run.Output = fmt.Sprintf("Cron task workspace is unavailable (%s).", cwd)
			if task.Workdir != "" {
				m.report(m.store.disableError(task.ID, run.Output))
			}
			return
		}
	}
	execution, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	defer cancel()
	if task.Type == "http" {
		run.Success, run.Output = executeCronHTTP(execution, task)
		if errors.Is(execution.Err(), context.DeadlineExceeded) {
			run.Success = false
			run.State = "expired"
			run.TerminationReason = "timeout"
			run.Output = cronTerminationOutput(run.Output, "Cron run timed out.")
		}
		return
	}
	if task.Type == "bash" {
		var policy *sandbox.Policy
		if m.policy != nil {
			policy = m.policy(cwd)
		}
		run.Success, run.Output, run.ExitCode = executeCronBash(execution, task, cwd, policy)
	} else {
		if m.promptExecutor == nil {
			run.Output = "canonical prompt executor is unavailable"
			return
		}
		run.Success, run.Output = m.promptExecutor(execution, task, cwd, &run)
	}
	if errors.Is(execution.Err(), context.DeadlineExceeded) {
		run.Success = false
		run.State = "expired"
		run.TerminationReason = "timeout"
		run.Output = cronTerminationOutput(run.Output, "Cron run timed out.")
	} else if execution.Err() != nil {
		run.Success = false
		run.Output += "\n" + execution.Err().Error()
	}
}
func cronTerminationOutput(output, reason string) string {
	lines := []string{reason}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == reason || trimmed == context.Canceled.Error() || trimmed == context.DeadlineExceeded.Error() {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *cronManager) report(err error) {
	if err != nil {
		log.Printf("cron persistence: %v", err)
	}
}

func executeCronBash(ctx context.Context, task *CronTask, cwd string, policy *sandbox.Policy) (bool, string, *int) {
	result := bashrun.Run(ctx, bashrun.Options{
		Command: task.Script,
		Dir:     cwd,
		Timeout: time.Duration(task.TimeoutSeconds) * time.Second,
		Sandbox: policy,
	})
	code := 0
	if result.Exit != "" {
		code = 1
		var exit int
		if _, err := fmt.Sscanf(result.Exit, "(exit: exit status %d)", &exit); err == nil {
			code = exit
		}
	}
	text := fmt.Sprintf("shell=%s\nexit=%s\ntimed_out=%t\nstdout/stderr:\n%s", bashrun.DefaultShell(), result.Exit, result.TimedOut, result.Output)
	return code == 0 && !result.TimedOut, text, &code
}
func executeCronHTTP(ctx context.Context, task *CronTask) (bool, string) {
	client := &http.Client{Timeout: time.Duration(task.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var sections []string
	success := true
	for i, input := range task.Requests {
		if ctx.Err() != nil {
			return false, strings.Join(append(sections, ctx.Err().Error()), "\n\n")
		}
		body, err := cronBody(input)
		if err != nil {
			return false, err.Error()
		}
		if input.Method == "GET" || input.Method == "HEAD" || input.Method == "OPTIONS" {
			body = nil
		}
		req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(body))
		if err != nil {
			return false, err.Error()
		}
		for k, v := range input.Headers {
			req.Header.Set(k, v)
		}
		if body != nil && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			success = false
			sections = append(sections, fmt.Sprintf("Request %d failed: %v", i+1, err))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCronOutput))
		_ = resp.Body.Close()
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 400 {
			success = false
		}
		sections = append(sections, fmt.Sprintf("Request %d: status=%d\n%s", i+1, resp.StatusCode, string(data)))
	}
	return success, strings.Join(sections, "\n\n")
}
