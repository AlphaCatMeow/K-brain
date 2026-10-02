# Codex app-server architecture reference

## Source scope

This reference uses `/Users/a/code/harness/codex/codex-rs`. The repository demonstrates the app-server protocol and runtime boundary used by desktop/IDE clients. It does not establish the complete Codex Desktop GUI, packaging, signing, or updater implementation.

## Relevant source boundaries

| Responsibility | Codex source | K-brain / LiveAgent mapping |
|---|---|---|
| Start an independently owned backend | `cli/src/main.rs`, `app-server/src/main.rs`, `app-server/src/lib.rs` | LiveAgent's Tauri host starts the bundled K-brain executable; frontend bootstrap connects before mounting consumers. |
| Transport and parent lifetime | `app-server-transport/src/transport/stdio.rs`, `websocket.rs`, `mod.rs` | K-brain retains authenticated loopback HTTP/JSON and SSE. `-parent-stdio` binds managed backend lifetime to the desktop parent. |
| Initialization and capabilities | `app-server-protocol/src/protocol/v1.rs`, `app-server/src/request_processors/initialize_processor.rs` | Validate protocol/version and advertise supported capabilities. Desktop identity alone is not authorization or compatibility proof. |
| Thread/session and turn ownership | `app-server-protocol/src/protocol/v2/thread.rs`, `turn.rs`, `app-server/src/request_processors/thread_processor.rs` | Backend-owned canonical sessions, runs, stable message IDs, model selection, cancellation, persistence and revisions. |
| Typed item lifecycle | `app-server-protocol/src/protocol/v2/item.rs`, `protocol/common.rs`, `core/src/session/turn.rs` | Frontend reduces versioned text, thinking, tool, permission, subagent and terminal events rather than interpreting provider wire formats. |
| Approvals and user input | `app-server-protocol/src/protocol/common.rs` server requests | SSE carries correlated requests; dedicated authenticated HTTP responses resolve pending approvals/questions. Reading notifications continues while the UI awaits input. |
| Durable history and recovery | `app-server/src/message_processor.rs`, `request_processors/daemon_snapshot.rs`, thread processors | Backend history is authoritative. SSE cursors improve replay; persisted session/run state supports recovery after process restart. |
| Models and configuration | `app-server/src/models.rs`, `app-server-protocol/src/protocol/common.rs` | Provider settings, discovery, credentials and generation remain in K-brain. LiveAgent uses the canonical catalog and settings APIs. |
| Skills and MCP | `app-server/src/skills_watcher.rs`, `mcp_refresh.rs`, `core/src/session/mcp_runtime.rs` | Backend owns resource loading, dynamic tool execution and configuration refresh; frontend renders and edits through adapters. |
| Native tool handoff | `app-server-protocol/src/protocol/common.rs` dynamic tool/process requests | Native terminal, files, SSH and desktop utilities can remain in the host, with correlated backend-authorized calls and results. Full LiveAgent bridge parity remains an acceptance requirement. |

## Adopted design

K-brain keeps its existing `kbrain.agent.v1` HTTP/SSE contract. Matching Codex's separation of responsibilities does not require adding a second JSON-RPC envelope. The important properties are typed contracts, one backend-owned agent loop, correlated bidirectional interactions, durable history, explicit process ownership, and compatible client/backend versions.

Canonical history remains independent of Gemini, Responses, Chat Completions and Anthropic request shapes. Provider adapters reconstruct each upstream request inside K-brain; provider-private signatures stay outside public messages.

LiveAgent installers pair the frontend with a pinned backend executable and computer helper. Configuration and runtime data resolve under `.liveagent`; older `.k-brain` data is migrated without overwriting existing destination files.

## Verification boundary

This document records architectural source references, not a full-parity completion claim. Current implementation, missing native/Gateway bridges, tests and browser verification are tracked in `liveagent-compatibility-matrix.md`. Codex source alone proves neither K-brain compatibility nor LiveAgent's packaged application behavior.
