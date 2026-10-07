# 工具执行与平台兼容复核（2026-10-07）

## 对照实现

参考 zcode CLI 的 `adapters/src/exec/bash-shell-provider.ts`、`execution-command.ts` 和 `core/src/tool/executor/result-display.ts`：shell 选择应集中管理，明确命令语法，不让同步/异步执行产生不同的编码、退出码和结果语义。zcode 的 Windows 自动选择偏向 Git Bash；KB 按用户要求采用下面的不同顺序。

## Shell 顺序与执行

- 显式工具 `shell` > 上下文选择 > `K_BRAIN_SHELL` > 平台自动选择。
- macOS/Linux：优先 PATH 的 Bash，其次 `/bin/bash`、`/usr/bin/bash`，不可用时才尝试 `$SHELL` 和 `sh`。
- Windows：PowerShell 7 `pwsh`（含标准安装目录）> Git Bash（标准安装目录、用户安装目录、从 git.exe 推断）> Windows PowerShell > CMD。
- 不把不明来源的裸 `bash.exe` 自动当成 Git Bash，避免误用旧 WSL 启动器。用户仍可显式选择 `wsl` 或 shell 绝对路径。
- 仅启动前查找可执行文件时回退。命令运行失败不换 shell 重跑，防止重复副作用。
- LA 的 `Bash`、`ManagedProcess` 新增可选 `shell` 字段；同步、yield 异步、托管进程共用 `bashrun.Command`，统一 PowerShell UTF-16 EncodedCommand、UTF-8 输出、退出码、CMD 引号处理和 Git Bash/WSL 选择。
- Windows PowerShell 的 `& executable` 是调用运算符，CMD 的 `&` 是命令分隔符，不再被 POSIX 后台扫描误拒绝。PowerShell 后台 `command &` 仍拒绝，长任务使用 Bash yield / ManagedProcess。
- shell 实际选择写入工具说明。普通命令优先 Bash 工具，交互需求使用 TerminalSession；文件读写仍优先专用工具。

## 浏览器故障修复

`Error: browser subsystem not initialized` 的根因是 runtime 原来只在 TUI 初始化，而 `backend`、`run`、ACP 都会注册 `browser_exec`。

- 三个非 TUI 入口现在在创建 agent 前初始化 browser manager、模式、CDP 配置及 computer policy，并在退出时释放浏览器。
- `browser.enabled=false`、`computer.enabled=false` 在工具注册前生效，而不是注册后仅改变布尔值。
- 真实 Chrome 验收中发现 fill 的逐字符按键路径超时且不能处理非 ASCII 字符。两个浏览器驱动共用 native setter + input/change 事件，支持 input、textarea、contenteditable、中文/emoji，拒绝 readonly/disabled。
- 支持模型常见的可选 `await helper()` / `print(await js(...))`，不改字符串内的 JavaScript。
- chromedp 浏览器生命周期与单次工具调用的取消分离，后续调用保留页面；单次操作仍响应取消和超时。
- 等待同一浏览器会话的调用可被取消，不再一直等待前一调用释放锁。

## 覆盖与限制

现有回归覆盖 LA 14 个基础工具的 schema，文件读写/编辑/删除/搜索/图片的真实 HTTP 模型循环，终端与托管进程、权限拒绝、取消、输出游标、运行归属、MCP、客户端委托工具、技能、记忆和日程路径。本轮新增 shell 顺序、PowerShell 调用、同步/异步退出码与中文输出、浏览器初始化和跨调用状态测试。

真实 macOS LA 使用 `gpt-6-luna` 完成 `browser_exec` 打开测试页、填中文、读取点击后的标题，以及 `Bash` 输出实际 Bash 版本。测试使用隔离 home，不修改正式用户的浏览器配置。Chrome 的 Rod 与 chromedp 两个驱动分别执行了真实浏览器回归。

跨编译成功只证明 Windows 可构建，不等于 Windows 实机功能已验收。`.github/workflows/tool-compat.yml` 在 Windows/macOS/Linux 运行 shell 回归，Windows 还覆盖 ConPTY、进程树取消及 PowerShell 带空格路径；远程 CI 的具体运行结果以验收报告为准。

不承诺所有外部 MCP 服务、所有 Windows 安装方式和电脑控制权限均可用。浏览器依赖 Chrome/Chromium 或已配置的 CDP/扩展；电脑控制依赖 `k-brain-computer` 和系统权限，缺失时保留明确错误，不伪装成功。
