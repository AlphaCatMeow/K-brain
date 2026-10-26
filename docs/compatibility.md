# K-brain 兼容约定

> 状态：草案（2026-10-26），待评审。配套设计：LiveAgent 仓库
> `docs/design/2026-10-26-kbrain-decoupled-updates.md`。
>
> 本文是 K-brain 对外的兼容承诺。每次发版前按文末清单逐项核对；违反本文的改动
> 只能随协议主版本升级一起发布。

## 1. 为什么需要这份约定

K-brain 有三类使用方：

| 使用方 | 怎么用 K-brain | 版本关系 |
|---|---|---|
| 独立 TUI（`kn`） | 用户直接运行 | 只有 K-brain 自己 |
| LiveAgent 桌面端 | 以后端模式启动 `kn backend`，走 HTTP + SSE | LiveAgent 可能比 K-brain 新，也可能比它旧 |
| LiveAgent Gateway / WebUI | Gateway 通过 `-kbrain-url` 连远程 K-brain，或经桌面端转发 | 同上，且 Gateway 与 K-brain 常由不同的人部署 |

LiveAgent 计划在不发新版的情况下自动获取最新的兼容版 K-brain。这要求：

1. 旧 LiveAgent 配新 K-brain 时，旧功能照常可用。
2. 新 LiveAgent 配旧 K-brain 时，LiveAgent 能识别出缺失的能力，自己降级。
3. 不兼容的改动有明确边界，LiveAgent 不会被自动升级到用不了的版本。
4. 以上任何改动都不能改变 TUI 的行为。

## 2. 四个版本号

| 名称 | 例子 | 含义 | 谁来读 |
|---|---|---|---|
| 发布版本 | `v0.107.6`、`v0.107.6-beta.22` | 一次发版的标识；带 `-` 的是预发布 | 人、自动更新 |
| 协议主版本 `protocol.major` | `1` | HTTP/SSE 接口与后端启动契约的兼容代际 | LiveAgent 桌面端、Gateway |
| 协议次版本 `protocol.minor` | `7` | 同一主版本内的累计加法次数，只增不减 | 诊断、日志 |
| 存储版本 `storage.version` | `1` | `~/.liveagent` 下 K-brain 数据的格式代际 | K-brain 自己、自动更新的回退保护 |

此外还有能力集 `capabilities`：标明具体功能是否可用（见第 4 节）。LiveAgent 判断"能不能用某功能"只看能力集，
不看发布版本号或次版本号。

## 3. `/v1/health` 契约

### 3.1 主版本 1 的响应

```json
{
  "status": "ok",
  "version": "kbrain.agent.v1",
  "protocol": { "name": "kbrain.agent", "major": 1, "minor": 7 },
  "build": { "version": "v0.107.7", "revision": "<git sha>", "channel": "stable" },
  "storage": { "version": 1, "minReader": 1 },
  "capabilities": { "session_exists": 1, "...": "..." }
}
```

- `version` 是旧字段，主版本 1 期间**永久保留**为 `kbrain.agent.v1`。现有 LiveAgent（包括 Gateway）
  按这个字符串严格比对，删改它等于让所有旧客户端拒绝连接。
- `protocol`、`build`、`storage` 是新增字段。缺少这些字段的旧 K-brain 一律按
  `major: 1, minor: 0, storage.version: 1` 处理。
- 主版本升到 2 时，`version` 变为 `kbrain.agent.v2`。旧 LiveAgent 会如预期地拒绝连接，新 LiveAgent
  按 `protocol.major` 判断。
- 响应必须在不需要模型凭据和网络的情况下返回（与现在一致），自动更新用它判断"能不能用"。请求本身仍需后端令牌
  （Bearer，`K_BRAIN_BACKEND_TOKEN`），health 在鉴权之后处理。

### 3.2 错误响应与 SSE 事件

所有 JSON 响应与 SSE 事件里的 `version` 字段在主版本 1 期间同样保持 `kbrain.agent.v1`，改动它属于破坏性改动。
现有客户端的比对方式：LiveAgent 桌面前端对运行受理、会话状态、事件流、队列、轨迹、文本生成的响应严格比对，
缺字段也拒绝；Gateway 对队列响应严格比对，对 StartRun 响应与 SSE 事件只在字段非空且不等时拒绝。

## 4. 能力集

### 4.1 规则

- 能力名用小写加下划线，值是整数版本，从 `1` 开始。值为复合对象的能力（如现有的 `image`、
  `permission_modes`）保持现状，新增能力一律用整数。
- `image.version`（当前 `kbrain.image.v1`）被 LiveAgent 严格相等比对，主版本 1 期间同样不得改变；生图接口的
  加法改动用新的整数能力表达。
- **新增**：任何新接口、新字段、新行为，只要 LiveAgent 需要据此决定是否启用功能，就新增一个能力名。
- **增强**：同一能力在不破坏旧调用方的前提下扩展时，把值加一（例如 `session_exists: 2`）。调用方用
  "大于等于"判断。
- **不删除**：主版本内，已发布的能力不删除、值不降低。要停用某个能力，先在本文登记弃用，
  直到下一个主版本才移除。
- **不撒谎**：只有接口真正可用时才声明。依赖可选子系统的能力（例如生图）要反映运行时状态，
  不能只看编译开关。

### 4.2 现有能力登记

| 能力 | 当前值 | 含义 | 首次发布 |
|---|---|---|---|
| `session_queue` | 1 | 会话排队接口 | v0.107.6-beta.16 |
| `computer_session_approval` | 1 | Computer Use 会话级授权 | v0.107.6-beta.17 |
| `memory_switch` | 1 | 记忆总开关 | v0.107.6-beta.20 |
| `image_model_toggle` | 1 | 供应商模型逐个开关生图 | v0.107.6-beta.20 |
| `session_exists` | 1 | `POST /v1/sessions/exists` | v0.107.6-beta.22 |
| `permission_modes` | 列表 | 支持的权限模式 | — |
| `image` | 对象 | 生图子系统是否可用及其接口版本 | — |

新增能力时同步更新本表，并在 PR 里写明 LiveAgent 侧对应的功能。

## 5. 主版本内允许与禁止的改动

| 类别 | 允许（加法） | 禁止（需要升主版本） |
|---|---|---|
| 接口 | 新路径、新方法；旧路径新增可选参数 | 删除或改名路径；改变已有参数的含义或必填性 |
| 响应 | 新增字段 | 删除字段、改名、改类型、改语义；把可选字段变成可能缺失的必需字段 |
| 请求 | 新增可选字段；对未知字段保持现有处理方式 | 新增必填字段；对旧客户端会发送的字段改成报错 |
| 事件 | 新事件类型；旧事件新增字段 | 删除事件类型；改变事件顺序语义 |
| 错误码 | 新错误码 | 改变已有错误码的 HTTP 状态或含义 |
| 启动参数 | 新参数（有默认值，不传时行为不变） | 删除或改名第 6 节列出的参数；改变默认行为 |
| 存储 | 见第 8 节 | 见第 8 节 |

注意 `decodeJSON` 对请求使用 `DisallowUnknownFields`：旧 K-brain 收到新 LiveAgent 多带的字段会返回 400。
所以 LiveAgent 只能在确认能力存在后才发送新字段，这条由 LiveAgent 遵守；K-brain 这边不得把现有
接口改成更严格。

## 6. 后端模式启动契约

LiveAgent 以如下方式启动后端，主版本 1 期间保持不变。标"新增"的是本约定要求新加的行为，其余为现状：

| 项目 | 内容 |
|---|---|
| 子命令 | `kn backend` |
| 参数 | LiveAgent 实际传入：`-listen`、`-data-dir`、`-parent-stdio`，有旧版桌面数据时加 `-legacy-desktop-dir`。`-token`、`-prepare-storage` 也属于本契约（不得删除或改名），但 LiveAgent 目前不传：令牌走环境变量，存储预检尚未使用 |
| 环境变量 | `K_BRAIN_BACKEND_TOKEN`、`LIVEAGENT_HOME`、`LIVEAGENT_KBRAIN_PROXY` |
| 就绪信号 | stdout 一行 `k-brain backend listening on http://127.0.0.1:<port>`。LiveAgent 只接受这个格式：主机必须是 `127.0.0.1`，不能带路径、查询或认证信息 |
| 父进程退出 | `-parent-stdio` 时 stdin EOF 即退出 |
| 版本查询 | `kn -version` 输出一行 `k-brain <发布版本>`（本地构建为 `k-brain dev`），在读取配置之前返回，不读写任何数据、不需要网络。自动更新在启动前用它做冒烟检查，格式不得改变 |
| 存储预检 | 现状：`kn backend -prepare-storage -data-dir <dir>` 打开存储，打印 `k-brain backend storage prepared` 后退出。新增：存储版本不可读时以退出码 `76` 退出、数据目录忙时以退出码 `75` 退出，并在 stderr 说明（见第 8 节） |

新增参数必须有默认值，不传时行为与旧版一致。

## 7. 发布产物契约

每个 tag 由 `.github/workflows/build.yml` 发布：

- 产物名：`k-brain-<os>-<arch>[.exe]`、`k-brain-computer-<os>-<arch>[.exe]`（与现有一致）。
- `SHA256SUMS`：覆盖全部产物，自动更新据此校验下载内容。现在的 workflow 用 `sha256sum k-brain-* > SHA256SUMS`，
  新增 `release-manifest.json` 时要把它一并纳入（例如 `sha256sum k-brain-* release-manifest.json > SHA256SUMS`）。
- 新增 `release-manifest.json`：

  ```json
  {
    "version": "v0.107.7",
    "revision": "<git sha>",
    "channel": "stable",
    "protocol": { "major": 1, "minor": 7 },
    "storage": { "version": 1, "minReader": 1, "migration": "none" },
    "capabilities": ["session_exists", "..."]
  }
  ```

  `storage.migration` 取值：`none`（不改存储格式）、`additive`（新增数据，旧版可读）、
  `breaking`（旧版不可读，见第 8 节）。自动更新不必启动二进制，就能据此判断是否兼容、是否需要用户确认。
- 频道：tag 带 `-` 的发布为预发布（`prerelease: true`），不带的为正式版并标为 latest。这条规则已在
  workflow 里实现，不得改变，LiveAgent 的频道过滤依赖它。

## 8. 存储版本与迁移

`~/.liveagent`（或 `-data-dir`）由 TUI 和后端共用，LiveAgent 的自动回退也可能让旧版 K-brain 打开新版写过的数据。

- 新增全局标记文件 `<data-dir>/kbrain-storage.json`：`{"version": N, "minReader": M}`。
  文件不存在视为 `{"version": 1, "minReader": 1}`。
- K-brain 的任何入口打开数据目录时（除 `kn -version` 外：TUI、`backend`、`acp`、`run`、`sessions`、`mcp` 等），
  若 `minReader` 大于自己能读的最高存储版本，拒绝打开数据并给出明确提示，**绝不尝试读写**：交互式入口打印到
  终端；后端模式在 `-prepare-storage` 和启动时以专用退出码 `76`（存储版本不可读）退出，并在 stderr 说明。
- 三种改动：
  1. 不改格式：版本不变。
  2. 加法（新文件、新字段，旧版会忽略）：`version` 加一，`minReader` 不变。旧版仍可读写。
  3. 破坏性（旧版读写会出错或丢数据）：`version` 加一，`minReader` 设为新值；迁移前把受影响的文件
     备份到 `<data-dir>/backups/storage-v<旧版本>-<时间>/`；在 `release-manifest.json` 标为 `breaking`。
     破坏性存储改动不要求升协议主版本。LiveAgent 的自动更新不会自动安装 `breaking` 版本，只有用户手动确认后才会安装
     （见 LiveAgent 设计文档），因为它会让用户自行安装的旧版 `kn`（TUI）打不开数据。
- 进程互斥：K-brain 每个打开数据目录的进程（同上一条的入口范围）都对 `<data-dir>/kbrain-storage.lock` 持有共享锁，
  退出时释放。破坏性迁移必须先拿到独占锁；拿不到就不迁移，也不读写数据：TUI 打印"另一个 K-brain 进程正在使用数据目录，
  请先关闭它"；后端模式以专用退出码 `75`（数据目录忙）退出，并在 stderr 说明。加法迁移不需要独占锁，但写入必须
  保证同时运行的旧版仍能读。
- 锁只对实现了它的 K-brain 有效：v0.107.6-beta.22 及以前的 `kn` 不持有 `kbrain-storage.lock`（现有的 `sessions/.lock`
  只在单次操作里加锁），新后端能直接拿到独占锁，而正在运行的旧 `kn` 不会再检查 `minReader`、会继续按旧格式写。
  因此：
  - 第一个 `breaking` 发布之前，带锁的 K-brain 必须已经作为正式版发布，并至少间隔一个正式版周期，让用户有机会
    升级手中的 `kn`。
  - 破坏性迁移前，后端还要检测是否有其他 K-brain 进程在运行（按进程名 `kn` / `k-brain`，排除自己与父进程启动的
    子进程）；检测到就同样以退出码 `75` 退出。这一步覆盖不持锁的旧 `kn`。
- 全局存储版本（`kbrain-storage.json`）的迁移只在启动时执行，不在运行中的进程里途中执行。子系统现有的按需迁移
  （例如记忆的旧项目目录在写入时搬迁，见 `internal/memory/legacy_project.go`）不受这一条约束，但它们只能是加法改动。
- 各子系统（会话、生图 WAL、记忆）自己的格式版本照旧保留，全局标记只记录"整个数据目录最低需要
  多新的 K-brain 才能安全打开"。

## 9. TUI 不受影响

- 为 LiveAgent 新增的接口、能力、参数只出现在后端模式（`kn backend`）与 HTTP 层，不改变 `kn`
  交互模式的参数、快捷键、默认行为与输出。
- 共享存储的改动同样受第 8 节约束：TUI 与后端模式读写同一份数据。具体保证：
  - 加法迁移后，用户 PATH 中较旧的 `kn` 仍能正常读写。
  - 破坏性迁移不会在用户不知情时发生：
    - LiveAgent 侧必须经用户确认（见 LiveAgent 设计文档第 6 节）。
    - 用户通过安装脚本或 `kn update` 装了新 `kn` 后，交互式 TUI 启动时如需破坏性迁移，先在终端说明影响与备份位置，
      用户输入确认后才迁移；非交互入口（`acp`、`run`、`mcp` 等）不迁移，拒绝打开并提示先交互运行一次 `kn`。
    - 其他 K-brain 进程（带锁的靠独占锁，不带锁的旧 `kn` 靠进程检测）正在运行时，迁移推迟，不改动它们正在用的数据。
  - 旧版 `kn` 遇到 `minReader` 更高的数据时拒绝打开，不会损坏数据。提示里给出能读这份数据的最低版本与对应的
    安装命令（`install.sh` 用 `K_BRAIN_VERSION=<tag>`，`install.ps1` 用 `-Version <tag>`）。`kn update` 现在只装
    GitHub latest（最新正式版），装不到预发布；新增：`kn update` 支持指定版本与预发布频道，之后提示改为对应的
    `kn update` 命令。
- 后端模式的自动更新状态放在后端数据根（默认 `~/.liveagent`，可被 `LIVEAGENT_HOME` / `K_BRAIN_HOME` 覆盖）下的
  `kbrain/`，K-brain 自身（含 TUI）不读写该目录；`kn update` 与
  `update.json` 的更新提示照旧只管理用户安装的 `kn`，与 LiveAgent 管理的二进制互不影响。
- `kn -version` 继续在读取配置之前返回，不创建数据目录。
- 新增 TUI 冒烟测试（启动、加载配置、读写会话、退出）并纳入 CI，任何 PR 都不得让它失败。`internal/tui`
  现有大量单元测试，但还没有覆盖整个启动与退出流程的冒烟测试。

## 10. 兼容测试

| 测试 | 位置 | 守护内容 |
|---|---|---|
| health 契约 | `internal/backend` | `version` 恒为 `kbrain.agent.v1`；`protocol`/`build`/`storage` 字段存在；登记表中的能力都存在且值不低于登记值 |
| 旧客户端回放 | `internal/backend` | 用 LiveAgent 已发布版本会发送的请求样例（存于 `internal/backend/testdata/la-contract/`）逐一调用，响应必须包含旧字段 |
| 启动契约 | `cmd/kn` | 第 6 节的参数、就绪信号、`-version` 不读写数据、`-prepare-storage` 遇到更高 `minReader` 时非零退出 |
| 存储标记 | `internal/...` | 不同 `version`/`minReader` 组合下各入口的打开/拒绝行为与退出码 76；带锁 `kn` 持锁时、不带锁的旧 `kn` 进程在运行时，破坏性迁移都推迟、后端退出码 75、数据不变；交互 TUI 迁移前要求确认，非交互入口拒绝 |
| TUI 冒烟（新增） | `internal/tui` | 见第 9 节 |

`internal/backend/testdata/la-contract/` 的样例由 LiveAgent 侧在每次发布（含预发布）时更新（见 LiveAgent 设计文档阶段一）。
旧客户端回放与 TUI 冒烟测试都在阶段一落地。

## 11. 升主版本的流程

1. 在本文登记要移除或改变的内容及理由，并开 issue 讨论。
2. LiveAgent 先发支持新主版本的版本（同时兼容旧主版本），用户升级后再发布 K-brain 新主版本。
3. K-brain 新主版本的 `release-manifest.json` 写明 `protocol.major`，旧 LiveAgent 的自动更新会跳过它。
4. 旧主版本的最后一个发布继续作为旧 LiveAgent 的可用版本，必要时在旧主版本分支上做安全修复。

## 12. 发版检查清单

- [ ] 本次改动全部属于第 5 节的"允许"列；否则已按第 11 节升主版本。
- [ ] 新能力已加入 `/v1/health` 与第 4.2 节登记表，`protocol.minor` 已递增。
- [ ] 存储改动已按第 8 节处理，`release-manifest.json` 的 `storage.migration` 正确。
- [ ] 兼容测试与 TUI 冒烟测试通过。
- [ ] `SHA256SUMS` 与 `release-manifest.json` 已随产物发布。
