# AI approval

K-brain owns the isolated reviewer, provider credentials, model requests, policy decisions and tool execution. LiveAgent only edits backend settings and displays review/permission events. This follows Codex guardian's isolated review and per-action decision design; it does not import Codex code.

## Configuration

```json
{
  "aiApproval": {
    "mode": "risk_only",
    "provider": "your-configured-provider",
    "model": "your-configured-small-model"
  }
}
```

The provider/model must already exist and be enabled. Choose the reviewer explicitly; K-brain does not silently select or download a small model. The default is `off`. GET/PUT `/v1/settings` exposes `aiApproval` without provider credentials. Changes apply on the next turn, including existing backend sessions.

| Mode | Behavior |
| --- | --- |
| `off` | No review calls; existing behavior unchanged. |
| `risk_only` | Review tool arguments/results and show risk; preserve existing permission decisions and tool output. |
| `auto` | Review each exact tool invocation. Approve once only when every risk dimension is low. Uncertain, medium/high-risk, invalid, failed or cancelled reviews never grant approval. Existing deny, chat/plan, workspace and sandbox enforcement remains authoritative. |

Review dimensions: sandbox operations, sensitive information, model behavior relative to the submitted user request, and tool-content instruction injection. Main-model prose alone is not continuously classified.

## Execution and fallback

- Agent tool execution, nested `tools.Execute`/`ExecuteResult` calls, backend runs, TUI turns and `kn run` use the same review controller. Tool implementation path checks and OS sandbox remain in place after review.
- Backend/TUI ask the user when auto review cannot approve. Noninteractive `kn run` rejects actions requiring a person; it cannot prompt. Unattended backend run restrictions remain unchanged.
- Auto mode withholds unreviewable tool text/images before returning them to the main model. Subsequent actions in the same turn require manual review after a risky result. Risk-only mode reports but does not withhold.
- Reviewer output is strict JSON. Unknown/duplicate fields, missing dimensions, invalid enum values, trailing JSON and malformed output fall back. The overall level is at least the highest dimension.
- Each call has a 15-second deadline, at most 12,000 bytes of serialized evidence, 512 output tokens, two simultaneous reviewer calls and 64 review attempts per run. Provider retries remain subject to the deadline. Review makes no tool calls and does not inherit the main conversation.
- Recognized sensitive values and known provider keys/header values are irreversibly redacted before transmission. Redaction forces manual approval; it is not proof that arbitrary secrets have been removed. Remote reviewers still receive redacted user intent, tool arguments and tool text. There is no persistent review-decision cache.
- `approval.reviewed` events contain phase, tool, structured report and whether a low-risk action was automatically approved. Permission requests include the report in `description`. Raw reviewer transport errors are not exposed.

## Boundaries

This is additional assistance, not a replacement for sandbox enforcement. It cannot guarantee detection of adversarial content. It reviews tool text, not image pixels. It does not revoke parallel actions already executing when a different tool returns suspicious content. The original submitted user prompt is supplied; later steering/history is not copied, which can cause conservative manual fallback. Trusted configured hooks and explicit user-operated terminal endpoints are not model tool calls and are outside this middleware.

LiveAgent desktop supports manual fallback. The legacy Gateway WebUI permission-response protocol has no confirmation RPC; its relay rejects pending permissions with an explanatory status instead of hanging. Both clients can configure all three modes. Completing WebUI manual approval controls requires a separate protocol/UI change.

## Verification (2026-10-11)

Passed race-enabled tests for `internal/approval`, `internal/privacy`, `internal/config`, `internal/backend`, `internal/tools`, `internal/tui`, and `cmd/kn`.

New HTTP integration tests use an actual local HTTP provider fixture and real backend runs, permissions and filesystem writes: off, warnings with ask/auto, low-risk auto, high-risk/manual, malformed response/manual, human allow, and hard-deny precedence. Unit tests cover strict output, irreversible redaction, cancellation, evidence/call budgets, output withholding, tainted turns, TUI plan precedence and manual fallback.

These deterministic tests validate integration, not the classification accuracy or billing of a real remote small model. No real user config or conversation data was modified.
