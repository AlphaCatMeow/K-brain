# 消息与模型

## 规范消息

`internal/protocol` 定义前后端交换数据；`internal/ai` 负责供应商格式转换。统一角色包括 `system`、`developer`、`user`、`assistant` 和 `tool`。内容块包含文本、思考和图片；工具调用与结果使用稳定的调用 ID 关联。

模型标识使用 `{provider, model}`。同名模型可来自不同供应商，展示名称与后端路由标识分别处理。前端从后端目录获取可用模型，保存选择后由下一轮请求携带路由。

## 供应商适配目标

- OpenAI Chat Completions 与 Responses 使用同一规范历史。
- Anthropic Messages 的工具结果、思考内容和签名按其协议能力转换。
- Gemini 的内容顺序、工具响应与签名归属在重启和切换后仍一致。
- 供应商特有缓存、原生搜索、附件格式及用量进入各自适配层。
- 不可转移的供应商元数据依据目标协议处理，并以请求 fixture 验证。

跨模型回归至少覆盖文本、图片、工具调用/结果、失败助手消息、用量和终止原因。中断的流不能生成假完成结果，空错误回复应在历史中可见。

## 设置与导入

`/v1/settings` 管理供应商、模型和默认路由。模型发现通过后端 provider 接口执行。供应商设置需要保留自定义 API 类型、完整 URL、模型目录 URL、代理和有序自定义请求头。

LiveAgent 的供应商设置布局、模型新增/删除/启停/保存/重载、聊天切换，以及 CC Switch/Cherry Studio 导入都属于迁移范围。导入应保留源数据库，设置中的密钥通过仅写入字段更新，读取使用脱敏状态。

## 当前实现与待验收

实现入口包括 `internal/ai/`、`internal/backend/settings.go`、`LiveAgent/crates/agent-gui/src/lib/kbrain/` 及共享供应商设置组件。统一模型和部分导入链路已有回归与受控上游验收；附件上传、供应商选项完整传递、缓存/签名及外部模型的组合覆盖继续按兼容矩阵跟踪。

## 验证

- 后端：`internal/protocol/providers_integration_test.go` 和各供应商测试。
- 设置：后端 settings/discovery 测试、实际 Rust 导入解析器和前端导入控件。
- 用户流程：创建供应商 → 发现模型 → 启用 → 保存 → 重载 → 选模 → 聊天 → 切换模型续聊。
- 使用受控上游和真实外部供应商时分别注明，避免合并为同一验收结论。
