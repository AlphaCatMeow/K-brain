# 会话与恢复

## 权威数据与身份

K-brain session ID 标识后端会话，run ID 标识一次执行，`client_request_id` 标识客户端提交。Gateway conversation/run ID 与后端 ID 使用显式映射；重连、排队和历史加载不能依赖模型返回文本推断身份。

同一幂等键与同一请求返回原运行；相同键承载不同请求应冲突。重连续传使用事件序号，不重复提交提示词。

## 历史契约

- 会话列表与详情来自后端权威数据。
- 历史分页返回原始消息 offset、revision、窗口边界和剩余页状态。
- 消息编辑、分支和检查点定位使用稳定消息 ID。
- 过期 revision 返回冲突，客户端刷新后让用户重新操作。
- 失败或取消运行保留已产生内容及明确终态。
- 自动标题与辅助生成通过后端无工具接口执行。

主要入口为 `/v1/sessions`、`/v1/sessions/{id}`、`/history`、`/branch`、`/edit` 和 `/share`。字段约束参见 [后端协议](../liveagent-backend.md)。

## 流与恢复

SSE 包含会话、运行、序号、类型和 payload。客户端以 `after_seq` 请求增量并去重。网络连接关闭只表示传输结束，运行完成由明确的终态事件确定。

恢复应区分：

1. 浏览器重连，Gateway 和后端仍存活。
2. 页面刷新，界面内存已丢失。
3. Gateway 重启，relay 映射与队列可能已丢失。
4. K-brain 重启，从会话、运行日志和事件日志恢复。
5. 更换宿主，数据和权限来源发生变化。

各项单独验证。当前桌面 K-brain 历史客户端与 Gateway 历史转发是独立链路，前者的验收不覆盖后者。Gateway 的 `history.list` 和 `history.get` 已直接映射 K-brain 会话与历史，并通过列表重新发现 backend session；当前真实双视口新聊天和协议历史帧通过，Gateway 重启后列表也返回持久化会话，但 WebUI 侧栏历史行/搜索仍可能走旧的 Agent 路径并显示 `agent offline`，所以打开历史、续聊和重复提交尚未认定通过。rename、delete、pin、branch、share 等历史变更及浏览器 alias 的跨 Gateway 恢复仍未全部接入。

## 数据迁移

统一使用 `.liveagent`。旧 history/checkpoint 导入需保留来源记录、二进制文件前像、历史 revision 和可重复导入信息。迁移满足：源数据保留、较新的目标保留、重复执行可检测、失败可重试。

大文件、目录恢复、缺失检查点和文件访问竞态仍需专门验证。来源数据不完整时展示具体限制。

## 验收清单

创建 → 连续对话 → 刷新 → 打开历史 → 续聊；分页加载与稀疏 offset；编辑/分支冲突；取消后恢复；后端重启；Gateway 重启；模型切换后的历史继续使用。真实 Gateway 验收必须经过 `/ws/v2`。
