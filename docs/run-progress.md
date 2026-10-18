# 运行阶段与首字计时

K-brain 拥有执行阶段，客户端只显示和本地计时，不从模型正文猜测执行状态。
参考 ZCode 的事件交付、grok-build 的运行可观测性，保留 HTTP/SSE 协议。

`run.progress` 是可选的追加事件，旧客户端可忽略。载荷包括 `stage`、自执行器
开始的单调时钟耗时 `elapsed_ms`，以及首次思考/正文出现时的
`first_thinking_ms` / `first_text_ms`。这些值不是客户端点击至显示的耗时。
`model_request_ms` 是本轮模型请求调用开始的偏移，不代表网络已发出。
不包含提示词、路径、供应商地址或凭据。

阶段为 preparing、compacting、waiting_model、thinking、responding、tool、
persisting。多次模型请求可重复进入 waiting_model；首次输出计时仅记录一次。
仅主运行的回调改变主运行阶段。每个增量仍按原协议交付，不增加逐 token 阶段事件。
终态仍由 run.completed / run.failed / run.cancelled 表示，不新增成功判断。
事件 journal 保存计时，断线后按既有序号重放，前端不重新提交运行。

SSE 空闲时每 15 秒发送不占序号的注释心跳，帮助代理维持连接。心跳不是
模型进度，不重置运行状态，也不表示模型已经响应。

验收：阶段先于对应输出；首字计时不随后续片段改变；多次模型请求、取消、
故障和子任务不产生假的完成；旧前端仍可接收文本；新前端连接旧后端仍可运行。

## 2026-10-18 验证记录

`internal/agent`、`internal/backend`、`internal/protocol` 完整测试和 vet 通过。
阶段/流式交付/心跳回归在 race 检测下连续运行三次通过。
真实 HTTP 流验证 preparing、waiting_model、responding 先于首段正文交付；
空闲注释心跳不消耗事件序号，重复阶段不追加事件，首次输出偏移保持不变。
LiveAgent 使用本次构建二进制完成 29 项跨仓库集成测试；浏览器使用脚本化模型供应商，
不以本地模型等待时长作为生产性能数据。
