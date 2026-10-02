# K-brain 与 LiveAgent 开发设计

本目录记录 K-brain 作为 LiveAgent 统一后端的架构、接口约束、迁移任务和验收标准。内容按本项目的 Go 后端、React 前端、Gateway 与 Tauri 宿主重新整理。

## 文档导航

| 文档 | 范围 |
| --- | --- |
| [架构与职责](architecture.md) | 模型执行、前端展示、Gateway 与原生宿主的职责 |
| [消息与模型](messages-and-models.md) | 供应商适配、模型切换、工具关联、设置和导入 |
| [会话与恢复](sessions-and-history.md) | 运行身份、幂等、历史分页、SSE 与数据迁移 |
| [Gateway 与队列](gateway-and-queue.md) | `/ws/v2`、排队、编辑、取消和跨宿主恢复 |
| [工具与宿主](tools-and-host.md) | 权限、进程、终端、浏览器和 SSH/SFTP/Tunnel |
| [压缩与轨迹](compaction-and-trajectory.md) | 压缩触发、检查点、文件记录与调试轨迹 |
| [资源与自动化](resources-and-automation.md) | Skills、MCP、记忆、提示词、Hooks 和 Cron |
| [界面与交互](frontend-interactions.md) | 编辑器、历史、流式呈现、移动端和异步状态 |
| [交付与验收](verification-and-delivery.md) | 测试、双仓交付、真实浏览器与发布边界 |
| [功能对齐计划](parity-plan.md) | 待完成任务、依赖和验收要求 |
| [参考来源与改写范围](sources.md) | 设计参考、来源版本和本项目适配方式 |

## 如何使用

- 阅读架构和对应主题，定位实现所在仓库与代码路径。
- 按文档中的验收要求补实现与回归测试。
- 功能状态以 [105 行兼容矩阵](../liveagent-compatibility-matrix.md) 为准；本文档中的“要求”“待补齐”描述目标。
- HTTP 字段细节参见 [后端协议](../liveagent-backend.md)，日常使用参见 [用户指南](../user-guide/README.md)。

代码路径以本仓库为起点；带 `LiveAgent/` 前缀的路径属于独立 LiveAgent 仓库。临时浏览器配置、测试日志和截图存放在当前任务指定的证据目录，产品文档只引用证据名称与验证范围。

## 当前边界

K-brain 已具备后端 Agent 循环、统一供应商适配、会话与事件持久化，以及多项资源和工具接口。LiveAgent 全功能替换仍在进行，Gateway 历史恢复、队列持久化、终端与远程宿主能力等按功能分别验收。本目录不继承参考项目的完成状态、截图、测试结果或签名发布结论。
