# 参考来源与项目化改写范围

## 参考材料

本目录最初用于吸收 `context-labs/whip` `.ai-docs` 中关于 Agent UI、Gateway、会话、流式呈现、终端、MCP、供应商和测试证据的设计经验。上游来源提交曾为 `88764670f1c6e2a57e22463e96cfb0d6021aea33`。

本项目没有把上游 `.ai-docs` 原样作为产品文档继续保留。上游项目名称、目录、路径、功能状态、测试结果、截图、二进制证据和发布结论均不自动适用于 K-brain 或 LiveAgent。当前文件是根据本项目代码和兼容矩阵重新编写的项目文档。

## 改写规则

- 上游抽象改写为 K-brain `internal/*`、LiveAgent `crates/*` 和实际 `/v1`、`/ws/v2` 接口。
- 上游的“已完成”或截图证据改为本项目的目标、实现边界或待验收项。
- 参考项目专属的 WhipCode、Loupe、Electron、Firefox、上游路径和不属于本项目的安装脚本不作为本项目入口。
- 只有当前工作区代码、当前测试和当前浏览器证据才能升级兼容矩阵状态。
- 设计建议与事实状态分开；本目录中的要求不等于已经实现。

## 事实来源优先级

1. 当前 K-brain/LiveAgent 源码和注册路径。
2. 当前版本的测试与真实 HTTP/SSE、`/ws/v2`、浏览器证据。
3. [LiveAgent 兼容矩阵](../liveagent-compatibility-matrix.md)。
4. [K-brain 后端协议](../liveagent-backend.md)。
5. 本目录的设计整理和外部参考。

外部参考只用于发现设计缺口，不用于证明功能已经完成。