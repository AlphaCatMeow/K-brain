# LiveAgent 客户端对齐与记忆整理回归（2026-10-24）

分支 `audit/memory-organizer-client`，基于 `24df53c0928f1c3ff9da39afe8d3e753f6108efe`（含尚未合并的 #52）。此记录描述本地集成工作区，未发布。

## 后端改动

1. `/v1/memory/manage` 增加 `memory_organizer` 命令，支持 `history`、`run`、`apply`，与 `/v1/memory/organize` 共用实现，方便 Gateway 复用现有类型化 MemoryManage RPC。
2. history 直接读取持久化 store，避免为只读操作创建 runtime。客户端请求限定手工整理；`scheduled` 自动应用仍由后端调度器负责。
3. 摘要相同不足以证明记忆重复。只有完整、未截断正文也一致时才生成删除建议。
4. 建议携带保留项和候选项的正文/摘要前置条件；执行删除时在 store mutation 锁内重新检查。任一项改变、缺失或超出读取窗口，拒绝删除并返回警告。
5. 旧 pending 建议没有内容校验条件时拒绝删除，提示重新整理。普通用户的手动删除不受此 organizer 限制。

此整理器仍是确定性去重，不是完整的模型智能合并；也不证明跨进程直接修改 Markdown 文件的原子一致性。

## 验证

- `go test -p 1 ./...` 通过。
- `go vet ./...` 通过。
- `go test -race -p 1 ./internal/memory ./internal/memoryruntime ./internal/backend` 通过。
- 新增真实 HTTP 管理命令回归：认证、拒绝 scheduled、manual→pending→apply→history、重复应用幂等。
- 新增内容回归：摘要相同正文不同不删除；审核后编辑候选项/保留项都不删除；旧无条件建议不删除。
- 使用最终编译 KB 在 macOS LiveAgent 原生 WKWebView 中完成两档宽度的建议生成、审核、应用与 store 查询；Web→Gateway→KB 完成相同流程。

LA 的完整矩阵在配套分支的 `docs/testing/2026-10-24-full-capability-audit.md`。真实外部模型/MCP、日历账号、图像/语音、Windows、长稳内存曲线等未在本轮完成，不能据单元测试宣称全功能可用。临时证据在 `/tmp/la-full-audit`，原始连接文件和请求日志含测试凭据，不上传。
