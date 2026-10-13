# Offline validation receipt — 2026-10-11

Scope: `internal/imaging/adapters/async`, package `asyncadapter`.

Final command: `go test ./internal/imaging/adapters/async -count=1 -timeout 45s`

Result: **PASS**, `ok github.com/Stack-Cairn/K-brain/internal/imaging/adapters/async 0.210s`.

16 top-level contract tests, including core settings compatibility. Earlier verbose run (15 tests before the settings compatibility test was added) passed in 0.213s. `go vet ./internal/imaging/adapters/async` emitted no diagnostics. No broader backend test suite was run by this owner.

Before every Go test invocation, the process environment set:

- LIVEAGENT_HOME unset
- K_BRAIN_HOME unset
- HOME and USERPROFILE `D:\Documents\Projects\Web\LiveAgent\.liveagent\image-studio-test-home`
- K_BRAIN_SHELL `C:\Program Files\Git\bin\bash.exe`
- GOPROXY `off`

First test run exposed an offline timeout-fixture teardown problem: the fake server waited for request cancellation without consuming its body. That test run hit its 45-second suite timeout. The fixtures now drain request bodies before waiting, and all subsequent runs pass. No adapter replay or production request occurred.

Signature fixture was independently computed with Python hashlib/hmac and fake fixed inputs, then verified by Go. It is a self-built algorithm vector, not an official CV vector.

No real service request, live image generation, actual credential read, personal configuration access, or old-log access. All provider endpoints were served by loopback httptest fixtures. Production targets remain **not enabled**, liveVerified=false.
