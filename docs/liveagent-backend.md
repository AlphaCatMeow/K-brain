# K-brain 与 LiveAgent 的会话协议

## 边界

K-brain 提供 HTTP JSON 与 Server-Sent Events（SSE）接口。协议版本为 `kbrain.agent.v1`。供应商原始请求与流格式由 `internal/ai` 适配；LiveAgent 消费 `internal/protocol` 定义的消息与事件。

当前接入是显式启用的迁移路径。LiveAgent 的原有 direct 模式仍然保留；K-brain 模式通过 `VITE_KBRAIN_BACKEND=true` 启用。聊天模型目录、会话列表与历史读取来自后端；本地 conversation ID 与后端 session ID 双向映射。重开会话会恢复消息和子代理报告。

历史的重命名、删除、置顶、分支、编辑重发、分享和分页通过后端接口持久化。编辑和分支使用后端消息 ID 与历史 revision；成功运行后前端重新读取权威历史。辅助文本生成和自动标题通过后端 `/v1/text/generate` 使用同一版本化规范消息模型，后端强制无工具并持有供应商凭据。模型设置页面通过 `/v1/settings` 管理供应商、模型和密钥；模型目录由后端配置维护，前端不执行供应商 discovery。

K-brain 模式下，工具执行、技能扫描、记忆和提示词由后端负责。LiveAgent 保留输入、流式展示、审批及窗口/文件选择等原生宿主交互；前端旧 memory、skills、Gateway、subagent、history 和 checkpoint 运行时命令明确拒绝。手动压缩、文件检查点回退和桌面轨迹统计在界面标记为暂不支持。原生终端、SSH、文件浏览等外围宿主能力保留原有边界，浏览器不提供这些原生命令。

## 启动

在 K-brain 中配置供应商与模型后运行：

```sh
go run ./cmd/kn backend -listen 127.0.0.1:47321
```

后端令牌可用 `K_BRAIN_BACKEND_TOKEN` 设置。LiveAgent 的连接参数为：

```sh
VITE_KBRAIN_BACKEND=true
VITE_KBRAIN_URL=http://127.0.0.1:47321
VITE_KBRAIN_TOKEN=<backend-token>
```

`VITE_KBRAIN_TOKEN` 是前端可见的后端访问令牌。供应商 API key 属于后端配置。`model.provider` 与 `model.model` 应使用后端 `/v1/models` 返回的路由标识。

## 字段盘点与映射

| 概念 | K-brain 内部 | 规范协议 | LiveAgent 展示 |
| --- | --- | --- | --- |
| 会话 | `session.Meta.ID` | `conversation_id` / session `id` | 独立的前端 conversation ID 映射 |
| 一轮运行 | backend run record | `run_id`, `client_request_id` | 前端消息 ID 作为幂等键 |
| 模型 | Agent model/provider | `{provider, model}` | assistant provider/model |
| 用户/助手 | `ai.Message` | `Message` | pi-ai `Message` 展示适配 |
| 工具调用 | `ai.ToolCall` | `{id,name,arguments}` | `ToolCall` 与工具轨迹 |
| 工具结果 | `tools.Result` | `{id,name,output,failed,cancelled}` | `ToolResultMessage` |
| 权限 | `tools.GateRequest` | `permission_id`, `tool`, `command`, `options` | 集中审批栏 |
| 子代理 | `agent.BackgroundTask` | `Subagent` | 状态与报告工具轨迹 |
| 流顺序 | durable event journal | 单会话单调递增 `seq` | 去重与续传游标 |
| 用量 | `ai.Usage` | input/output/cache token 计数 | assistant usage |

规范消息角色：`system`, `developer`, `user`, `assistant`, `tool`。内容块：`text`, `thinking`, `image`。工具参数必须为 JSON object；工具结果通过 `tool_call_id` 关联。供应商的 response item、chat chunk、Gemini candidate 留在后端适配边界。

## HTTP 接口

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/v1/health` | 健康状态与协议版本 |
| GET | `/v1/models` | 当前后端模型路由目录 |
| GET / PUT | `/v1/settings` | 读取脱敏配置 / 原子持久化供应商、模型与默认选择 |
| POST | `/v1/text/generate` | 无状态辅助文本生成；canonical `messages` 仅允许文本与 system/developer/user/assistant，后端不转发 tools |
| GET / POST | `/v1/sessions` | 列出 / 创建会话 |
| GET | `/v1/sessions/{id}` | 消息、子代理任务与最近事件序号 |
| PATCH | `/v1/sessions/{id}` | 持久化 title、pinned、archived 或 model |
| DELETE | `/v1/sessions/{id}` | 删除会话与运行日志，活动运行/子任务返回 409 |
| GET | `/v1/sessions/{id}/history` | 按 raw offset 分页读取历史，可校验 revision |
| POST | `/v1/sessions/{id}/branch` | 从指定用户消息分支，保留其回复 |
| POST | `/v1/sessions/{id}/edit` | 替换用户消息并删除后续历史，校验 revision |
| GET / POST | `/v1/sessions/{id}/share` | 获取 / 设置可撤销分享 |
| GET | `/v1/shares/{token}` / `/share/{token}` | 公开分享 JSON / HTML 页面 |
| POST | `/v1/sessions/{id}/runs` | 提交提示及可选模型切换 |
| GET | `/v1/sessions/{id}/events?after_seq=N` | 从指定序号之后重放并订阅事件 |
| POST | `/v1/sessions/{id}/runs/{run}/cancel` | 取消运行 |
| POST | `/v1/sessions/{id}/permissions/{permission}` | 提交审批结果 |
| POST | `/v1/sessions/{id}/close` | 结束当前会话运行并保存 |

列表支持 `page`、`page_size`、`cwd`、`cwd_empty`、`shared`，返回 `sessions` 和 `total_count`。历史窗口支持 `max_messages`、`before_offset`、`expected_revision`、`include_active`；返回 `session`、`revision`、`oldest_offset`、`has_more_before`、`total_message_count`、与消息数组一一对应的原始 `message_offsets`，以及可选 `active_messages`。原始 offset 可以不连续，客户端应按返回值构造消息引用。

编辑请求为 `{message_ref:{message_id},replacement:<规范 user 消息>,expected_revision}`。分支请求为 `{message_ref:{message_id},expected_revision?,title?}`。过期 revision 返回 409。编辑完成后启动运行时传入 `resume_message_id`，后端验证它指向已持久化的末尾用户消息，避免重复追加。普通发送不使用该字段。

分享请求为 `{enabled,redact_tool_content?}`，默认隐藏工具内容；返回 snake_case 分享状态。公开链接不需要后端令牌，撤销后原 token 失效。分享视图排除系统指令、工作目录、运行日志和内部任务信息。

CLI 可用 `-config FILE -session-dir DIR` 指定配置与存储目录；省略时使用现有用户/项目默认值。

设置 PUT 接受 `{defaultProvider?,defaultModel?,providers?,deleteProviders?}`。供应商包含 `id/name/api/baseUrl/models`，`apiKey` 为仅写入字段：省略保留现值，`clearApiKey:true` 明确清除。GET 仅返回 `apiKeyConfigured` 并脱敏 URL。保存成功后才发布新配置；已运行中的回合继续使用原客户端，下一轮在原 Agent 上刷新模型配置，保留任务和历史。已删除模型的下一轮返回错误。同名模型在不同供应商下使用相同元数据；冲突配置会明确拒绝。直接修改配置文件后需重启后端加载；当前设置接口采用最后成功保存的配置，不提供多编辑者 revision 冲突检测。

浏览器设置缓存只保留必要的界面偏好，加载旧缓存时清理其中的供应商和连接凭据。`VITE_KBRAIN_TOKEN` 会进入前端构建产物，应按客户端访问令牌管理；面向多用户的远程部署需要独立的认证与权限设计。

`/v1/text/generate` 请求体为 `{model:{provider,model},messages:[{role,content:[{type:"text",text}]}],output?:"text"|"json"}`，响应为 `{version,text,model,usage?}`。请求使用后端 Bearer 鉴权并遵循 HTTP request context；客户端取消或断开连接会取消供应商请求。后端错误只返回通用生成失败/取消信息，不回显上游 URL、请求头或凭据。

启动请求携带 `client_request_id`。同键同请求返回原 `run_id`，同键不同请求返回冲突。重连继续读取 SSE；客户端不得因为断线而重新提交另一轮 prompt。

每条事件包含 `version`, `conversation_id`, `run_id`, `seq`, `type`, `created_at`, `payload`。主要事件族：

- `run.accepted`, `user.message.appended`
- `assistant.text.delta`, `assistant.thinking.delta`, `assistant.message.created`
- `tool.call`, `tool.result`, `tool.status`
- `permission.requested`, `permission.resolved`
- `subagent.started`, `subagent.updated`, `subagent.completed`, `subagent.failed`
- `usage.updated`
- `run.completed`, `run.failed`, `run.cancelled`

流连接结束与运行终态是两个不同事实。客户端只有消费终态事件后才能确认运行结束。

## Fixtures 与验证入口

- `internal/protocol/testdata/canonical_messages.json`
- `internal/protocol/testdata/canonical_events.json`
- `internal/protocol/protocol_test.go` 与 `fixtures_test.go`：规范消息、图片顺序、工具参数校验、模型归属与用量持久化。
- `internal/protocol/providers_integration_test.go`：同一段历史依次经过 Chat Completions → Responses → Gemini → Chat Completions；使用真实客户端工厂和本地 HTTP 上游，检查工具关联、文本、用量、终止原因、上游错误与提前 EOF。
- `internal/ai/gemini_test.go` 与已有 Chat/Responses stream fixtures
- `internal/backend/server_test.go`、`history_contract_test.go`、`history_extended_test.go`：运行、历史变更、并发校验、稀疏分页、分享脱敏与重启。
- `internal/backend/tool_history_test.go`、`subagent_workflow_test.go`：真实工具审批、失败标志、子代理事件与持久化恢复。
- `cmd/kn/backend_cli_test.go`：构建真实可执行文件，两次启动与会话恢复。
- LiveAgent `test/providers/kbrain-client.test.mjs`
- LiveAgent `test/chat/kbrain-conversation-turn.test.mjs`：调用真实 text/agent turn 入口、本地 HTTP fixture、真实审批状态服务与展示归并函数。

```sh
go test ./internal/protocol ./internal/ai ./internal/backend
# 在 LiveAgent/crates/agent-gui 中：
node --test test/providers/kbrain-client.test.mjs test/chat/kbrain-conversation-turn.test.mjs
```
