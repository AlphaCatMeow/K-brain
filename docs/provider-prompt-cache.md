# Provider prompt-cache compatibility

KB owns prompt-cache policy. LA sends conversation requests and displays provider-reported
usage; it does not insert cache markers, select affinity headers, or rewrite cached history.

## Provider configuration

Existing fields remain supported:

- `promptCachingEnabled: false` disables automatic cache hints and affinity headers.
  It cannot force a provider to disable its own automatic prefix cache.
- `promptCacheRetention`: `none`, `short`, or `long`.
- `cacheControlFormat: "anthropic"` explicitly enables Anthropic-style cache markers
  in Chat Completions. Leave unset for ordinary OpenAI-compatible endpoints.
- `cacheSessionAffinity: true` auto-selects headers for known endpoints only.

New optional `cacheCapabilities` fields:

| Field | Meaning |
| --- | --- |
| `supportsPromptCacheKey` | Whether to send the `prompt_cache_key` body field. |
| `supportsLongRetention` | Whether long retention is accepted by this endpoint/model deployment. |
| `sessionAffinityFormat` | `none`, `openai`, `openai-nosession`, or `openrouter`. Empty uses defaults. |
| `responsesCacheOptions` | Explicit opt-in for Responses `prompt_cache_options`, instead of the legacy retention field. |

Booleans omitted from the first two fields retain endpoint defaults. Explicit false overrides
defaults. Capability declarations describe the configured endpoint/model deployment, not just
the API shape; use separate provider entries if models on the same URL support different options.
Do not enable `responsesCacheOptions` unless the endpoint accepts that contract.

Example for a proxy that accepts cache keys and legacy long Responses retention:

```json
{
  "promptCacheRetention": "long",
  "cacheCapabilities": {
    "supportsPromptCacheKey": true,
    "supportsLongRetention": true,
    "sessionAffinityFormat": "openai-nosession"
  }
}
```

Example for automatic prefix caching on a strict DeepSeek-compatible proxy:

```json
{
  "promptCacheRetention": "short",
  "cacheCapabilities": {
    "supportsPromptCacheKey": false,
    "supportsLongRetention": false,
    "sessionAffinityFormat": "none"
  }
}
```

These fields are accepted by backend provider settings updates and survive unrelated provider
edits. `cacheCapabilities: {}` resets capability overrides; `cacheControlFormat: ""` removes the
Chat Completions marker convention. Unknown affinity formats are rejected before persistence.
This change does not add LA settings controls for these advanced fields.

## Defaults and wire behavior

- Endpoint inference parses the hostname. A path or lookalike domain containing `api.openai.com`
  does not enable long retention.
- Official OpenAI and Anthropic endpoints default to long-retention support when `long` is requested.
  Unknown proxies require an explicit long-retention override.
- Official DeepSeek/xAI endpoints and configured DeepSeek/xAI provider types omit prompt-cache keys
  by default. Custom proxies can override this without relying on model-name substring detection.
- Unknown OpenAI-compatible proxies retain the existing default of sending session cache keys;
  strict proxies should explicitly disable that field.
- `openai` affinity sends `session_id`, `x-client-request-id`, and `x-session-affinity`;
  `openai-nosession` omits `session_id`; `openrouter` sends only `x-session-id`.
  Explicit caller headers are preserved. Unknown proxy URLs get no automatically inferred headers.
- Request-level cache keys also drive generated affinity headers. Long keys use a stable SHA-256
  representation, avoiding collisions caused by truncating shared prefixes. Short keys remain unchanged.
- Anthropic long retention emits `cache_control: {"type":"ephemeral","ttl":"1h"}` only when supported.
  Cache boundaries cover tools, system content and the latest eligible history block, skipping thinking.
- Opt-in Chat Completions markers use the same three boundaries. All nonempty text messages keep
  block representation as the history grows, so moving the tail marker does not change old message shapes.
- Opt-in Responses cache options use `ttl: "30m"` for supported long retention and `mode: "explicit"`
  for the disabled policy. Other Responses endpoints keep the legacy retention convention.
- Unsupported fields are not retried with a different request body after a provider error. Configure
  capabilities explicitly rather than silently changing a potentially billable request.

## Diagnostics and acceptance

`ai.WithCacheObserver` is an opt-in Go context observer at the final HTTP serialization boundary.
It reports parameter and message hashes, excluding credentials and plaintext prompts.
`ai.SharedCacheMessages` compares whole matching messages, conservatively invalidating the comparison
when parameters differ. The callback may run for each retry and must be concurrency-safe if shared.
It is a diagnostic hook, not a new HTTP endpoint or an automatically persisted log.

Hash matches are not provider cache-hit tokens. Measure actual usage after a cold request, follow-up,
tool result, memory update, restart and compaction. Preserve distinctions between cache reads and writes;
Anthropic total input includes uncached input plus cache reads and cache creation. Never present prefix
hash overlap as a measured cache-hit percentage.

References inspected locally: Pi provider compatibility/retention and Chat Completions markers;
ZCode provider-request history/compaction boundaries; grok-build eligible Anthropic block selection and
usage normalization; DSH source-owned request reconstruction and prefix-reusing compaction.
Existing KB snapshot/compaction tests remain part of regression coverage. No live provider hit-rate
claim is made by fixture tests, and no 97% hit-rate guarantee is implied.
