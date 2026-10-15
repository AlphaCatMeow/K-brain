# Direct synchronous adapters — Goal04 static implementation

This directory contains authored protocol fixtures and offline tests, **not provider receipts**. Synthetic PNGs are generated in tests. No credentials were read and no generation, upload, account query or paid API was called.

## Registry integration

Import `github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct`.

- `NewOpenAI(Options) (*Adapter, error)` — ID `openai-images`, version `1`; base URL includes `/v1`.
- `NewGoogle(Options) (*Adapter, error)` — ID `google-generate-content-image`, version `1`; base URL includes `/v1beta`.
- `NewVolcengine(Options) (*Adapter, error)` — ID `volcengine-ark-image`, version `1`; base URL includes `/api/v3`.
- `(*Adapter).Profile() Profile` returns independent copies of capability/parameter metadata. It is not `TargetVerification` and does not register/enable anything. Google `ContractComplete=false`. All profiles have `LiveVerified=false`, no continuation/preview/cancel/reconcile. Poll/Cancel/Reconcile return `imaging.ErrUnsupported` without network access.

`Options` requires an injected `HTTPClient`, `BaseURL`, `SecretResolver`, `Scope`, and `SecretRef`; optional `Limits` only tighten local bounds. Optional `CustomHeaders map[string]string` is defensively copied and validated: only OpenAI-Organization, OpenAI-Project and restricted X-* names; auth/token/key/secret, idempotency, forwarding, proxy, method and transport-control names are rejected, as are case-duplicate names and control characters. At most 32 headers / 16 KiB total / 4 KiB per value. Host, Content-Length, Content-Type, Accept and all adapter auth headers cannot be overridden. This supports safe inherited provider_ref extension headers; assembly maps `SecretRefs["apiKey"]` to `SecretRef` and checks AuthKind (`bearer` for OpenAI/Ark, `api_key_header` for Google) before construction. Client/transport pooling and CloseIdleConnections remain owned by assembly, not by per-submit adapter code. Auth ownership: the backend resolver owns secrets at rest; the adapter resolves the scoped ref on each Submit and writes either `Authorization: Bearer ...` or `x-goog-api-key`. The adapter never reads env credentials. The caller owns network/proxy/egress policy and must supply a trusted transport without retries, injected auth, request logging or hidden redirects. The adapter copies the client, removes its cookie jar, refuses redirects, removes `GetBody` replay support and sends no idempotency header. HTTPS is required except loopback HTTP for httptest.

Call `Submit` with an explicit TargetSnapshot (adapter ID, version `1`, kind `model`, RemoteID), one item (`Request.Count=1`), and authorized ResolvedAssets in exactly the same order/identity as Request.Inputs. The engine already reduces batch items to count=1. Request.Parameters uses native snake_case names for OpenAI/Ark; Ark's flat `optimize_prompt_options.mode` maps to the native nested object. Google accepts no optional parameters in this minimal profile. Unexpected parameters, continuation, model families, and count are rejected before HTTP. Explicit Ark `ep-*` is accepted only on the integration assumption that registry evidence binds it to Seedream 5.0 pro; registry must enforce that binding.

RemoteIDs allowed: OpenAI `gpt-image-2` / `gpt-image-2-2026-04-21`; Google `gemini-nano-banana-2.1`; Ark `doubao-seedream-5-0-pro-260628` or an explicitly verified endpoint ID. These are allowlists, never default selection. No generic OpenAI-compatible inference is made.

## Implemented restrictions

- OpenAI: JSON generations and JSON edits using ordered `images:[{image_url:dataURL}]`; `n=1`; selected size/quality/output format/compression/background fields. No multipart, Files API, stream or legacy response_format. Mask uses `mask:{image_url:dataURL}`, applies to the first image (must be the source), requires matching PNG dimensions and a fully transparent region. Alpha is unchanged. This is guidance to the model, **not exact pixel locking**. Local maximum is 16 total inputs including mask (stricter than 16 images plus mask).
- Google: single-turn generateContent, canonical camelCase contents/inlineData and responseModalities TEXT+IMAGE; up to 14 ordered PNG/JPEG/WebP references; semantic edit, no mask. Final non-thought images across candidates/parts are retained, at most 16; STOP and model role required. Text/signature history is not retained because current RemoteResult has no continuation/text storage contract. Thus continuation is disabled even though the provider supports conversation history. MAX_TOKENS/unknown finish reasons conservatively become unknown; partial valid images in a malformed/incomplete response are not returned. These limits are local and do not claim a provider candidateCount limit. Contract remains incomplete; do not enable a Google target.
- Ark: Seedream 5.0 pro ordinary single image generation/reference/semantic edit; ordered `image` array; `response_format` defaults locally to b64_json; size tiers 1K/1.5K/2K or validated pixel dimensions, png/jpeg output, watermark and optimization mode. No `n`, group generation fields, layer decomposition, background, mask, tools or stream.

Defaults: 64 MiB encoded request/response, 20 MiB per image, 40 MP. Google entire encoded request is further limited to 20,000,000 bytes; OpenAI each data URL to 20,971,520 characters; Ark inputs to 36 MP, both edges >14 pixels, ratio 1/16..16. Byte and pixel caps are local intersections, not claims that providers accept every format or size. Prompt ceiling 32,000 Unicode codepoints is a local restriction for all profiles (OpenAI documented limit; stricter local policy for others).

Responses are bounded before parsing. Strict canonical base64 (including pad bits and rejecting CR/LF), real MIME, pixel bounds and full image decode are checked via offline `imaging.DecodeImageContent`. Only bytes or internal `OutputSource{Kind:"url"}` leave the adapter. OpenAI GPT Image URL results are rejected. Ark HTTPS URLs remain untrusted until the engine's future safe downloader validates egress, downloads, decodes and atomically saves; **download remains blocked/unimplemented here**. `RemoteResult.State=completed` means provider response complete, not a persisted successful job. No URL download occurs in this package.

`imaging.Usage` only represents money. Provider token/image usage cannot populate it, so Usage is nil even when native token counts exist; no fabricated 0 amount. Empty/text-only/blocked responses fail with NO_IMAGE_OUTPUT. Sent request transport failures, lost/truncated/invalid responses, redirect, 408/499/5xx, and unstructured 409/429 become DeliveryUncertain + unknown. Explicit 409/429 error envelopes are rejected; ordinary 400/401/403/404 are rejected. No raw body, prompt, auth, URL, provider message or wrapped transport cause is exposed in errors. No retries are performed.

## Source verification and gaps (2026-10-11)

The requested `web.run` tool was not available in this agent. Actual public browser requests were made instead, without login or API calls:

- OpenAI official `https://developers.openai.com/api/reference/resources/images/methods/edit` returned **403 FORBIDDEN**. Official alternate `https://platform.openai.com/docs/api-reference/images/createEdit` returned a Cloudflare block page. Consequently OpenAI implementation relies on the existing repository research `docs/research/image-studio-2026-10-11/openai.md`; **not newly/currently confirmed by this implementation run**. Its static ContractComplete follows that research's restricted contract, not successful new verification. Registry must preserve this evidence gap and keep targets disabled until genuine connection/model verification.
- `https://ai.google.dev/api/generate-content?hl=en` loaded successfully: confirmed v1beta path, required model/contents, generationConfig, candidates/content/parts, usageMetadata, promptFeedback. `https://ai.google.dev/gemini-api/docs/generate-content/image-generation?hl=en` navigation initially timed out; subsequently loaded page text was successfully read, including hidden REST samples: confirmed x-goog-api-key, model name, inlineData, TEXT/IMAGE modalities, 14 reference images and thought image distinction. No independent HTTP status receipt was captured. Existing responseFormat enum/model support discrepancies from google.md remain unresolved; this package deliberately omits those fields.
- `https://docs.volcengine.com/docs/ark/image-generation-api?lang=zh` initially showed only its shell, then loaded; visible and public collapsed text were read. Confirmed Beijing v3 path, Bearer sample, 5.0 pro model, ordinary single-image/no stream/no group generation, image data URLs and array, max 10 refs, image/size bounds, response_format, output_format, watermark, usage/error fields. Page displayed update `2026.10.10 22:39:45`. No API debugging controls were activated.

All account entitlement, region reachability, live generation/edit/mask quality, actual usage/errors and persistence/download integration remain unverified. Static httptest is not live acceptance. Registry should bind trusted evidence to connection ID/target revision separately. No files outside this direct package were edited.

## Validation

Run from the K-brain checkout, after clearing LIVEAGENT_HOME/K_BRAIN_HOME and setting HOME and USERPROFILE to the workspace `.liveagent/image-studio-test-home`:

```powershell
$env:LIVEAGENT_HOME = $null
$env:K_BRAIN_HOME = $null
$env:HOME = 'D:\Documents\Projects\Web\LiveAgent\.liveagent\image-studio-test-home'
$env:USERPROFILE = $env:HOME
gofmt -w internal/imaging/adapters/direct
go test ./internal/imaging/adapters/direct -count=1
go vet ./internal/imaging/adapters/direct
```

Only httptest/fake transports and synthetic small PNGs are used. Test fixtures are hand-authored schema examples, never official responses. Tests assert the actual Submit HTTP method/path/auth/body, edit/reference order, mask rules, reader closure, model/parameter/input limits, mixed final/thought output, invalid MIME/base64/image bodies, absent monetary usage, bounded responses/requests, no URL fetching, no redirect or replay, dropped responses, cancellation, error sanitation and unsupported methods.
