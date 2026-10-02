# Gateway 与队列

## 运行路径

WebUI 通过 Gateway `/ws/v2` 发送 protobuf 请求。Gateway 将聊天映射到 K-brain HTTP/SSE，保留客户端 conversation/run 身份并维护后端对应关系。代码入口位于 `LiveAgent/crates/agent-gateway/internal/protocol/pbws/` 和 `internal/kbrain/`。

## 队列行为

队列支持查看、上移/重排、删除、编辑、取消编辑和立即执行。快照包含 conversation ID、revision 与有序 item；每项保留提交身份、文本、附件和执行选项。

- revision 随队列变更在锁内递增，订阅者只接收快照。
- 入队后及时发送 `queued_in_gui`，解除前端的启动超时等待。
- `run_now` 将目标放到队首，并请求取消当前后端运行；等待后端终态后再派发。
- 编辑开始暂存原项及位置；保存使用 revision 校验；取消恢复原项。
- 无附件的详情返回可解析的 `uploadedFilesJson="[]"`。
- 重复提交使用既有 command/run 身份，不重复产生用户消息或执行。

## 交互并发

队列编辑状态须绑定开始编辑时的 conversation/item。切换会话、关闭编辑、异步加载返回和组件重渲染都需要校验所属会话。取消 RPC 成功后才清除需要恢复的原项；保存失败保留可重试的编辑内容。

桌面端与移动端使用同一契约，按钮位置和 composer 状态变化应分别验收。

## 当前缺口

relay-local 队列、revision、编辑暂存与去重信息仍需持久化及重启恢复。取消未获后端确认时如何重新确认和恢复派发，也需要完整生命周期设计。历史列表和详情已接入 K-brain 的 `/v1/sessions` 与 `/history`，并通过重新发现后端 session ID 恢复新 relay 的映射。旧浏览器 alias 在 Gateway 重启后的独立恢复、历史修改操作与完整跨宿主恢复仍需补齐；真实浏览器结果另行记录。

目录选择、遥测、终端、SSH/SFTP/Tunnel 等 RPC 应按实际宿主能力转发；K-brain 接管聊天不自动使原生 Agent 在线。必须保留原操作入口，并补齐实现与身份传播。

## 验收

使用真实 WebUI → `/ws/v2` → Go Gateway → Go K-brain。检查首发、长时间排队、编辑保存/取消、排序、删除、立即执行、取消后继续、页面刷新和重启。保存原始二进制帧、解码结果、视口和构建指纹。局部协议 fixture 与真实浏览器结果分开记录。
