# Computer use with Cua Driver

K-brain's default `computer_exec` implementation connects to a bundled or installed
`cua-driver mcp` process using the official Go MCP SDK. Cua owns native desktop
automation on macOS, Windows and Linux. K-brain does not copy Cua's Rust runtime,
reimplement its input drivers, or require the ChatGPT/LCU runtime.

## Configuration

```json
{
  "computer": {
    "enabled": true,
    "backend": "cua",
    "command": ["/absolute/path/to/cua-driver", "mcp"]
  }
}
```

`backend` defaults to `cua`. `command` is an argv array, not shell text. When
omitted, release builds first unpack their verified embedded runtime under the KB
data directory in `runtimes/cua/<archive-hash>`. Source builds without an embedded
runtime search PATH, `~/.local/bin/cua-driver`, and on macOS the
signed `/Applications/CuaDriver.app/Contents/MacOS/cua-driver`. Windows uses
`cua-driver.exe` and also checks the canonical
`%LOCALAPPDATA%\Programs\Cua\cua-driver\bin` install location; no Bash dependency
is introduced for this transport.

OS permission grants remain explicit user setup. Embedded macOS runtimes use
`mcp --direct` from the signed app executable; KB owns the connection and process.
No shared daemon is installed or launched. Host permission attribution still needs
native acceptance. Separately installed drivers retain the normal `mcp` behavior.
At runtime K-brain does not download a driver, grant permissions, enable existing
browser profiles, or switch to unrestricted mode. Keep Cua's trusted launch
configuration and capability manifest with the driver. An unavailable driver
fails on first computer use, not application startup or ordinary chat.

On macOS, the ordinary `mcp` command connects to the signed CuaDriver service.
After installing the driver, its documented service start command is:

```sh
open -n -g -a CuaDriver --args serve
cua-driver permissions status
```

Follow Cua's permission onboarding when grants are missing. `mcp --direct` is
an explicit alternative with host-attributed macOS permissions; it is not an
automatic fallback and does not prove that cursor overlays or capture work.

LiveAgent keeps downloading its fixed, checksum-verified K-brain Release
binary. This change adds no Go or Rust compilation to the LiveAgent release
workflow. The KB release workflow embeds the complete platform archive, including
license notices, before Go compilation for all six targets. `scripts/cua.lock.json`
pins upstream 0.33.4 URLs and SHA-256 hashes; `scripts/prepare-cua.py` verifies them.
Extraction uses a process lock, atomic rename and file hashes. Explicit commands
still take precedence. LA carries Cua inside its existing pinned backend download,
without another installer or compiler. Existing published releases are unchanged.

LA no longer exposes a dedicated Computer Use settings page. KB owns configuration,
permissions and execution; normal tool-result and approval rendering remain.

## Model-facing protocol

The outer tool name stays `computer_exec` so existing backend tool allowlists
and LiveAgent tool events remain usable. Its argument schema intentionally
replaces the old `code` pseudocode:

1. `{"action":"discover"}` returns runtime instructions and a concise tool
   catalog. It lazily opens the persistent MCP connection for this turn.
2. `{"action":"describe","tool":"get_window_state"}` returns that tool's
   original descriptor, including the complete input/output schemas.
3. `{"action":"call","tool":"get_window_state","arguments":{...}}` forwards
   native Cua arguments. Read the installed runtime's schema first; do not copy
   platform-specific sample arguments without discovery.
4. `{"action":"guide","guide":"SKILL.md"}` reads Cua's bundled guide through
   `resources/read`. Other documented guide filenames may be read on demand;
   older versions may not include every guide. These are reference documents,
   not automatically installed or executed K-brain skills.

Each tool must be described before it can be called, including tools nested in
`run_actions`. Catalog entries are discovered from the installed binary rather
than hard-coded from a different Cua version. Screenshots become vision content
parts; text, structured results and `isError` survive the bridge. A non-vision
model receives an explicit screenshot delivery error instead of silent loss.

## Sessions, permissions and interruption

- One persistent transport per K-brain turn preserves implicit Cua session
  state, snapshot handles and recordings across tool calls.
- K-brain calls `end_session {}` on completion, error or cancellation, under a
  fresh cleanup timeout, and then closes only its MCP connection. It never
  stops the shared daemon or closes user apps. Cleanup errors are returned.
- Lifecycle tools and public session labels are host-owned. Model calls cannot
  supply `session`, private underscore fields, or lifecycle calls hidden in
  `run_actions`. No Codex/LCU metadata is sent to Cua.
- A transport error makes that turn's computer client unusable; there is no
  automatic action retry or reconnect. An uncertain/partial action must be
  inspected before deciding what to do next.
- Only one K-brain turn per process may own the desktop client. Other turns
  receive a busy error. This is not a cross-process desktop lock: other apps
  and other K-brain processes still need external coordination.
- Existing tool authorization and plan/chat-mode filtering still apply. Cua's
  default standard mode is promptless for routine automation, unlike LCU's
  per-app approval model. The MCP adapter does **not** pretend to implement
  Cua's embedding SDK `DriverAuthorizationHost`; unsolicited MCP elicitation
  is canceled. Residual grants such as existing Chromium profile attachment
  require explicit trusted Cua setup.
- Foreground input and full-desktop control remain visible takeovers, not
  automatic fallbacks when background input fails. Inspect exact targets and
  verify the observed result instead of equating transport success to success.

## Compatibility

Set `computer.backend` to `legacy` to explicitly retain the previous
`k-brain-computer` helper and its `code` pseudocode. There is no silent fallback.
`computer.enabled=false` removes the tool during agent construction.

Legacy `computer.deny` and `computer.defaultDeny=true` cannot reliably constrain
Cua's window, browser and batch tools by app name. K-brain refuses Cua discovery
while those settings are present, rather than silently ignoring them. Use the
legacy backend or first configure a reviewed Cua capability manifest, then
remove obsolete settings. Legacy `computer.allow` is not a Cua permission grant.

The backend used by LiveAgent and the interactive terminal use the new adapter.
The existing noninteractive `kn run` and ACP computer-disable policies are
unchanged. Old conversation tool records remain historical results; new tool
calls must follow the new descriptor.

## Verification record (2026-10-07)

Reference checkout: `trycua/cua` at `c90e94662`; installed binary: Cua Driver
`0.33.4`. The implementation relies on the installed MCP contract, not assumed
parity between those versions.

An explicit read-only `mcp --direct` probe on this Mac successfully discovered
52 model-facing tools, described `get_window_state`, `click` and
`check_permissions`, read the bundled `SKILL.md`, called `check_permissions`,
and cleaned up its session. Accessibility was reported granted, Screen
Recording was reported **not granted**, and direct capture was not probed.
The signed background service was not running. No user app was driven and no
screenshot was captured during this probe.

Hermetic coverage includes modern/legacy MCP negotiation, describe-before-call,
private-field/lifecycle rejection, batch validation, process-wide ownership,
cancellation without replay, cleanup failures, stdio process teardown, screenshot
attachments, tool gates and a complete mock-model agent turn through MCP to
session cleanup. Run it with:

```sh
go test ./internal/computer/cua ./internal/tools ./internal/agent
```

The opt-in installed-driver protocol probe is:

```sh
KB_TEST_CUA_INSTALLED=1 go test ./internal/computer/cua \
  -run '^TestInstalledCuaReadOnly$' -v -count=1
```

Native screenshot/input, macOS service-backed onboarding, Windows interactive
desktop, Linux compositor behavior and LiveAgent end-to-end desktop acceptance
still require suitable test environments. Protocol mocks and cross-compilation
do not establish native desktop success.
