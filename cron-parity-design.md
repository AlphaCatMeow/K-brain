# LiveAgent Cron Parity Design

## Authority

When the LiveAgent GUI uses the configured K-brain backend, K-brain owns the canonical cron task list, revision, run records, scheduling, and execution. The GUI adapter only reads snapshots, applies revision-checked operations, requests manual runs, and displays history. It does not claim or execute prompt runs.

Native Tauri automation remains the owner for native mode. The `/v1/hooks` service remains separate and is not part of cron execution.

## Persistence and revisions

K-brain stores cron state in `automation-cron.json` below the session store directory. Writes are protected by the store mutex and use a temporary file plus rename. Every successful task mutation increments the cron revision. `PUT /v1/cron` applies a batch only when `baseRevision` equals the stored revision and returns a conflict snapshot otherwise. Deleting a task also deletes its run records, matching the original automation store.

Run records are persisted before execution as `leased`. Completed and cancelled runs are retained for at most 200 records per task and 30 days. Leases found during startup are converted to expired records so restart cannot silently lose an execution.

## Scheduler and execution

The backend scheduler matches the original six-field expression (`second minute hour day month weekday`) and reloads task state from the store at each fire. Scheduled runs count against `remainingExecutions`; `run-now` is manual and does not consume the count, while manual runs may execute disabled or exhausted tasks. Bash execution uses K-brain's `bashrun` and sandbox policy, HTTP execution uses the backend HTTP client, and prompt execution uses the supplied K-brain `Factory` and `Agent.TurnAuthored`. No pi process, browser provider, or frontend prompt executor participates in K-brain mode.

A task with an explicit missing/non-directory workdir records a failed run and is disabled. An unpinned task uses the backend default working directory. Cancellation cancels the active backend context and records an expired run.

## Shared-file coordination

`internal/backend/server.go` has the cron manager field, construction, shutdown, `/v1/cron` dispatch, and two `attachTool` calls at agent construction/model refresh added for this work. The file also contains concurrent server and hook changes owned by other work; those changes must remain intact. `internal/agent/agent.go` is consumed through the existing `Factory` contract and is not changed by cron work.

## Compatibility routes

The prompt claim/release/complete routes remain as inert compatibility endpoints for clients that probe the old API. The K-brain frontend bypasses them, so there is a single prompt executor. Native hook routes continue through the hook adapter.

## Remaining parity limits

- The six-field matcher supports lists, ranges, steps, month/weekday names, `?`, Sunday 0/7 and day/weekday OR matching. Croner-specific `L`, `W`, and `#` expressions are currently rejected.
- Unpinned tasks resolve to the backend startup working directory. Following subsequent GUI workspace switches needs an explicit active-workspace transport.
- Existing native Rust cron definitions are not automatically imported into the backend store. Migration and disabling the native scheduler need a coordinated native change to avoid duplicate execution of previously configured tasks.
- JSON persistence assumes one backend process owns a session store. It has process-local serialization and atomic file replacement; cross-process locking remains future work.
- Prompt execution uses the backend agent factory and rejects interactive tool approval. It does not create a regular chat transcript or receive the full session-level MCP/hook/memory setup.
- Cancellation is available through the HTTP API and `cancelKBrainCron`; the original settings UI has no cancellation button.

