# Subagent control follow-up — 2026-10-13

Reference: local grok-build commit `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`.
Reviewed its subagent user guide and `send_subagent_message.rs`, including explicit
ownership, bounded message admission, completion/resumption identity, and the
distinction between cancellation requested and completion observed.

## Implemented in KB

- Parent-only status, cancel and follow-up tools, retaining existing spawn/steer names.
- Follow-up reuses retained child state and ID; propagates request-task identity and
  the child's sandbox policy. Existing follow-up cancellation and duplicate-admission
  protections remain in force.
- Empty/oversized messages rejected, steering queue bounded and registry admission
  serialized against terminal settlement. This is queue acceptance, not a claim
  that the model already processed the message.
- Finalization cannot be cleared or followed up before callbacks/persistence complete;
  callbacks receive a task snapshot rather than mutable registry state.
- Automatic parent report injection is capped; stored transcript remains available.
- Status output is bounded and deterministic; restored summaries are not resumable.

## Deliberately not implemented in this change

Grok's personas/custom role discovery, nested delegation, sibling/parent messaging,
queue/interject delivery modes, global spawn concurrency limits and durable child
resumption across process restart. KB currently omits orchestration tools from child
inheritance, so copying those features without ownership/configuration design would
change existing behavior. No frontend execution logic or new LA controls were added.

## Verification

Tests cover cross-parent ID rejection, running-task cancellation, follow-up context
and identity, concurrent steering admission/limits, terminal finalization races,
restored-state rejection, inherited-tool exclusion and cancelled caller rejection.
A canonical backend HTTP run calls the follow-up tool via a local model fixture;
its tool result, child-attributed trajectory and persisted task ID are checked.
Existing agent/backend tests include MCP inheritance, worktree isolation, follow-up
cancellation and transcript persistence. No paid provider or native LA UI was used.

Validation commands:

```sh
go test -p 2 ./...
go vet ./...
go test -race ./internal/agent -count=1
go test -race ./internal/backend -run 'TestCanonicalRunCanFollowupOwnedSubagent|TestSubagent' -count=1
go test -race ./internal/tui -run 'Test.*(Task|Subagent)' -count=1
```

The full agent/backend race suite also passed before the final small cancellation
admission guard. An earlier overlapping run hit the existing five-second timeout
in `TestRunningTaskViewReplaysThenStreams`; the task-focused TUI race rerun and
subsequent full non-race TUI suite passed. Initial new-test fixture errors were
corrected before verification; the finalization guard preserves existing synthetic
registry fixtures while preventing removal during actual settlement callbacks.
