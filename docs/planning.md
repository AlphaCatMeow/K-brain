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

Subscriptions refresh every 30 seconds when due, even without a desktop attached. Private feed URLs are retained only in backend storage/migration input and excluded from public snapshots. Fetches have a 30-second timeout and 10 MiB limit. Recurrence expansion is bounded to the previous 30 days and following year, at most 10,000 imported occurrences. DTSTART, RRULE and RDATE form a deduplicated recurrence set, with EXDATE and recurrence overrides applied. RDATE PERIOD supports explicit end dates or durations. DURATION supports calendar days/weeks and elapsed hours/minutes/seconds across DST. Embedded VTIMEZONE STANDARD/DAYLIGHT definitions support DTSTART, RRULE and RDATE transitions, including historical final transitions; imported custom-zone timed events retain absolute instants with UTC metadata. Failed parsing or validation preserves the last good events and exposes a subscription error.

The parser bounds event-rule iteration to 100,000 and timezone-rule iteration to 10,000. Very dense or extremely old recurrence sets can exceed these limits. Zero/negative duration events, mismatched date value types, METHOD:CANCEL calendars and malformed definitions are rejected. This is a bounded calendar feed importer, not a complete scheduling-message processor; RANGE=THISANDFUTURE overrides are not implemented.

## Verification (2026-10-06)

### Follow-up acceptance contract

- Subscription recurrence is the union of DTSTART, RRULE and RDATE, minus EXDATE and overridden original occurrences. Duplicate dates import once. RDATE PERIOD may override an occurrence duration; malformed dates/durations fail refresh without replacing previously imported events.
- VEVENT DURATION supports weeks/days/time; day/week components are calendar arithmetic across DST, while hours/minutes/seconds are elapsed time. DTEND and DURATION together are invalid. All-day duration must contain only whole days/weeks. A malformed DTEND must not silently become a one-hour event.
- Embedded VTIMEZONE definitions are resolved before host IANA names. STANDARD/DAYLIGHT transitions from DTSTART/RRULE/RDATE are converted to a private Go location; timed imported occurrences are stored as UTC instants. Custom transition expansion is bounded, and unsupported or invalid definitions fail explicitly rather than silently using UTC.
- macOS acceptance uses the real WKWebView and local backend, including an HTTP calendar fixture, refresh, editing, restart, and reminder lease delivery. External calendar services and OS notification visibility must be reported separately from local protocol success.

- `go test -p 1 ./...`: passed. Parallel runs twice hit the existing TUI streaming timing test; the test passed in isolation on both the baseline and changed tree.
- `go test -race ./internal/planning ./internal/backend`: passed.
- Tests cover authentication, tool authorization denial/allow, persistence, CAS/idempotency, failed-write rollback, hierarchy, recurrence split/exception, DST, malformed fields, migration, notification leases and feed refresh without desktop.
- Real macOS desktop and browser tests are recorded in LiveAgent's `docs/testing/v2-kbrain-main-feature-test-report.md`.

### Subscription follow-up (2026-10-06)

- `go test -race ./internal/planning ./internal/backend`, `go test -p 1 ./...`, and `go vet ./internal/planning ./internal/backend`: passed.
- Added regression coverage for recurrence union/deduplication, PERIOD, DST durations, exclusive all-day ends, cancelled overrides, historical custom-zone transitions, overlapping recurrence windows, invalid value types and atomic failed-refresh preservation.
- Built `./cmd/kn` for the local Apple Silicon Mac, replaced the dedicated test desktop's sidecar, and verified reconnect with a changed backend port.
- Real WKWebView subscription create/refresh/delete used a local HTTP ICS fixture. Verified custom-zone instants, two-hour durations, read-only enforcement, invalid-feed preservation, retry recovery, stable IDs, occurrence removal, and desktop/mobile rendering. This does not certify Google/iCloud/Exchange service interoperability or visible macOS notification delivery.
- Detailed frontend acceptance and remaining work: LiveAgent `docs/testing/2026-10-06-planning-followup-macos.md`.

### Shared timezone preference and provider diagnostics (2026-10-06)

- `timezone.get` returns `{preference,timeZone,systemTimeZone,revision}`. `preference:""` follows the backend system zone, not the browser's zone. Existing stores initially expose their saved timezone as an explicit preference. Automatic mode is re-resolved on backend restart.
- `timezone` accepts `{preference,expectedRevision}`; stale revisions return `E:conflict`. Successful writes persist the preference and revision atomically, reconcile reminders and advance the planning sequence. Legacy `{timeZone}` requests remain accepted without a revision check.
- LiveAgent desktop, browser and Gateway settings selectors read this same endpoint and poll for remote changes. Unrelated native system-settings saves no longer overwrite shared planning timezones.
- Subscription errors distinguish authentication, missing/revoked feeds, rate limits and invalid ICS. See [provider acceptance](planning-provider-acceptance.md) for the actual external-service test boundary.
