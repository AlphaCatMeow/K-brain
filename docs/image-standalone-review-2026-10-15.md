# 生图模块对独立客户端的兼容性检查

日期：2026-10-15。审查范围为本分支相对 upstream/main 的全部生图修改，以及恢复后尚未提交的增量。基线 ac3cbe6。

## 检查结论

本次未发现生图模块改变 CLI/TUI 的默认入口、Agent 执行、默认数据根或 Shell 选择逻辑。这些路径相对基线没有源码改动。图像引擎只在 backend 命令组装，初始化失败时仅关闭图像能力，普通服务继续运行。图像配置缺省为 nil，旧配置读写测试通过；图像设置在配置快照中深拷贝，普通设置更新基于快照保存。图像路由要求既有鉴权，远程监听不会获得本地默认图像身份。

这是当前检查和测试支持的结论，不是对全部交互场景的保证。PR 保持 Draft，等待独立终端交互、真实平台及跨端人工验收。

## 本轮新增验证

- 新增 TestStandaloneChatWithoutImageModule：没有图像模块时 health 正常、图像接口返回 503，普通会话仍通过真实 HTTP/SSE 完成回复。
- 从本分支源码编译独立可执行文件，运行 --help 成功。
- 用独立临时数据根运行真实 backend 进程。空配置时 health 正常，会话和图像项目 API 均返回 200；将 imaging 路径替换为普通文件后，图像初始化报错、图像 API 返回 503，health 和会话 API 仍正常。两种情况 stdin EOF 后均以 0 正常退出。验收目录已清理，没有接触个人配置或调用收费平台。
- 全仓 go test ./... -timeout=120s 实际运行结束，Agent、backend、config、storage、imaging 等包通过，但整个命令失败。cmd/kn、planning、tools、tools/bashrun、tui 存在失败，不能称全仓通过。
- cmd/kn 的 TestRunStartupHonorsTimeout 在默认 Git Bash 下执行 Windows 的 Start-Sleep 失败；显式 K_BRAIN_SHELL=powershell.exe 复测通过。Shell 选择实现和该用例均与基线一致，不在生图改动内。其余环境/夹具失败保留在 PR 中，不擅自修改独立客户端行为来满足某一测试环境。

## 后续验收

1. 独立 TUI 实际启动、输入、回复、恢复会话和退出，以及有/无图像配置时的配置保存。
2. 对全仓失败分别验证上游基线和本机环境，不能仅凭文件未改动就宣称所有失败都与本分支无关。
3. RunningHub 模型和工作流的生产注册、配置与验证条件仍未收敛；平台适配器文件及单测不代表生产支持完成。
4. 图像原生导出、异步恢复、取消和真实平台验收仍待完成。独立客户端兼容检查不替代这些验收。

本轮没有修改默认 Shell、TUI 或 CLI 行为，也没有发布、合并或收费调用。中文说明依本地 shuorenhua 技能核对事实、范围与未完成状态。

## 原生 Shell 复测结果

显式 K_BRAIN_SHELL=powershell.exe 后，cmd/kn、config、backend、storage 完整包通过。仍失败的用例为 tools/bashrun 的 TestOnUpdateThrottle，planning 的 TestPersistenceCASIdempotencyAndRollback，tui 的 TestLinkifyNativeUnicodePaths，以及 tools 的 TestBashToolOnUpdateCtx、TestLiveAgentManagedProcessWaitCursorAndStop。此轮没有宣称这些失败已全部完成基线归因；它们继续作为 Draft 的验证缺口，不将“独立客户端无影响”表述为无条件保证。

## 上游源码复现

随后使用 Go 的 -overlay，将相对 ac3cbe6 修改的 94 个 Go 源文件映射为上游版本，新增文件设为不可见，在同一工作区和相同 PowerShell 环境定向运行上述五个失败用例。没有切换分支或使用额外 worktree，也没有修改当前源码。

五个失败用例全部在上游源码覆盖运行中复现：TestOnUpdateThrottle、TestPersistenceCASIdempotencyAndRollback、TestLinkifyNativeUnicodePaths、TestBashToolOnUpdateCtx、TestLiveAgentManagedProcessWaitCursorAndStop。因此，这五项失败并非本分支生图代码新增。该证据只覆盖这五个用例，不代表上游全仓测试已运行或通过，也不能替代独立 TUI 人工验收。
