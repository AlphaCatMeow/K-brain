# Provider-backed image targets

`GET /v1/image/targets` returns the existing `{ "items": ImageTarget[] }` envelope (plus the standard image API version). It merges legacy standalone image targets with a read-only projection of active ordinary provider/model configuration. Listing never sends an image generation request or issues live verification evidence.

A derived entry includes:

```json
{
  "id": "it_provider_<stable hash>",
  "source": "provider",
  "providerId": "images",
  "modelId": "gpt-image-1",
  "connectionId": "ic_provider_<stable hash>",
  "adapter": "openai-images",
  "adapterVersion": "1",
  "kind": "model",
  "remoteId": "gpt-image-1",
  "revision": 123,
  "enabled": true,
  "verification": { "state": "provider_configured" }
}
```

The actual entry also carries `name`, `capabilities` and `parameters`. Jobs use the returned `id` as `targetId`; no connection or target configuration write is needed. IDs remain stable across configuration changes and restarts. The execution revision changes with the provider/model binding. Each list/resolve reads current settings; refresh the list after provider/model settings changes. Disabled/inactive models disappear; image models with missing credentials, unsupported protocols, unsafe model IDs or unusable endpoints are returned disabled with a machine-readable `disabledReason`.

`provider_configured` means configuration is available for execution, not that credentials have been tested or generation succeeded. It carries no fabricated verification job or timestamp. Derived entries cannot be written to the legacy image settings endpoint and are not persisted in the standalone `image` configuration. Legacy targets no longer need verification evidence to be enabled; adapters whose contract is unfinished (`jimeng-image`, `runninghub-workflow`, `google-interactions-image`) still cannot be enabled. On startup the backend removes `provider_ref` image connections that no target uses.

## Source configuration

The ordinary config file's `providers.<id>.models` list is the source. Existing model names/metadata and per-provider metadata overrides are respected. `vision`/`inputModalities` describe input and never establish image generation support. Model `outputModalities` containing `image` can identify image output; an explicitly non-image output list suppresses name-based discovery. Model `imageAdapter` can select `openai-images` or `google-generate-content-image`, `enabled` opts in with the protocol chosen from the provider API, `disabled` opts out, and an empty value restores automatic detection. Settings reads and model discovery also return a read-only `imageModel` with the effective result; updates may send it back and it is ignored. This explicit option has highest precedence and supports private model aliases. The same fields are available on settings model read/update/discovery responses; omitting them from updates preserves current metadata.

```json
{
  "defaultModel": "chat",
  "defaultProvider": "openai",
  "providers": {
    "openai": {
      "api": "openai-completions",
      "baseUrl": "http://127.0.0.1:19091/v1",
      "apiKey": "synthetic-fixture-key",
      "models": [
        { "id": "chat", "inputModalities": ["text", "image"] },
        { "id": "gpt-image-1", "outputModalities": ["image"] },
        { "id": "private-painter", "imageAdapter": "openai-images" }
      ]
    },
    "google": {
      "api": "google-generative-ai",
      "baseUrl": "http://127.0.0.1:19092/v1beta",
      "apiKey": "synthetic-fixture-key",
      "models": [
        { "id": "gemini-2.5-flash-image", "outputModalities": ["text", "image"] },
        { "id": "gemini-3-pro-image-preview", "outputModalities": ["text", "image"] }
      ]
    }
  }
}
```

These loopback URLs are placeholders for local fake servers, not functioning services. No real credential is needed for discovery. Without a local fixture, listing still works and submitting fails locally. Omit `activeModels` to expose all configured models, or set it to the intended active IDs. An empty array disables all models for that provider.

## Protocol mapping and limits

| Adapter | Request | Authentication | Advertised operations |
| --- | --- | --- | --- |
| `openai-images` | JSON `POST <base>/images/generations`; multipart `POST <base>/images/edits` with ordered `image[]` files and optional PNG `mask` | `Authorization: Bearer <provider key>` | Generate, reference, edit and mask; up to 16 inputs including mask, one output per request; implemented size/quality/output format/background/compression schema |
| `google-generate-content-image` | `POST <base>/models/<model>:generateContent`, contents/inlineData and TEXT+IMAGE response modalities | `x-goog-api-key: <provider key>` | Generate, reference, semantic edit; up to 14 ordered inputs, one request count; `aspectRatio` and model-specific `imageSize`; no mask |

Google parameter keys are `aspectRatio` and `imageSize`; requests map them to `generationConfig.imageConfig` using the same camelCase names. Common aspect ratios are `1:1`, `2:3`, `3:2`, `3:4`, `4:3`, `4:5`, `5:4`, `9:16`, `16:9`, `21:9`. Gemini 2.5 exposes aspect ratio only. Gemini 3 Pro Image and Nano Banana 2.1 expose `1K`, `2K`, `4K`; Gemini 3.1 Flash Image additionally exposes `512`. Unknown explicit Google aliases expose aspect ratio only because their resolution capability is not known. Invalid or model-incompatible values fail before HTTP. Schemas come from each target's `parameters`, so the generic frontend parameter form needs no protocol-specific wiring.

Both reuse provider secret resolution, proxy preference and the adapter's existing restricted custom-header contract. No credential, endpoint, header value or resolved execution connection is exposed by the target response or durable job snapshot. Queued/recovered executions and credential use are checked against current configuration; a changed/removed binding fails before sending rather than silently changing a queued job's target.

Automatic detection treats a model as an image model when its ID contains `image` (case-insensitive), its output metadata includes `image`, or it is a known GPT Image or Gemini image model; users can override the guess per model. Imagen, Google Interactions and unrelated provider protocols do not gain execution support, and image input support never counts. A Google image model on an OpenAI-chat provider needs an explicit image adapter choice; the backend does not assume that a proxy implements native Google endpoints. Two OpenAI-compatible vendors get a dialect selected by exact official base URL; a proxy in front of them keeps the standard contract. RollDek (`https://rolldek.com/v1`, `https://api-cn.rolldek.com/v1`) takes no tuning parameters and sends edits and references through multipart `/images/edits`, without masks. Agnes (`https://apihub.agnes-ai.com/v1`, `https://api.agnes-ai.cn/v1`) sends everything to JSON `/images/generations`: `aspectRatio` and `imageSize` (1K/2K/3K/4K, default 1K) map to Agnes `ratio` and `size`; text-to-image asks for `return_base64`, while edits and references pass inputs as data URIs in `extra_body.image` with `extra_body.response_format: "b64_json"`. Masks are rejected. Both dialects also accept URL results.

Root endpoints get `/v1` (OpenAI) or `/v1beta` (Google). Existing API prefixes are preserved. Recognized full chat/completions/responses or Google generateContent endpoints are converted back to their API prefix. Unknown full endpoint shapes, query credentials, URL userinfo, path injection and non-loopback plaintext HTTP are rejected. OpenAI edits upload decoded, validated PNG/JPEG/WebP files in caller order, with a PNG mask when requested; they use bounded multipart bodies and cannot replay automatically. Cancel, reconcile, preview and conversation continuation are not advertised.

Tests use local `httptest` servers and synthetic images only. A local artifact can be built from the repository root with:

```powershell
go build -o D:\Documents\Projects\Web\LiveAgent\.liveagent\kbrain-artifact\k-brain-image-workbench.exe ./cmd/kn
```

No custom linker flags are required.
