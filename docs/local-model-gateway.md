# Unified local model gateway

KB is the aggregation boundary. Clients supply a configured provider/model selection and canonical
messages; only KB resolves credentials and serializes upstream requests. Chat Completions, Responses,
Anthropic Messages and Gemini adapters expose the same internal `ai.Client` contract.

## Stateful agent conversations

Continue using `/v1/sessions`, `/v1/sessions/{id}/runs` and the session SSE event stream for LA chat.
These endpoints own conversation persistence, tool execution, permissions and replay. Model changes
reuse the canonical session rather than converting stored messages into a vendor-specific format.
The TUI uses the same adapters in-process; it is not changed to require a separate HTTP server.

All four adapters now share `prepareRequestHistory` before serialization. It:

- works on a copy, leaving saved history untouched;
- supplies explicit interrupted results for unfinished calls;
- carries missing tool names into their matching results;
- projects late, duplicate and orphan results as historical context rather than invalid tool responses;
- keeps provider-private replay only when API, endpoint and model all match.

Gemini now uses the same history repair as the other three protocols. Display-only reasoning is not
used as signed provider reasoning. Changing models may reduce prefix-cache reuse and does not make
an unsupported attachment or capability supported.

## Stateless model inference

`POST /v1/generate` is a single inference endpoint, not another agent loop. It uses the same local
Bearer authentication as the rest of the backend and accepts only configured, enabled model routes.
No upstream URL or API key is accepted in the request.

```json
{
  "model": {"provider": "configured-provider-id", "model": "configured-model-id"},
  "messages": [
    {"role": "system", "content": [{"type": "text", "text": "Be concise."}]},
    {"role": "user", "content": [{"type": "text", "text": "Hello"}]}
  ],
  "max_output_tokens": 256,
  "reasoning": "low",
  "cache_key": "stable-conversation-key",
  "stream": false
}
```

Optional `tools` use `{name, description, parameters}`. Assistant history uses top-level
`tool_calls: [{id, name, arguments}]`; tool results use `role: "tool"` and `tool_call_id`.
Text, thinking, image and file content use the same `protocol.Message` representation as sessions.

The response contains `version`, selected `model`, canonical assistant `message` and normalized
`usage` (`input_tokens`, `output_tokens`, `cached_tokens`, `cache_write_tokens`). Tool calls are returned
as data and **never executed by this endpoint**. LA must continue using session runs for tool execution.

For `stream: true`, SSE data frames carry these types:

- `request.accepted`
- `assistant.text.delta`
- `assistant.thinking.delta`
- `request.completed` with the final canonical response, including complete tool calls
- `request.failed` with a redacted error

Upstream streaming is used even when the local response is buffered, so tool calls and normalized
finish reasons are preserved. Client disconnection cancels the request. The endpoint has a ten-minute
deadline and does not persist conversations or automatically replay uncertain requests.

## Limits

- This is a canonical local API, not four interchangeable public vendor-compatible API facades.
- `/v1/text/generate` remains available for existing text-only callers.
- Stateless responses do not expose encrypted/signed provider replay. Use session runs when native
  signed reasoning continuity is required; universal stateless replay of signed reasoning is not promised.
- Provider attachment restrictions still apply. Unsupported attachments are rejected, never silently dropped.
- Tool name/ID restrictions and signed tool-history requirements still vary by provider; a shared
  schema does not establish compatibility with every model. See the acceptance report for measured scope.
- No model-list discovery or credential change is performed automatically by this endpoint.
- Tests cover a single canonical history passed through all four wire formats, JSON/SSE responses,
  normalized cache usage, authentication, validation, private-error redaction, and tools returned as data.

Real measurements and outstanding acceptance work: [2026-10-08 acceptance report](testing/2026-10-08-model-gateway-cache.md).
