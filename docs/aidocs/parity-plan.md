# 功能对齐计划

本文档把参考设计转换为本项目的实现顺序；它不是完成声明。

## P0：统一运行主链

1. 保持规范消息、工具、权限、事件和模型路由的单一来源。
2. 完成 Gateway conversation/run 到 K-brain session/run 的持久映射。
3. 让页面刷新、Gateway 重启和 K-brain 重启可从权威历史与事件恢复。
4. 使队列查看、重排、删除、编辑保存/取消、run-now、取消和继续形成完整真实浏览器闭环。
5. 统一终端、进程和远程宿主请求身份，补齐 SSH/SFTP/Tunnel/Git 的实际远端执行。

## P1：资源和生命周期

1. 完成 memory extraction/organizer 的取消、重启、调度和 UI 状态。
2. 完成 Skills/MCP/Hook/Cron 的保存、重载、执行、错误、取消和历史。
3. 完善 compaction 的 mid-stream、post-tool、overflow、resume 和多客户端冲突。
4. 补齐 checkpoint、file ledger、trajectory 的大文件、目录和跨宿主语义。
5. 消除 stale response、task modal 复用和页面切换状态竞态。

## P2：供应商和交互

1. 完成 Gemini、OpenAI、Anthropic 的附件、工具、原生搜索、签名、缓存和重试 golden。
2. 完成 CC Switch/Cherry 导入的所有字段和实际桌面链路。
3. 补齐桌面与 `390×844` 移动端的队列、历史、终端、资源和设置交互。
4. 处理工作区路径并发变化、权限、软链接和上传引用。

## 每项工作的完成条件

- 有实现入口、明确身份/权限/错误契约。
- 有后端或 Gateway focused test，并覆盖失败路径。
- 涉及 UI 时有桌面和移动真实浏览器操作。
- 涉及 Gateway 时证据经过真实 `/ws/v2`，不是直连后端。
- 更新兼容矩阵对应行、测试日志和双仓交付材料。
- 没有用删除入口、隐藏按钮、`unsupported` 标志或 frontend bypass 代替实现。

## 当前明确缺口

队列与 relay 的持久化/cross-host recovery、历史 mutation 全量接管、reload/replay 的真实 Gateway 验收、目录选择导致的 terminal blocker、Dashboard telemetry identity、SSH/SFTP/Tunnel 完整 canonical 接管和 signed release 仍保持未完成或未验证。队列桌面/移动编辑保存与 Escape 取消已有真实浏览器证据；run-now/cancel/continue 和完整历史重载仍需单独闭环。具体状态以兼容矩阵为准。
