# 按 ZCode 分层：前端、宿主、运行时与存储

## 当前范围

根据用户最新要求参考 ZCode 的实现，采用单对单连接，不实现三端任意多对多。
存储作为独立模块由 KB 加载，不额外部署网络数据服务。

```text
LA 交互层 → 协议客户端 → KB HTTP/SSE → Agent、工具、MCP、任务
                              ↓
                        storage.Local
                   会话 / 配置 / 记忆 / 提示词

桌面宿主 → 管理一个 KB 子进程、连接凭据、健康检查与退出回收
浏览器   → 连接一个独立启动的 KB
Gateway  → 在服务端转发 KB 请求与事件，不让网页执行工具
```

- 前端负责交互、展示、发送用户请求与审批决定，不注册或执行工具。
- 运行端负责 Gateway 服务、模型循环、工具与 MCP 执行、任务调度、取消和事件分发。
- 数据端负责会话、用户配置、记忆的持久化和版本检查，不执行模型、工具或任务。
- 第一阶段前端通过运行端访问数据，不增加前端直连数据端的第二条读写路径。
- 运行端的监听地址、数据连接凭据和启动参数属于部署配置，不能依赖数据端启动后才能读取。
- 用户配置的持久化归数据端；前端只能读取适合展示的投影，不能获得运行端使用的全部密钥。

KB 可以独立启动或重启，数据保留在明确的数据根目录。
允许用户启动多套使用不同数据根目录的部署；暂不承诺多个运行端共享写入。

## ZCode 参考依据

本地参考仓库：`/Users/a/code/harness/zcode`。

- `packages/services/src/zcode-agent/zcodeAgentProcessManager.ts` 的
  `processesByWorkspaceKey` 按工作区保存运行进程，并提供独立用途的 manager。
- 同一文件启动 `app-server --stdio`，通过管道连接其子进程。
- `packages/services/src/zcode-agent/zcodeStdioTransport.ts` 管理单个子进程连接。
- `apps/zcode-cli/packages/bootstrap/src/zcode-protocol/storage-startup.ts` 由 CLI
  加载 `SqliteSessionStore`，提供不创建 Provider/MCP 的存储准备流程。

因此，该桌面调用链支持管理多个工作区的子进程，但不能据此认定它实现了
前端、运行端、数据端任意互联的多对多架构。借鉴其职责划分和进程生命周期管理，
不要求将 LA 已有 HTTP/SSE 传输改成 stdio。

## 实施顺序

1. 完成 LA 到 KB 的执行边界：前端只发送请求、展示事件；所有工具由 KB 执行。
2. 为现有会话、配置、记忆存储提取接口，先保持现有格式与功能，避免平行创建另一套数据。
3. 将存储初始化与运行时创建分开，让一个运行端使用一套明确的本地存储。
4. 接入启动、健康状态、断线提示和重连；数据不可用时明确报错，不自动落到另一套本地存储。
5. 验证旧数据迁移、会话继续、配置变更、记忆读写、任务持久化及服务重启恢复。

写请求仍需版本冲突检查，因为单对单部署也会存在后台任务与交互请求并发。
连接中断后不能盲目重放工具操作或可能已经提交的数据写入。

## 当前实现状态

- `internal/storage/local.go` 集中打开会话、配置、记忆、提示词，不启动模型、工具或调度器。
- `cmd/kn/backend.go` 先打开存储，再装配运行时；API 与记忆运行时共享同一记忆存储。
- `backend -data-dir` 统一数据根目录；显式 `-config`、`-session-dir` 仍兼容。
- `backend -prepare-storage` 初始化和校验存储后退出，不启动 MCP、模型或 HTTP 服务。
- 空配置保持为空，格式错误明确报错，不悄悄生成模型目录或覆盖原文件。
- LA 聊天入口使用受管理连接，后端重启时不固定旧地址和旧令牌。
- LA turn API 拒绝客户端工具注册；旧后端委托的工具请求返回错误，不执行工具。
- Gateway 转换 KB 工具结果及图片；Computer Use 设置由 KB 保存、审批和执行。
- 本阶段不增加连接注册中心、多数据端选择、跨实例同步或分布式任务调度。

## 使用

以下命令需要包含本次修改的 KB 二进制：

```sh
kn backend -data-dir /absolute/path/to/liveagent-data -prepare-storage
kn backend -data-dir /absolute/path/to/liveagent-data -listen 127.0.0.1:47321
```

通过 `K_BRAIN_BACKEND_TOKEN` 环境变量传入自行生成的连接令牌。
独立启动不加 `-parent-stdio`；桌面托管保留该标志以在父进程断开后退出。

## 发布边界

- LA 原生宿主仅传入 `-data-dir` 和必要的 `-legacy-desktop-dir`；KB 在运行时启动前完成
  存储准备、旧目录迁移、空配置创建和权限收紧。原 Rust 迁移模块已移除。
- 迁移尊重旧版 `.migration-desktop-kbrain`、`.migration-legacy-kbrain` 完成标记，
  不会在升级后重新导入用户已经删除的记录。
- LA 旧工具模块仍存在于源码中，生产 KB 聊天链路不调用它们。
- Gateway 仍为独立服务端适配层，没有把全部 Gateway 实现合并进 KB 可执行文件。
- 发布下载方式不变；LA 必须固定到支持这些启动参数的 KB Release，不能使用旧版本二进制。
