# Parent-owned backend exit verification

2026-10-12, macOS arm64, isolated test data and local model fixtures.

The backend CLI now closes HTTP connections/listener before joining backend
shutdown. Previously HTTP Shutdown waited up to ten seconds for SSE handlers,
while those handlers waited for runs cancelled only by the deferred Server.Close.
Server shutdown rejects requests and run/compaction admission, cancels all known
runs, then stops schedulers and waits for terminal persistence and host cleanup.

Validated:
- Full Go suite: `go test -p 2 ./...`.
- Backend race suite: `go test -race ./internal/backend -count=1`.
- Static checks: `go vet ./...`.
- CLI executable tests: active SSE plus parent stdin EOF exits within two seconds;
  normal parent lifecycle; standalone mode ignores EOF; persistence across restart.
- Actual Bash sleep child, process-level fixtures: idle EOF 0.002s; active EOF
  0.022s; active SSE plus EOF 0.023s; active SSE plus LA-style EOF/TERM 0.020s.
  All exited successfully, without forced kill or a surviving tool process.
- Companion modified LA native build: application menu Quit 0.164s, Cmd+Q
  0.196s, close-window exit 0.164s; LA, owned KB and the Bash process all gone.

LA separately performs owned-child cleanup on ExitRequested and Exit, prevents
startup after final exit, and retains a two-second forced termination fallback
for unresponsive backends. Normal active-task cleanup does not consume that grace.
Windows was not tested on a Windows host. No installed app, user configuration,
release tag or backend release lock was changed by this verification.
