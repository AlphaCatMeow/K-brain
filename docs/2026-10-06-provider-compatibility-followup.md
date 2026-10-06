# LiveAgent v2 请求兼容性修复进度

日期：2026-10-06。基线：K-brain `550a410`，LiveAgent `6dfedb18`。

## 本次修复

- Anthropic Opus/Sonnet 4.6+、Claude 5 系列与 Mythos Preview 按模型生成 adaptive thinking 和 `output_config.effort`。
- 旧模型的预算严格小于实际 `max_tokens`；`minimal` 使用 1024；输出上限不足 1025 时不发送无效预算。
- Anthropic 流保存 thinking/signature/redacted_thinking。后端持久化 `provider_replay`，仅在协议、endpoint、模型一致时回放；canonical HTTP 历史不暴露这些字段。Chat Completions 不序列化该私有状态。
- Anthropic `server_tool_use` 的 JSON 分片不再误判为无对应本地工具，也不会下发给本地工具执行器。
- 前端导入的 display-only thinking 文本经过 canonical → ai.Message → 持久化 → canonical 后仍保留；这类无签名文本不会伪装成 provider 的签名思考块。
- 编辑历史仅重建 recorder 和消息上下文，保留原运行时的模型、工具、记忆和管理器。已删除模型的历史可编辑；原历史模型元数据仍保持不变。

## 验证

- `go test -race ./internal/ai ./internal/protocol ./internal/backend` 通过。
- `go test -p 1 ./...` 通过。
- `go build -o /tmp/kbrain-compat-20261006 ./cmd/kn` 与 `--help` 通过。
- 新增 11 组 Anthropic 请求参数用例、签名/加密块持久化及跨模型/endpoint 隔离、内置搜索分片、canonical 签名隔离、旧模型历史编辑回归。
- 使用真实 LiveAgent `runKBrainTurn` → 本次编译的 K-brain HTTP 后端 → 本地 Anthropic HTTP fixture → 实际 Read 工具 → 带签名的第二轮请求，端到端通过。脚本：`/private/tmp/desk/compat-live.mjs`；日志：`/tmp/kbrain-compat-live.log`。上游是测试服务，不代表真实供应商验收。
- LiveAgent 相关前端回归 25/25；GUI、Gateway TypeScript 通过。
- 真实 macOS WKWebView 检查 Cron、Memory、Skills、MCP、Planning 导航以及 1440/480 两档视口。该桌面进程仍使用原基线 sidecar，仅证明现有页面未回归，不宣称桌面已加载本次新二进制。

## 仍待完成

1. Chat Completions 的 reasoning_content 保存回放、重复工具名分片、模型专属参数及 max_completion_tokens。
2. Responses encrypted reasoning item 回放、include 合并、xAI cache 参数、模型 effort 映射。
3. Gemini thinkingConfig、parametersJsonSchema、工具图片结果。现有工具签名缓存保持不变。
4. `options.reasoning=off` 在 backend 仍转换为空 effort；默认开启推理的供应商仍需协议专属显式关闭处理。
5. 删除所有可用模型后，依赖 loadRuntimeByID 的历史操作仍没有独立的只读运行时；当前回退依赖至少一个可用模型。
6. 真实供应商、多轮原生搜索上下文、Windows PTY、桌面新 sidecar 的完整验收未完成。
7. 本次没有重复进行全部 main/v2 功能验收，也没有继续增加日程迁移功能；原日程后端迁移提交保持不变。

## 架构结论

LiveAgent 发送统一的 model/provider ID、prompt、options 和 canonical messages。最终供应商请求应由 K-brain 适配。不能将 display-only thinking 直接转换成 Responses 消息内的 reasoning block，或缺签名的 Anthropic thinking block；这会制造新的无效请求。
