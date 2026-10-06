# LiveAgent v2 请求兼容性修复进度

## 第二轮：补齐请求兼容、零模型历史与 Windows 终端

日期：2026-10-06。本节更新下方第一轮的剩余项；原测试结果保留为历史记录。

### 实现

- **Chat Completions**：保存 `reasoning_content` / `reasoning` / `reasoning_text`，同协议、endpoint、模型回放为 `reasoning_content`；重复完整工具名不再拼接两遍。OpenAI 推理模型使用 `max_completion_tokens` 并移除采样参数；DeepSeek/GLM/Kimi 使用 `thinking`，Qwen 使用 `enable_thinking`，Grok 不发送 `reasoning_effort`。
- **Responses**：启用 `store:false` 和 encrypted reasoning include；持久化原始 reasoning items，按 item ID 合并终态，回放时检查协议、endpoint、模型；原生搜索 include 不覆盖 reasoning include；Grok/xAI 移除 cache key、cache retention 与 reasoning 参数。
- **Gemini**：按模型生成 `thinkingConfig`，工具 schema 使用 `parametersJsonSchema`，保留工具图片结果、合并连续同角色内容、避免重复版本路径。已有工具签名缓存不回退。
- **关闭推理**：后端保留 `off`；Anthropic/DeepSeek/GLM/Kimi 显式 disabled，Qwen false，支持的 OpenAI 模型使用 none。不能关闭的模型降为最低支持档位：o 系列/Codex 为 low、初代 GPT-5 为 minimal、Gemini 2.5 Pro 为 128 budget、Gemini 3 Pro 为 low、Gemini 3 Flash 为 minimal。此行为不是保证所有模型真正停止内部推理。
- **错误终态**：Chat content_filter/refusal、Responses refusal、Gemini prompt block/异常 finishReason/错误响应明确返回错误，不写入成功 assistant 消息。
- **零模型历史**：只读 GET 不初始化模型或 agent，不把临时只读运行时装入运行缓存；历史消息、revision 和分页 offset 来自同一个存储快照，避免并发写入时混用两次读取。发起新推理仍需要可用模型，零模型写操作不在本次保证范围内。
- **Windows TerminalSession**：新增原生 ConPTY，实现读写、缩放、退出码和取消；挂入 Job Object，关闭时终止进程树。支持 cmd、PowerShell/pwsh 参数，修正 cmd 命令引号与 Unicode 环境块。要求 Windows 10 1809+。
- **Unix 终端**：保留原 PTY 实现；race 测试发现并修复 Resize 获取文件描述符与 Close 之间的数据竞争。

### 验证与证据

- AI、protocol、backend、tools 四个包的 `go test -race` 通过；最终 `go test -p 1 ./...` 通过，`go vet ./internal/ai ./internal/backend ./internal/tools` 通过。
- 新增请求体、推理持久化/回放隔离、错误终态、零模型读取、显式 off 测试。Windows 生命周期/输入/取消/退出码测试已新增，HTTP 终端用例不再跳过 Windows 自动授权路径。
- Windows amd64 工具测试与 backend 测试交叉编译、工具包 vet、CLI 交叉构建通过；**没有 Windows 真机运行结果**，交叉编译不等同于 ConPTY 实测。
- LiveAgent 相关前端回归 **25/25**；真实 `runKBrainTurn` → 本次 K-brain → 本地 Anthropic HTTP fixture → Read 工具 → 签名回放通过，canonical 历史不泄露签名。
- 已替换测试桌面进程使用的 sidecar 并重新启动后端；实际 WKWebView 验证 Cron/Memory/Skills/MCP/Planning 导航、后端健康、1440×900 与 480×844 视口。测试 home 为 `/tmp/la-desktop/home`，不是用户正式配置。
- 桌面终端创建、输入、读回标记、缩放、重命名、关闭及文件操作通过；历史命令 **22/22**；日程 **26 项断言通过**，包括重启恢复、CRUD、revision 冲突、幂等重试、回收恢复、cron 图层及两档视口。桌面原有终端命令通过不等同于 Windows ConPTY 验收。
- 日志：`/tmp/kbrain-remaining-race.log`、`/tmp/kbrain-remaining-full-final.log`、`/tmp/kbrain-remaining-terminal-final.log`、`/tmp/kbrain-compat-live-remaining.log`、`/tmp/la-remaining-frontend.log`、`/tmp/la-remaining-desktop.log`、`/tmp/la-remaining-terminal.log`、`/tmp/la-remaining-history.log`、`/tmp/la-remaining-planning.log`。

### 明确保留的边界

1. 没有调用真实供应商；规则基于请求契约和本地 fixture，不代表每个中转站、模型别名都接受这些字段。原生搜索的完整多轮供应商上下文仍待验收。
2. Windows ConPTY、Job Object 子进程回收、WSL sandbox、安装包升级尚未真机验收；`bashrun` 的独立 interactive 模式仍未接入 ConPTY，本次实现针对 TerminalSession/ReadTerminal 和 `/v1/terminal`。
3. 既有日程迁移保持不变；独立 TUI 的 Planning 工具注册、特殊 ICS、通知投递、浏览器全局时区和上下文压缩的剩余事项见 LiveAgent 总报告，本次没有声称全部 main/v2 功能补齐。
4. 不修改发布标签、附件或发布工作流；这次代码不会自动进入已有 `v2.0.0-beta.1` 附件。

## 第一轮记录

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
