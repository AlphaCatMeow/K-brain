# Complete changed-file manifest

This delivery spans two independent repositories. Paths below are repository-qualified; LiveAgent entries are not paths inside the K-brain source tree. The complete frontend source and tests are also shipped as `integrations/liveagent/kbrain-backend.patch` in K-brain.

## K-brain

- Base commit: `7e9b7d5b2c195afa923bd7919b0b81287ffef7b2`
- Scope: backend implementation, protocol, adapters, persistence, tests, documentation and companion frontend delivery.

```text
K-brain/CHANGED_FILES.md
K-brain/cmd/kn/backend.go
K-brain/cmd/kn/backend_cli_test.go
K-brain/cmd/kn/main.go
K-brain/docs/liveagent-backend.md
K-brain/integrations/liveagent/README.md
K-brain/integrations/liveagent/kbrain-backend.patch
K-brain/internal/agent/agent.go
K-brain/internal/agent/background.go
K-brain/internal/agent/events.go
K-brain/internal/agent/events_test.go
K-brain/internal/agent/response.go
K-brain/internal/agent/response_metadata_test.go
K-brain/internal/agent/tool_execution.go
K-brain/internal/ai/factory.go
K-brain/internal/ai/factory_test.go
K-brain/internal/ai/gemini.go
K-brain/internal/ai/gemini_test.go
K-brain/internal/ai/message.go
K-brain/internal/ai/stop.go
K-brain/internal/backend/cors_test.go
K-brain/internal/backend/history.go
K-brain/internal/backend/history_contract_test.go
K-brain/internal/backend/history_extended_test.go
K-brain/internal/backend/server.go
K-brain/internal/backend/server_test.go
K-brain/internal/backend/settings.go
K-brain/internal/backend/settings_review_test.go
K-brain/internal/backend/settings_test.go
K-brain/internal/backend/subagent_workflow_test.go
K-brain/internal/backend/text.go
K-brain/internal/backend/text_test.go
K-brain/internal/backend/tool_history_test.go
K-brain/internal/browser/e2e_test.go
K-brain/internal/browser/parity_test.go
K-brain/internal/config/config.go
K-brain/internal/config/conflict_test.go
K-brain/internal/config/load_file.go
K-brain/internal/prompts/system.go
K-brain/internal/prompts/system_test.go
K-brain/internal/protocol/fixtures_test.go
K-brain/internal/protocol/protocol.go
K-brain/internal/protocol/protocol_test.go
K-brain/internal/protocol/providers_integration_test.go
K-brain/internal/protocol/testdata/canonical_events.json
K-brain/internal/protocol/testdata/canonical_messages.json
K-brain/internal/routing/client.go
K-brain/internal/routing/route_test.go
K-brain/internal/session/messages.go
K-brain/internal/session/metadata.go
K-brain/internal/session/mutation.go
K-brain/internal/session/page.go
K-brain/internal/session/session.go
K-brain/internal/session/session_test.go
K-brain/internal/session/transcript.go
K-brain/internal/tools/attachments.go
K-brain/internal/tools/attachments_test.go
K-brain/internal/tools/tools.go
K-brain/internal/tui/gitstatus_test.go
```

## LiveAgent

- Base commit: `e63588a1328be66530353ad6d274db14b5f0e68d`
- Integration commit: `cc594a60f89f51d737cd132efb3d59e64d3266ec`
- Files: 126
- All entries below are included in the companion patch.

```text
liveagent/crates/agent-gateway/web/src/lib/sidebar/webSidebarBackend.ts
liveagent/crates/agent-gui/src/App.tsx
liveagent/crates/agent-gui/src/agent-ui-adapters/composerImagePreview.ts
liveagent/crates/agent-gui/src/agent-ui-adapters/directoryPicker.tsx
liveagent/crates/agent-gui/src/agent-ui-adapters/imagePreview.ts
liveagent/crates/agent-gui/src/agent-ui-adapters/providerSettings.tsx
liveagent/crates/agent-gui/src/agent-ui-adapters/sandboxCapability.ts
liveagent/crates/agent-gui/src/agent-ui-adapters/systemSettings.tsx
liveagent/crates/agent-gui/src/agent-ui-adapters/workspacePreview.tsx
liveagent/crates/agent-gui/src/components/MacOsTitleBarSpacer.tsx
liveagent/crates/agent-gui/src/components/ReleaseAnnouncementDialog.tsx
liveagent/crates/agent-gui/src/components/cron/CronPromptRunner.tsx
liveagent/crates/agent-gui/src/lib/appUpdates.ts
liveagent/crates/agent-gui/src/lib/automation/backend.ts
liveagent/crates/agent-gui/src/lib/automation/hookRunner.ts
liveagent/crates/agent-gui/src/lib/backup/index.ts
liveagent/crates/agent-gui/src/lib/chat/conversation/run/gatewayBridgeEvents.ts
liveagent/crates/agent-gui/src/lib/chat/history/chatHistory.ts
liveagent/crates/agent-gui/src/lib/debug/agentDebug.ts
liveagent/crates/agent-gui/src/lib/git/tauriGitClient.ts
liveagent/crates/agent-gui/src/lib/host.ts
liveagent/crates/agent-gui/src/lib/kbrain/catalog.ts
liveagent/crates/agent-gui/src/lib/kbrain/client.ts
liveagent/crates/agent-gui/src/lib/kbrain/history.ts
liveagent/crates/agent-gui/src/lib/kbrain/mapping.ts
liveagent/crates/agent-gui/src/lib/kbrain/turn.ts
liveagent/crates/agent-gui/src/lib/kbrain/types.ts
liveagent/crates/agent-gui/src/lib/managed-process/backend.ts
liveagent/crates/agent-gui/src/lib/providers/deepSeekAttachments.ts
liveagent/crates/agent-gui/src/lib/providers/nativeResponsesAttachments.ts
liveagent/crates/agent-gui/src/lib/providers/runtime/providerRuntimeConfig.ts
liveagent/crates/agent-gui/src/lib/providers/runtime/streamByApi.ts
liveagent/crates/agent-gui/src/lib/providers/runtime/textOnlyRuntime.ts
liveagent/crates/agent-gui/src/lib/providers/runtime/types.ts
liveagent/crates/agent-gui/src/lib/providers/usageQuery.ts
liveagent/crates/agent-gui/src/lib/releaseAnnouncement.ts
liveagent/crates/agent-gui/src/lib/runtimePlatform.ts
liveagent/crates/agent-gui/src/lib/settings/storage.ts
liveagent/crates/agent-gui/src/lib/sftp/tauriSftpClient.ts
liveagent/crates/agent-gui/src/lib/shortcuts/globalShortcuts.ts
liveagent/crates/agent-gui/src/lib/sidebar/guiSidebarBackend.ts
liveagent/crates/agent-gui/src/lib/stt/desktopSttSettingsService.ts
liveagent/crates/agent-gui/src/lib/stt/desktopSttTransport.ts
liveagent/crates/agent-gui/src/lib/subagents/ipc/store.ts
liveagent/crates/agent-gui/src/lib/subagents/ipc/worktree.ts
liveagent/crates/agent-gui/src/lib/system/clipboardText.ts
liveagent/crates/agent-gui/src/lib/system/powerActivity.ts
liveagent/crates/agent-gui/src/lib/terminal/tauriSshLocalForwardClient.ts
liveagent/crates/agent-gui/src/lib/terminal/tauriTerminalClient.ts
liveagent/crates/agent-gui/src/lib/tools/browserTools.ts
liveagent/crates/agent-gui/src/lib/tools/builtinRegistry.ts
liveagent/crates/agent-gui/src/lib/tools/cuaSelfGuard.ts
liveagent/crates/agent-gui/src/lib/tools/fsTools.ts
liveagent/crates/agent-gui/src/lib/tools/invokeWithAbort.ts
liveagent/crates/agent-gui/src/lib/tools/mcpManagerTools.ts
liveagent/crates/agent-gui/src/lib/tools/mcpTools.ts
liveagent/crates/agent-gui/src/lib/tools/shellTools.ts
liveagent/crates/agent-gui/src/lib/tools/sshManagerTools.ts
liveagent/crates/agent-gui/src/lib/tools/terminalTools.ts
liveagent/crates/agent-gui/src/lib/tools/tunnelManagerTools.ts
liveagent/crates/agent-gui/src/lib/tray/trayMenu.ts
liveagent/crates/agent-gui/src/lib/tunnels/tauriTunnelClient.ts
liveagent/crates/agent-gui/src/lib/workspace-activity/tauriWorkspaceActivityClient.ts
liveagent/crates/agent-gui/src/lib/workspaceRootGrants.ts
liveagent/crates/agent-gui/src/pages/ChatPage.tsx
liveagent/crates/agent-gui/src/pages/chat/components/DesktopCheckpointRewindProvider.tsx
liveagent/crates/agent-gui/src/pages/chat/composer/composerDraftText.ts
liveagent/crates/agent-gui/src/pages/chat/gateway/useGatewayBridgeListeners.ts
liveagent/crates/agent-gui/src/pages/chat/gateway/useGatewayRunMirrorCoordinator.ts
liveagent/crates/agent-gui/src/pages/chat/gateway/useGatewayStatus.ts
liveagent/crates/agent-gui/src/pages/chat/history/useConversationHistoryActions.ts
liveagent/crates/agent-gui/src/pages/chat/history/useSharedHistory.ts
liveagent/crates/agent-gui/src/pages/chat/hooks/usePendingUploads.ts
liveagent/crates/agent-gui/src/pages/chat/hooks/useTauriFileDrop.ts
liveagent/crates/agent-gui/src/pages/chat/hooks/useUploadZoneDrop.ts
liveagent/crates/agent-gui/src/pages/chat/queue/useChatTurnQueue.ts
liveagent/crates/agent-gui/src/pages/chat/runtime/useChatModelSelection.ts
liveagent/crates/agent-gui/src/pages/chat/runtime/useManualCompaction.ts
liveagent/crates/agent-gui/src/pages/chat/runtime/useSendChatTurn.ts
liveagent/crates/agent-gui/src/pages/chat/surfaces/ConversationPaneHost.tsx
liveagent/crates/agent-gui/src/pages/chat/surfaces/ConversationTrajectorySurface.tsx
liveagent/crates/agent-gui/src/pages/chat/turns/runAgentConversationTurn.ts
liveagent/crates/agent-gui/src/pages/chat/turns/runKBrainConversationTurn.ts
liveagent/crates/agent-gui/src/pages/chat/turns/runTextConversationTurn.ts
liveagent/crates/agent-gui/src/pages/chat/workspace/cloneTasks.ts
liveagent/crates/agent-gui/src/pages/chat/workspace/useProjectTerminals.tsx
liveagent/crates/agent-gui/src/pages/chat/workspace/useWorkspaceProjects.ts
liveagent/crates/agent-gui/src/pages/settings/AboutSection.tsx
liveagent/crates/agent-gui/src/pages/settings/BackupSyncSection.tsx
liveagent/crates/agent-gui/src/shims/tauriCore.ts
liveagent/crates/agent-gui/src/shims/tauriEvent.ts
liveagent/crates/agent-gui/src/shims/tauriOpener.ts
liveagent/crates/agent-gui/src/shims/tauriPath.ts
liveagent/crates/agent-gui/test/chat/chat-react-performance.test.mjs
liveagent/crates/agent-gui/test/chat/conversation-title-job.test.mjs
liveagent/crates/agent-gui/test/chat/gateway-bridge-events.test.mjs
liveagent/crates/agent-gui/test/chat/history-share-origin.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-conversation-turn.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-host-boundary.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-model-catalog.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-settings-component.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-settings-ui.test.mjs
liveagent/crates/agent-gui/test/chat/kbrain-sharing-ui.test.mjs
liveagent/crates/agent-gui/test/chat/mention-app-suggestions.test.mjs
liveagent/crates/agent-gui/test/chat/sidebar-conversation-menu.test.mjs
liveagent/crates/agent-gui/test/chat/sidebar-reconcile.test.mjs
liveagent/crates/agent-gui/test/providers/kbrain-chat-history-seam.test.mjs
liveagent/crates/agent-gui/test/providers/kbrain-client.test.mjs
liveagent/crates/agent-gui/test/providers/kbrain-history.test.mjs
liveagent/crates/agent-gui/test/providers/kbrain-provider-boundary.test.mjs
liveagent/crates/agent-gui/test/providers/kbrain-turn.test.mjs
liveagent/crates/agent-gui/test/settings/storage.test.mjs
liveagent/crates/agent-gui/test/settings/workspace-resource-settings.test.mjs
liveagent/crates/agent-ui/src/components/chat/ChatHistorySidebarRows.tsx
liveagent/crates/agent-ui/src/components/chat/HistoryShareModal.tsx
liveagent/crates/agent-ui/src/components/chat/SharedHistoryManagerModal.tsx
liveagent/crates/agent-ui/src/components/chat/TranscriptMessageActions.tsx
liveagent/crates/agent-ui/src/i18n/translations/enUSSettings.ts
liveagent/crates/agent-ui/src/i18n/translations/zhCNSettings.ts
liveagent/crates/agent-ui/src/lib/chat/checkpointRewind.tsx
liveagent/crates/agent-ui/src/lib/chat/historyShareOrigin.ts
liveagent/crates/agent-ui/src/lib/sidebar/scope.ts
liveagent/crates/agent-ui/src/lib/sidebar/types.ts
liveagent/crates/agent-ui/src/pages/settings/KBrainSettingsSection.tsx
liveagent/crates/agent-ui/src/pages/settings/SettingsPage.tsx
liveagent/crates/agent-ui/src/pages/settings/providerUtils.ts
```

## Reproducibility

The companion patch was applied to a temporary Git index initialized from the stated LiveAgent base. Its resulting tree matched the integration commit tree exactly. Product code, shared UI and test files are part of that comparison. See `integrations/liveagent/README.md` for application and test commands.
