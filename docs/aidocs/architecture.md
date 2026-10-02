# 架构与职责

## 目标

LiveAgent 保留桌面和 Web 界面、Gateway、Tauri 原生服务及既有操作入口；K-brain 承担模型调用、Agent 循环、工具生命周期和权威会话数据。切换供应商或模型时，前端使用同一份消息与运行协议。

## 调用路径

```text
LiveAgent React UI
  ├─ Tauri 后端启动器 → K-brain HTTP/SSE
  └─ Gateway WebUI → Gateway /ws/v2 → K-brain HTTP/SSE
                                            ├─ Agent 循环
                                            ├─ 供应商适配
                                            ├─ 工具、审批和资源
                                            └─ .liveagent 数据
```

桌面原生能力保留在 Tauri 或所属宿主；调用需要关联后端会话、运行和权限。Gateway 独立运行时，应显式区分后端已接管的能力与仍需要原生 Agent 的能力。

## 代码职责

| 层 | 路径 | 职责 |
| --- | --- | --- |
| 启动 | `cmd/kn/backend.go` | 配置、资源工厂、HTTP 服务和退出处理 |
| Agent | `internal/agent/` | 模型轮次、工具执行、子代理与压缩 |
| 供应商 | `internal/ai/` | OpenAI、Anthropic、Gemini 请求与事件适配 |
| 后端 | `internal/backend/` | 鉴权、会话操作、运行、事件日志与资源接口 |
| 协议 | `internal/protocol/` | 规范消息、内容块、工具关联和事件 |
| 持久化 | `internal/session/` | 会话、历史、运行相关数据 |
| 桌面 | `LiveAgent/crates/agent-gui/` | React 页面、K-brain 客户端与 Tauri 宿主 |
| 共享界面 | `LiveAgent/crates/agent-ui/` | 跨桌面/Web 复用的组件与契约 |
| Gateway | `LiveAgent/crates/agent-gateway/` | WebSocket v2、远端身份、排队与转发 |

## 实现约束

1. 桌面默认启动 bundled K-brain，使用动态 loopback 地址与每进程令牌。
2. 供应商调用和密钥管理由 K-brain 负责。模型执行失败应进入明确的错误/重试流程。
3. 前端入口保持可见且行为完整，功能状态由端到端链路确定。
4. 各层保存自己的身份映射，原始供应商数据在后端转换后进入统一协议。
5. 数据路径统一到 `.liveagent`；旧数据迁移保留源文件并保护更新的目标。

## 生命周期

服务关闭先停止接收新运行，取消活动运行，再等待其终态与历史持久化，最后回收进程、终端和记忆运行时。终态 SSE 的发送时机应允许消费者立即启动下一轮。

验收需要覆盖启动失败重试、后端意外退出、宿主退出、模型切换、会话删除和后端重启。开发包生命周期通过与正式签名发布分别记录。
