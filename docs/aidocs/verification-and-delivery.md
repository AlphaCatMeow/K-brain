# 交付与验收

## 回归分层

| 层级 | 目的 |
| --- | --- |
| 单元测试 | 协议、解析、状态 reducer、权限和边界错误 |
| 后端集成 | HTTP/SSE、存储、重启、运行终态、资源生命周期 |
| Gateway 集成 | `/ws/v2` protobuf、身份映射、队列和转发 |
| 前端契约 | 类型检查、共享组件、adapter、状态归属和构建 |
| 真实浏览器 | 桌面视口、`390×844`、交互、持久化和重载 |
| 打包生命周期 | bundled K-brain、动态端口、令牌、关闭和父进程退出 |

健康检查、fixture HTTP 请求、源码存在和单一页面渲染都不能替代端到端验收。

## 测试要求

前端变化必须执行受影响包的本地类型检查、测试、构建和 lint。共享状态、路由、布局或样式变化检查桌面和移动视口，并访问所有共用该状态的页面。

Gateway 变化必须至少覆盖真实 `/ws/v2` binary frames、后端 HTTP/SSE、重连/after-seq、错误身份、取消和终态。直接调用 K-brain API 的证据只能证明后端接口，不算 Gateway WebUI 通过。

后端变化执行目标包测试、必要的 race 和 `go vet`。关闭、取消、压缩和进程生命周期使用重复 race 回归。测试日志放在任务指定证据目录，不把临时日志写进产品目录。

## 双仓交付

K-brain 与 LiveAgent 是独立 Git 仓库。交付内容包括：

- `CHANGED_FILES.md`：两仓相对于各自基线的完整路径和状态。
- `integrations/liveagent/kbrain-backend.patch`：可应用到 LiveAgent 基线的工作树补丁。
- `integrations/liveagent/README.md`：补丁基线、树哈希和验证说明。
- 指定 scratch 中的 `frontend-tests.log`、`browser-queue-result-final.json`、`browser-canonical-evidence.json`、`resource-routes-final.json` 及相关原始功能证据。
- `docs/liveagent-compatibility-matrix.md`：105 行能力和 29 个 builtin 工具的逐项状态。

重新修改任一仓库后必须重新生成清单、补丁哈希和测试日志。未签名的开发包不能写成已发布或已签名版本。

## 证据记录

每份证据注明源码指纹、服务拓扑、使用的受控/外部上游、视口、请求入口、清理结果和未覆盖范围。截图只作为可见结果，原始事件、请求和持久化记录用于证明行为链路。

## 本轮验证记录

证据目录为 `/var/folders/s0/8xn72j6s20v1thgbz1__2sfw0000gn/T/grok-goal-b330cd3185db/implementer`。

- `workspace-protocol-browser-proof.json` 与 `workspace-activity-mobile-protocol.json` 保存真实 Chrome/CDP 的工作区订阅、ACK 和活动帧。Gateway-local owner 只承担本机工作区活动；原生 Git、终端及远程文件服务仍单独验收。
- `workspace-activity-browser-current.json` 汇总桌面和移动端证据；`resource-routes-final.json` 保留页面实测结果及资源能力边界。Skills 页面 offline 与 Agent-GUI 直连 K-brain 的 Skills/Memory round-trip 分别记录。
- `gui-final-closure.log`：2838 通过，0 失败，0 跳过。`webui-workspace-fix.log`：759 通过，0 失败，0 跳过。
- `resource-real-roundtrip-correct.log`：4 通过，0 跳过，包含真实 K-brain Skills/Memory 记录往返。
- `gateway-race-final-closure.log`：默认并行运行 `./internal/kbrain ./internal/protocol/pbws ./test/websocket`，三包通过。
- `gateway-full-closure.log`、`kbrain-full-closure.log`：完整串行 Go 测试通过。对应 `gateway-vet-closure.log`、`kbrain-vet-closure.log` 记录 vet。
- 交付补丁使用 scratch 临时 Git index 从 LiveAgent 基线重建，并比较树哈希；最后的 `delivery-closure.json` 记录补丁、清单、源码和证据哈希。历史失败日志保留，最新结果通过独立文件引用。

## 当前结论

本项目已有统一 K-brain 后端和多项可验证替换能力；完整 LiveAgent parity 尚未完成。使用兼容矩阵的 `implemented`、`partial`、`missing`、`unverified` 状态，不使用“全量完成”或“release”表述替代证据。
