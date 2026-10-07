# zcode / dsh 缓存实现对照与 K-brain 移植

## 参考版本和源码

- zcode：`29628c9acdb81b703bbd4080c207a0e7ce5e276e`
  - `apps/zcode-cli/packages/core/src/runtime/helpers/provider-request-messages.ts`
  - `apps/zcode-cli/packages/core/src/runtime/helpers/provider-mid-conversation-system.ts`
  - `apps/zcode-cli/packages/core/src/runtime/methods/context-refresh.ts`
  - `apps/zcode-cli/packages/core/src/context/builder.ts`
- deepseek-harness：`5badb15009ae1756c3afe0ae0cef1faafc290ccc`
  - `packages/core/agent-loop/src/runtime-context.ts`
  - `packages/core/agent-loop/src/agent.ts`
  - `packages/compaction/compaction-basic/src/summarizer.ts`
  - `packages/core/agent-loop/tests/request-cache.e2e.ts`

## 实现对应

| 参考机制 | K-brain 实现 |
| --- | --- |
| dsh RuntimeContextProjection 按 source 识别上下文，不根据用户文本判断身份 | `ai.Message.PromptSnapshots` 保存 source/text；公开历史不暴露内部字段 |
| 扫描 retained history 恢复状态，仅变化时提交，空值显式清空 | `agent/context_snapshots.go`；重载去重、清空、压缩后重建；旧 PromptContext 兼容读取 |
| 动态上下文在发生位置追加，而不是改写 system 头部 | 后台记忆与旧 `memory.md` 共用快照路径；新上下文通知持久化，不再下一请求消失 |
| zcode 将内部来源剥离后再序列化，保持工具结果因果位置 | `withPromptContext` 请求投影；工具结果顺序不变；供应商请求不发送 snapshot metadata |
| zcode 最后有效消息缓存标记、静态前缀缓存边界 | KB 既有 Anthropic 工具/system/末尾消息边界保持不变，不给 Responses 填 Anthropic 专有字段 |
| dsh summarizer 复用历史、system、tools，末尾追加摘要指令 | 同客户端同模型压缩重放待压缩历史前缀和当前工具定义；保留请求推理/采样设置 |
| 无效摘要不落盘 | 空摘要拒绝，历史不丢；既有截断摘要拒绝逻辑保持 |

系统提示词变更、工具变更、模型路由和压缩仍可开启新的请求序列。dsh 对 in-history
system 更新有明确供应商能力条件；zcode 的 rebuildContextPrefix 也会替换上下文前缀。
因此没有将所有 system 更新强制降为 user，也没有冻结过时工具来追求数字。

独立摘要模型和估算超出上下文预算时使用原有有界文本摘要，避免为复用缓存发送超限请求。
本轮不更改用户的缓存开关、模型、上游地址、亲和头和统计分母。

## 验证

- `go test -p 2 ./...` 通过。
- `go test -race ./internal/agent ./internal/backend ./internal/ai ./internal/session` 通过。
- 新增覆盖：来源身份与用户文本隔离、旧数据兼容、删除通知、重启去重、清空上下文后恢复、
  同模型摘要前缀/工具复用、独立摘要路由与预算回退、空摘要保护。
- 更新 TUI 和 ACP 记忆测试为实际请求验收，确认记忆仍交付给模型，而不要求其位于 system。
- 原有 Responses / Anthropic / Chat Completions 前缀、定时任务、MCP、会话持久化测试通过。

真实 LA runKBrainTurn → KB → gpt-6-luna 三轮工具请求：9 次主对话调用，
72290 输入 tokens，51712 cached tokens，总命中率 71.53%；相邻请求消息前缀变化 0 次。
首轮与一次续请求返回零命中，故尚不能声称整体命中率提升或达到 97%。
本轮摘要复用由真实本地 HTTP 序列化测试验证，没有对真实上游进行长对话压缩计费测量。

本次为 KB 源码实现与验证。LA 仍下载锁定的 `v0.107.6-beta.1`，未发布新的 KB/LA 安装包。
