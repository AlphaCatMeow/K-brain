# Conversation loops

`POST /v1/sessions/{id}/loops` accepts `interval` and `prompt`. Intervals are
positive integers followed by `s`, `m`, `h` or `d`, up to seven days. Values below
60 seconds are normalized to one minute. For example, `90m` means 5,400 elapsed
seconds, not a calendar cron step. Existing cron-only clients remain supported.

The first occurrence is due immediately. Later deadlines are persisted when a
worker is admitted. Restart preserves the deadline; missed occurrences coalesce
into one run. Workers never overlap. A loop expires seven days after creation;
at most 50 enabled, unexpired loops may exist across this backend.

Each interval occurrence uses a hidden canonical session with fresh context,
the selected model, configured tools and the loop's workspace. Parent chat
history is not inherited, so prompts must be self-contained. Only the result
is appended to the parent when it is idle. Stable message IDs prevent duplicate
delivery after a crash. A full 200-result delivery backlog suspends new automatic
occurrences until the parent can receive the pending results. Run output is
bounded by the scheduled-task log limits; parent messages show up to 4,000 runes.

Listing returns normalized `interval`, `nextFireAt` and `expiresAt` (Unix
milliseconds). The cron field is retained for transport compatibility and is not
the interval scheduler's source of truth. Changing the schedule requires
cancelling and recreating the loop. Cancellation removes the task and cancels
its active worker without cancelling any parent run. Deleting the parent also
removes its loops. K-brain must remain running for execution.

The composer requires an explicit interval. Natural-language frequency parsing
and continuing a single worker's model context across occurrences are not
implemented.
