# Local model gateway and cache acceptance — 2026-10-08

## Architecture and scope

The shared canonical message representation remains the storage and frontend contract.
KB translates it into Chat Completions, Responses, Anthropic Messages or Gemini requests.
LA continues using persistent session/run endpoints for chat and backend-owned tool execution.
The new authenticated `/v1/generate` endpoint provides stateless JSON/SSE inference; it
returns tool calls without executing them. The CLI/TUI shares the same adapter layer in-process.

No real user configuration or old conversation was edited during these tests. Real provider
requests used isolated temporary configuration copies, a private workspace and no tools.

## Automated coverage

- One canonical history traverses all four protocols, including completed and interrupted
  tool calls, display-only reasoning and subsequent model switches.
- The same history traverses all four protocols through the persistent session/run endpoints
  used by LA, not only the stateless endpoint.
- JSON and SSE responses, normalized cache usage, authentication, enabled model selection,
  input validation, cancellation, malformed upstream tool arguments and error redaction.
- Provider-private replay remains scoped to API, endpoint and model; source history is unchanged.
- Late/duplicate/orphan tool results are carried as historical context, with missing results
  repaired and missing tool names restored.
- Full Go tests and focused race tests cover the AI adapters, backend, agent and protocol.
- Windows amd64 and Linux amd64 cross-compilation checks; these are not native OS runtime tests.

Four-protocol tests use local protocol fixtures. They do not demonstrate acceptance by every
upstream model or gateway.

## Real cache measurements

CLI `run`, not interactive TUI; `gpt-6-luna` through the user's configured Responses provider.
The synthetic stable system prefix was about 6,000 tokens. Turns two and three resumed the
same stored session in separate CLI processes. Each reply matched its requested test code.

| Turn | Input tokens | Cached input tokens | Output tokens | Cache hit rate | Elapsed |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 6,473 | 0 | 8 | 0% | 5.04 s |
| 2, process restart | 6,500 | 5,632 | 8 | 86.65% | 16.88 s |
| 3, process restart | 6,527 | 5,632 | 8 | 86.29% | 3.18 s |

Rates are cached input tokens divided by total input tokens, from provider-reported usage
persisted in the session. They are not inferred from latency or local prefix diagnostics.
This test does not establish a 97% hit rate or a latency improvement.

The Anthropic trial first returned 404 using the configured endpoint. Appending `/v1` in
the temporary copy reached a 429 credential/model cooldown response. No successful
Anthropic cache measurement was obtained; the user's original configuration was unchanged.

## Real local HTTP gateway model switches

A real KB backend listened on a random loopback port with Bearer authentication. Requests
used `/v1/generate`, canonical messages and a stable cache key, without session persistence.

| Selected model | Reply | Input / output tokens | Elapsed |
| --- | --- | ---: | ---: |
| `gpt-6-luna` | `ORCHID-42` | 339 / 9 | 4.10 s |
| `gpt-6-sol` | `ORCHID-42` | 368 / 9 | 3.45 s |
| `gpt-6-luna` | `ORCHID-42` | 397 / 9 | 5.92 s |

The second and third requests asked for the code from the earlier conversation. The actual
switch and switch-back preserved that context. Both models use Responses; this is not a
live cross-provider or four-protocol acceptance test. The short prompts reported no cache hits.

An earlier attempt to switch to `gpt-5.4` returned local HTTP 502. A separate CLI request
to that model returned upstream HTTP 400 with `model_not_found`, despite its presence
in the local catalog. It is excluded from the successful switch result.

## Remaining verification and compatibility limits

- Real Anthropic/Gemini/Chat Completions cross-protocol switching and cache measurements
  require working upstream routes; only Responses switching was measured live here.
- No new desktop UI or interactive TUI acceptance was performed for this backend change.
- Stateless signed/encrypted reasoning replay is not exposed. Persistent sessions retain
  provider replay, but foreign model signatures are not synthesized.
- Tool name/ID restrictions, attachment capabilities and model availability remain provider
  constraints; a common message schema cannot guarantee every model accepts every history.
- Native macOS notification banners, real calendar-account subscriptions, native dragging,
  multi-client timezone synchronization, Cua screen-recording permission and self-window
  exclusion are not established by these tests.

## Release boundary

KB `v0.107.6-beta.2` and LA `v2.0.0-beta.5` were already published before these cache/gateway
changes. LA beta.5 pins KB beta.2. These tests do not make the new main-branch implementation
part of those immutable release assets; a new KB release and LA binary pin are required.
