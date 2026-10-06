# 查询运行接收记录

GUI 与 CLI 的状态边界：后端接收 run 后保存 client_request_id 与执行身份，前端网络失败不得凭显示状态重新发起另一轮。

`GET /v1/sessions/{id}/runs?client_request_id={id}` 返回原 `RunAccepted`（version、conversation_id、run_id、accepted_seq）。仅查询接收身份，不代表模型完成，也不代表进程重启后会恢复原执行。

- 鉴权沿用 `/v1` 通用鉴权。
- 缺少/空请求 ID 返回 400；会话或接收记录不存在返回 404。
- 查询复用只读历史运行时，不初始化模型或执行器；删除全部模型不影响查询。
- 已加载的运行时有持久化错误时返回 500，不将无法确认的记录报告为已接收。
- 前端可在 POST 响应丢失时查询，获得相同 run_id 后从 accepted_seq 订阅 SSE；未找到记录时不得自动创建新 run。

测试覆盖：接收前未知、接收后与 ACK 一致、重启后的只读查询、错误参数、无模型 factory、持久化故障。
