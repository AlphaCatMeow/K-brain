# Shared planning backend

K-brain owns calendars, task lists, tasks, events, recurrence, trash, reminders and subscription refresh. LiveAgent desktop and Gateway use the same authenticated endpoint and persistent data. The desktop only delivers operating-system notifications and reads legacy SQLite records for migration.

## Persistence and protocol

- Storage: `<sessionsDir>/planning.json`, mode `0600`, atomic replacement after file sync. Run one backend process per session directory; the store mutex serializes requests within that process, not across processes.
- Endpoint: `POST /v1/planning`, normal backend Bearer authentication. Body: `{"action":"query","input":{}}`.
- Actions: `query`, `export`, `mutate`, `import`, `migrate`, `timezone`, `reminders.claim`, `reminders.finish`, `subscription.create/update/refresh/delete`, `cron.occurrences`.
- `mutate` input contains `requestId`, `action`, `data`, and (for existing records) `id` and `expectedRevision`. Revision conflicts return `status: "conflict"`; reread and let the user reconcile their change. Never blindly retry with a newer revision.
- Stable request IDs replay the original successful mutation for 30 days. Reusing an ID with different data is rejected. Full snapshot import is a replacement operation, not a revision-checked merge.
- Range queries expand events for at most 366 days and include `eventMasters`. Cron queries allow 93 days and use the scheduler's system timezone, independently of calendar display preferences.
- The backend agent runtime registers `PlanningQuery` and `PlanningMutate`; the mutation tool passes through the normal permission gate. This does not add these tools to the standalone terminal agent runtime.

## Legacy migration

LiveAgent reads legacy `planning_*` SQLite tables in one transaction and assigns a durable migration-source ID. The first import preserves record IDs. Repeated import from the same source is a no-op, so desktop restarts cannot overwrite later backend edits. Original SQLite rows remain intact. Conflicting records or duplicate names in a nonempty destination reject the whole import; bootstrap reports the error instead of dropping records. The request limit is 4 MiB, including migration data.

## Reminders and subscriptions

Reminder claims lease a notification for 60 seconds. Completion must match both lease and revision. Successful claims retain notification history; recurring reminders use occurrence-specific IDs compatible with legacy records. Disabling reminders, clearing due dates, completing linked tasks, and soft deletion suppress pending notifications. Delivery retries are possible after a client crashes between OS delivery and acknowledgment; this is not an exactly-once delivery guarantee.

Subscriptions refresh every 30 seconds when due, even without a desktop attached. Private feed URLs are retained only in backend storage/migration input and excluded from public snapshots. Fetches have a 30-second timeout and 10 MiB limit. Recurrence expansion is bounded to the previous 30 days and following year, at most 10,000 imported occurrences. RRULE, EXDATE and recurrence overrides are supported. RDATE, DURATION and custom VTIMEZONE definitions are not fully implemented; validate such feeds before relying on their rendering.

## Verification (2026-10-06)

- `go test -p 1 ./...`: passed. Parallel runs twice hit the existing TUI streaming timing test; the test passed in isolation on both the baseline and changed tree.
- `go test -race ./internal/planning ./internal/backend`: passed.
- Tests cover authentication, tool authorization denial/allow, persistence, CAS/idempotency, failed-write rollback, hierarchy, recurrence split/exception, DST, malformed fields, migration, notification leases and feed refresh without desktop.
- Real macOS desktop and browser tests are recorded in LiveAgent's `docs/testing/v2-kbrain-main-feature-test-report.md`.
