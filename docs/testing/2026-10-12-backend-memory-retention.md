# Backend memory retention verification — 2026-10-12

## Scope

Changes on top of PR #31 (`9a17066`), preserving its standalone environment-proxy fix.
Measurements and process tests used isolated data directories on macOS arm64, without
changing the installed LiveAgent application or real user histories/configuration.
Model responses in automated tests came from local fixtures, not external providers.

## Changes

- Remove deleted sessions from the runtime registry and release their agent, recorder,
  event and run references. Recheck storage before inserting concurrently loaded sessions.
- Check idle runtimes every 30 seconds. Reclaim eligible instances after five minutes,
  or while the registry exceeds eight instances. Requests and run workers hold leases.
  Active runs, unfinished background tasks, pending interactions, running terminals and
  managed processes (including isolated ones) prevent eviction. Persistence-quarantined
  runtimes remain quarantined rather than being silently reloaded.
- Keep at most 512 events and approximately 4 MiB of payload/metadata per runtime.
  Evicted entries are zeroed. The disk journal remains authoritative; SSE replay reads
  batches from disk, while history editing, branching, questions and cron results can
  still access older events. JSON streaming supports single events larger than 4 MiB.
- Keep a 1 MiB command-output tail and spill complete oversized output to a private
  temporary log. Tool results include the log path. Changed-output notifications carry
  at most 16 KiB and stop before the command runner returns.

## Measurements

Go heap values below are after garbage collection, not process RSS or total app memory.
The baseline is the beta.8 backend; output/event fixtures are the same workload.

| Workload | Before | After |
| --- | --- | --- |
| Load then delete 24 sessions, each with 1 MiB user history | 24 runtimes / +24.5 MiB retained | 0 runtimes / approximately 0 MiB retained |
| 120 cumulative tool events, final output 476 KiB | 27.9 MiB event payload / +28.6 MiB heap | 3.6 MiB payload / +3.7 MiB heap |
| Real Bash command producing 384 KiB over time | approximately 5 MiB retained snapshots | 0.38 MiB retained snapshots |
| Ten cold reads of a 4 MiB trajectory journal | approximately 139.7 MiB total allocations | approximately 140.3 MiB total allocations; no retained runtime |

Repeated history imports and read-only trajectory requests did not instantiate runtimes.
The cold-read allocation pressure remains; this change does not claim to eliminate it.

## Regression coverage

- Idle eviction and history reload; run acceptance deduplication after eviction.
- Concurrent request acquisition/reaping and concurrent deletion/loading.
- Leased/active/quarantined runtimes and live PTY retention.
- Bounded SSE replay after runtime reload, including an oversized individual event.
- Editing and branching histories after the original events leave the memory cache.
- Command-output spill integrity, notification bounds and callback completion.
- Existing backend, command tools and proxy behavior, with the race detector enabled.

The following checks passed on the final patch:

```sh
go test -race ./internal/backend ./internal/tools/bashrun ./internal/tools ./internal/netproxy -count=1
go test -p 2 ./... -count=1
go vet ./...
go build -o /tmp/kb31-darwin-arm64 ./cmd/kn
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/kb31-windows-amd64.exe ./cmd/kn
```

A process-level HTTP test against the compiled macOS backend verified health, session
creation, a real Bash invocation through a local model fixture, SSE completion, the
complete 2,097,161-byte output file, readable history, deletion followed by HTTP 404,
and clean idle exit. Windows was cross-compiled, not executed on a Windows host.

## Limitations and separate follow-up

- This is a fix for reproduced retention paths, not proof of a bound on whole-process
  memory. Active model context, request-time full-history reads, run indexes and
  background-task records can still consume memory. Disk journal/log size is not capped.
- Completed terminal/process handles are runtime-local and may expire when their idle
  runtime is reclaimed; durable conversation history and tool results remain on disk.
- Earlier overlapping full-suite runs hit the existing five-second timeout in
  `TestRunningTaskViewReplaysThenStreams`; the isolated test and standalone TUI suite
  passed. A final full-suite run without overlapping test suites also passed.
- The previously reproduced macOS quit delay with an active SSE connection is separate.
  This patch does not change HTTP shutdown ordering or LiveAgent process ownership.
- Merging this change does not update installed binaries. No release tag is created here.
