# K-brain 兼容与独立发布契约

日期：2026-10-28。状态：兼容契约草案，尚未实施。本文与 [LiveAgent 更新设计（PR #1068）](https://github.com/Stack-Cairn/LiveAgent/pull/1068) 配套；LiveAgent 侧正文路径为 `docs/design/2026-10-26-kbrain-decoupled-updates.md`。本文定义 K-brain 向桌面端、Gateway、浏览器和独立 TUI 提供的兼容边界。

文中 JSON、命令、错误码和目录布局是拟新增规范。示例 tag `v0.200.0-example.1` 仅为说明，不是已发布版本。L0/K0 在首次落地发布时替换为真实 tag。

## 1. 兼容承诺与边界

K-brain 同时供独立 TUI、LiveAgent 桌面端、浏览器和 Gateway 使用。协议主版本内保留已经登记的请求、响应和事件语义。新客户端必须能够识别缺失能力，后端不能假设所有调用方已经升级。

独立发布承诺包括后端启动、HTTP/SSE、资产绑定和可验证存储状态。TUI 的键位、命令输出、配置默认值各有自己的回归要求。新增后端接口不能无意改变交互模式。

历史二进制不受新锁与存储检查控制。默认历史目录 G0 只允许旧版仍可安全读写的改动；需要破坏性格式时，使用新数据代际 G1。旧 `kn` 默认继续读写 G0，不会跟随 LiveAgent 自动改向 G1。选择新版 TUI 显式连接 G1 后，它必须遵守该代际的租约和格式检查。

| 名称 | 含义 | 能否独立变化 |
|---|---|---|
| 发布 tag / revision | 一组不可变二进制的身份 | 每次修复都可以发布 |
| releaseSequence | 候选排序的单调编号 | 每次发布递增，不能复用 |
| protocol.major | HTTP/SSE 兼容代际 | 破坏旧调用时升级 |
| protocol.minor | 协议内累计加法修订 | 仅供日志，不用于功能开关 |
| startupContract | 子命令、参数、就绪与退出约定 | 破坏启动方式时升级 |
| manifest schema | 发布清单的解析格式 | 未知 schema 不参与更新 |
| storage format / reader / writer | 数据格式及二进制可读写集合 | 与协议独立，按第 6 节处理 |
| capability | 具体功能的有类型契约 | 按登记的比较方式判断 |

## 2. Health、响应与实例

### 2.1 Health 示例

```json
{
  "status": "ok",
  "version": "kbrain.agent.v1",
  "protocol": {"name": "kbrain.agent", "major": 1, "minor": 8},
  "build": {
    "version": "v0.200.0-example.1",
    "revision": "1111111111111111111111111111111111111111",
    "releaseSequence": 1500,
    "channel": "prerelease"
  },
  "startupContract": 1,
  "instanceId": "c438bc67-0445-48c5-ad21-78403ad461ef",
  "capabilityRevision": 3,
  "storage": {
    "storageId": "store-example-g1",
    "generation": "g1-example",
    "format": 2,
    "minReader": 2,
    "minWriter": 2,
    "state": "ready"
  },
  "capabilities": {
    "session_queue": 1,
    "computer_session_approval": 1,
    "memory_switch": 1,
    "image_model_toggle": 1,
    "session_exists": 1,
    "session_changes": 2,
    "instance_precondition": 1,
    "backend_drain": 1,
    "permission_modes": ["normal", "plan", "auto_edit", "full_access", "always", "auto_review"],
    "image": {"supported": true, "version": "kbrain.image.v1"}
  }
}
```

health 需要后端令牌，但不需要模型凭据或外网。它不创建会话、不启动工具。启动尚未完成或迁移待恢复时，不得返回 `status: ok`。可提供不含凭据的结构化启动诊断，不能把坏数据目录伪装成已就绪。

主版本 1 中，旧 `version`、生图 `image.version` 和全部既有响应/SSE 的版本字段保持原值。`protocol` 与旧字段矛盾属于损坏契约。旧后端只有合法 v1 字段时，客户端可以采用旧解析路径；新后端发布测试必须要求完整新字段，不能靠旧兜底掩盖遗漏。

### 2.2 实例条件检查

`instanceId` 每次进程启动重新生成，不能用监听地址代替。`capabilityRevision` 在运行时可用状态发生变化时递增。静态能力的支持承诺不因某个配置失效而删除。

声明 `instance_precondition: 1` 的后端接受 `X-Kbrain-Instance` 请求头。带头请求必须在业务受理前检查：不匹配返回 HTTP 409、code `KB_INSTANCE_CHANGED`，不执行写入，不消耗受理 ID。请求已受理后只允许按原 ID 查询结果，不能借刷新实例重放操作。

旧客户端不带该头仍可工作，不能新增必填头。Gateway 对实例变化重新取 health 并下发能力快照。滚动部署若不能保证同一会话和数据所有者，必须禁止混版本副本。协议握手不替代会话路由与存储一致性。

## 3. 能力与行为兼容

### 3.1 登记规则

每项能力登记类型、比较方式、首次发布、可用条件及缺失行为。历史能力的整数 1 不递增，增强使用新名字；已经声明为单调整数的能力可以递增，但新值必须包含旧值的全部语义。

| 能力 | 类型与判断 | 缺失后的客户端约束 |
|---|---|---|
| session_queue | 历史常量，固定 1 | 不接管新后端队列；已有队列保留所有权 |
| computer_session_approval | 历史常量，固定 1 | 不显示会话始终允许，逐次确认 |
| memory_switch | 历史常量，固定 1 | 不发送新的开关字段，保留配置 |
| image_model_toggle | 历史常量，固定 1 | 不发送模型级生图开关，旧字段探测兼容保留 |
| session_exists | 历史常量，固定 1 | 回到既有迁移检查，不将缺失当不存在 |
| session_changes | 单调整数，当前 LA 消费 >= 2 | 禁用对应增量同步，使用旧刷新路径 |
| permission_modes | 字符串列表，按成员判断 | 不支持已保存模式时阻止执行，用户明确选择逐次确认 |
| image | 对象，supported 与固定版本字段 | 关闭相关 UI；可用性变化增加 capabilityRevision |
| instance_precondition | 新整数，>= 1 | 远程替换采用排空停机流程，限制混版本 |
| backend_drain | 新整数，>= 1 | 不在线热切换，留待停止所有写入者后启动 |

表中的历史值依据源码和当前客户端登记。首次发布 tag 必须由发布记录核实后加入机器可读登记表，不能猜测。新声明不得早于功能实际可用。健康接口可报告生图不可用，却仍保留它的协议结构。不要把运行时状态单调性与协议兼容性混为一谈。

### 3.2 主版本内的变更

| 内容 | 可接受的加法 | 必须拒绝或升级代际的变化 |
|---|---|---|
| 路径与请求 | 新路径，可选字段且旧默认行为保持 | 删除路径、新必填字段、改变参数语义 |
| 响应 | 客户端能忽略的新字段 | 删改字段或类型，降低已有完整性保证 |
| SSE | 新事件且旧客户端已测试可忽略 | 改 seq/终态/受理顺序，令旧解析器无法前进 |
| 枚举 | 仅在未知值处理已验证时新增 | 把新值当作默认发送给会拒绝它的旧客户端 |
| 错误 | 新行为的明确错误码 | 改已有错误的恢复语义，诱发重复写入 |
| 默认行为 | 已有行为保持，新增能力显式启用 | 自动放宽权限、改变旧队列所有者 |
| 存储 | 通过旧读改写保真和并发测试 | 仅因旧版能解码就声称可安全写回 |

当前服务端使用 `DisallowUnknownFields`。新客户端只在能力成立时发送新增字段，服务端不能通过“旧请求也能解析”单向证明兼容。新增事件类型必须跑真实旧客户端消费测试，不能仅检查响应包含旧字段。

## 4. 后端启动与排空契约

### 4.1 旧启动入口

继续支持 `kn backend -listen <address> -data-dir <dir> -parent-stdio`，可选 `-legacy-desktop-dir`。`-token` 与环境变量 `K_BRAIN_BACKEND_TOKEN` 保留。`LIVEAGENT_HOME`、兼容别名 `K_BRAIN_HOME` 和代理变量的既有优先级不能静默改变，LiveAgent 启动时显式传入目录与代理。

本地就绪 stdout 继续为 `k-brain backend listening on http://127.0.0.1:<port>`。远程监听遵守部署配置，不能为了本地解析规则强制所有远程实例只监听 loopback。`-parent-stdio` 收到 EOF 退出，退出前释放租约，不留下依赖 PID 清理的永久锁。

`kn -version` 仍只输出 `k-brain <发布版本>`，本地构建可为 dev，在读配置前返回。LiveAgent 先验证更新目录和发布清单的 minisign/Ed25519 签名，再按清单校验 backend 与 helper 的大小和 SHA-256，完成后才运行版本查询。这里不要求 Windows Authenticode 代码签名。发布资产中 backend 和 helper 必须来自同一个清单，两者自检和实际握手都需要验证。

现有 `backend -prepare-storage` 会打开、初始化存储，它不能作为只读规划入口。该行为保持兼容，但新版本在任何写入前执行格式和 journal 检查。不可读写、忙碌或需要迁移时明确退出，不擅自创建新格式。

### 4.2 排空协议

拟新增 `POST /v1/backend/drain`，由 `backend_drain` 能力保护。请求携带稳定的 `client_request_id`。同一 ID 重复调用返回同一排空事务；内容不同返回冲突。响应返回 drain ID 和状态，不需要模型凭据。

排空必须停止新 run、Cron 受理、队列出队、生图和后台写入作业，等待正在执行的操作到持久化安全点。队列已接受条目留在存储中，不重复接受。排空不取消已有工具外部副作用，也不承诺杀进程后模型执行能无缝继续。

拟新增 `GET /v1/backend/drain/<id>` 和 `POST /v1/backend/drain/<id>/cancel`。只有持有本地协调权的管理者可控制排空，不对普通 WebUI 暴露令牌。取消恢复原调度，重复取消幂等。默认等待三十秒后由 LiveAgent 取消切换。后端不自行强制终止任务。

跨进程 TUI 仍持共享租约时，托管后端的排空不能代替它退出。切换需要独占存储租约，拿不到返回忙碌。必须在前端显示具体占用，而不是把目录忙当作版本损坏。

新增可选 `backend -exclusive-start`，默认不传时保留普通启动行为。托管切换先排空并停止旧后端，新后端拿独占租约后重检格式与 journal，再降为共享租约，最后报就绪。平台无法原子降级时重新获取共享租约并重检修订。发现竞争变更则中止，不能留下无租约的数据访问窗口。TUI 抢先占用导致新候选返回 75 时，协调者取消切换，可按原组合重新启动旧后端，不把忙碌标为坏版本。

## 5. 只读存储规划接口

拟新增 `kn storage plan -data-dir <source> -target-dir <target> -target-manifest <verified-manifest> -format json`。输入已经是快照时，用 `-snapshot <path>` 替代 `-data-dir`，两种输入互斥。调用者先验证清单签名，规划器检查清单与自身 build 身份一致，再把其摘要绑定计划。解析命令必须在任何配置保存、存储初始化和后台更新检查之前分派。它只读标记及元数据，不创建目录、锁文件、缓存、模型客户端或凭据文件。需要协调读取时使用已有只读句柄，不能为规划顺便修复数据。

规划命令在 stdin 非交互、无模型凭据、无外网时也应可用。stdout 只输出一份 JSON，stderr 记录脱敏诊断。语法错误和未知 schema 拒绝，不把失败输出当作空计划。

```json
{
  "schema": 1,
  "planId": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
  "source": {
    "storageId": "store-example-g0",
    "generation": "legacy",
    "format": 1,
    "revision": 41,
    "legacyWritersPossible": true
  },
  "target": {
    "generation": "g1-example",
    "format": 2,
    "minReader": 2,
    "minWriter": 2
  },
  "action": "new_generation_import",
  "steps": ["import_snapshot_v1", "upgrade_1_to_2", "validate_v2"],
  "requiresConsent": true,
  "requiresExclusive": true,
  "requiresSnapshot": true,
  "sameGenerationBinaryRollback": false,
  "reasonCodes": ["LEGACY_WRITERS_UNGUARDED", "STORAGE_REQUIREMENTS_INCREASE"]
}
```

`requiresExclusive` 在此例指目标目录；源快照需另有可证明的一致性边界。源没有新式 revision 时返回 `revision: null` 并说明来源，不能制造数字证明一致性。真正执行导入的确认以不可变快照摘要取代未知源修订。

`action` 的集合为 `use_as_is`、`compatible_prepare`、`new_generation_import`、`recover_required`、`unsupported`。只有前两种且读写条件成立才允许普通切换。`compatible_prepare` 必须保持历史写入者安全，不代表可以自动原地提高最低读写要求。

计划 ID 绑定源存储身份、源修订或快照摘要、目标格式、迁移步骤和目标清单摘要。规划可以在快照前说明所需条件，但此时不能授权执行。快照准备后再生成可执行计划，LiveAgent 展示并确认最终计划。执行时重新核对输入与租约，不符则要求重新规划。

格式 1 跳到格式 3 时，即使目标发布相对前一版没有存储变化，仍检查全部路径 1→2→3。禁止只看目标 release 本次是否“breaking”。缺少任何迁移边就返回 unsupported，不自行重建数据。

## 6. 存储读写、快照与恢复

### 6.1 标记与二进制声明

新管理目录使用以下标记。它是存储事实，不是二进制兼容范围的替代品。

```json
{
  "schema": 1,
  "storageId": "store-example-g1",
  "generation": "g1-example",
  "format": 2,
  "minReader": 2,
  "minWriter": 2,
  "revision": 42,
  "state": "ready"
}
```

文件名为 `<data-dir>/kbrain-storage.json`。缺失时按 legacy 目录处理，不证明没有其他进程，不重置未知的新目录。损坏、重复键或不支持的 schema 阻止打开，不把损坏标记删除后按格式 1 继续。

二进制清单分别声明 `readFormats`、`writeFormats` 与 `targetFormat`。格式编号按兼容级别递增，必须同时满足 reader/writer 门槛及明确集合。例如二进制能写 2，不能据此推导能安全写 1 或 3。服务端正常启动属于读写打开，即使用户只准备查看历史，也不能用读权限绕过写入门禁。

每个已支持入口在任何业务数据访问前检查标记和 journal，包括 TUI、backend、acp、run、sessions、mcp、脚本及管道入口。`-version` 与只读规划不初始化数据。新增显式 TUI 参数 `kn -data-dir <dir>`，作用仅为选择数据目录，不改变未传参数时的 G0 默认值。

### 6.2 兼容加法的标准

加法必须满足旧版本读取、修改并写回后，新数据和语义不丢失。配置、会话、记忆、生图 WAL、队列与自动化都要独立验证。旧版会忽略未知字段，却在序列化时删掉它，不属于安全加法。

优先把新数据放在旧程序不触碰的独立文件；能保留扩展字段的格式可继续共享。给新版本补保留逻辑不能改变已经发布的旧程序，仍须测最老支持写入者。不满足标准的改动使用新代际，不能仅提高 G0 标记来阻止未知老程序。

### 6.3 租约与操作锁

实现本契约的进程持有 `<data-dir>/kbrain-storage.lock` 共享租约至退出。普通单次写入继续使用各子系统操作锁和事务。共享租约不串行化普通业务写入。所有写入者更新控制修订，快照与规划不能使用只统计部分文件的修订。

兼容准备、快照导出和导入需要各自声明的独占边界。跨进程锁顺序遵守 LiveAgent 协调锁、存储租约、子系统操作锁。升级共享租约为独占时必须释放后重新检查状态，不在锁升级竞争中死等。独占失败返回 75，无业务数据写入。

旧进程扫描仅用于提示，不能作为安全放行依据。NFS/SMB 等未验证租约语义的文件系统禁用托管切换。跨平台进程锁及原子替换由真实平台测试证明。

### 6.4 快照输入

导出器覆盖全部受迁移影响的子系统，记录源格式、存储身份、修订、文件摘要与业务一致性结果。源格式没有全局锁时，必须使用各子系统已有的可证明一致性机制或要求离线；没有机制就不提供该格式的自动迁移。

停止已知进程、比较文件时间、扫描进程名，都不能证明没有未知历史写入者。人工离线流程需要用户停止全部源写入者，工具仍需验证快照内的数据一致性。无法验证的导出不能标为可迁移，只能保留作人工恢复材料。

输入快照建立后保持不可变，包含完整性清单，迁移只读取快照，不能再按需读取活跃 G0 的文件。导入后保留源目录和快照，新版在 G1 的后续变化不自动回写 G0。

### 6.5 迁移 journal

拟新增 `kn storage import -snapshot <path> -target-dir <new-dir> -target-manifest <verified-manifest> -plan-id <id> -approval-file <file>` 和 `kn storage recover -data-dir <dir> -format json`。approval 文件只提供绑定输入的用户确认记录，不是认证凭据。目录权限与控制权仍须校验。它不得包含模型密钥。恢复程序仅信任先前验签并绑定在 journal 的迁移实现和输入，不执行数据目录里提供的脚本。

| 阶段 | 先持久化的内容 | 中断后的允许动作 |
|---|---|---|
| created | 目标身份、计划 ID、输入快照摘要、state=migrating | 普通入口拒绝，仅恢复入口可继续 |
| importing | 当前步骤、输入摘要、临时输出位置 | 以幂等步骤重做或继续，不暴露半份目标 |
| validating | 子系统与跨文件一致性结果 | 失败保留证据，目标仍不可用 |
| committed | ready 标记与最终格式，journal 的提交记录 | 校验提交记录后允许打开 |
| recovered | 恢复选择及校验结果 | 仅与已提交格式相符的二进制可打开 |

每步写临时文件后同步，再原子替换，journal 与标记不在同一个事务时定义可判定的先后顺序。目标有 committed journal 但标记未写完，恢复入口完成提交；标记 ready 但无有效提交记录，普通入口仍拒绝。目录 fsync、Windows 替换与锁冲突由平台实现负责，不能假设 rename 等于断电持久化。

失败恢复优先从同一快照前滚，不能删除 journal 强行回退。放弃目标必须经用户单独操作，且不删除源或快照。当前任务不执行任何真实迁移或清理。

### 6.6 退出与 HTTP 错误

| 退出码 | 稳定含义 | LiveAgent 行为 |
|---|---|---|
| 75 | `KB_STORAGE_BUSY`，租约或目录正被占用 | 提示占用、可重试，不记版本坏 |
| 76 | `KB_STORAGE_INCOMPATIBLE`，不能安全读写该格式 | 禁止此候选，给出版本/代际信息 |
| 77 | `KB_MIGRATION_INCOMPLETE`，journal 需恢复 | 阻止普通启动，进入恢复界面 |
| 78 | `KB_MIGRATION_APPROVAL_REQUIRED`，需新计划确认 | 不迁移，不自动回退到另一代际 |

这些值是新增的项目约定，不宣称已经被全部平台和 CLI 使用。后端、交互及非交互入口保持同一语义；交互 TUI 可提示，但不在读取了不兼容数据后才提问。HTTP 以稳定 code 表示相同类别，状态分别用 409、409、503、409，客户端按 code 处理，不只按状态码猜测。

## 7. 签名发布契约

本节签名是 K-brain 发布方用项目私钥签发更新元数据，由调用方内置公钥验证。`channels.sig` 验证更新目录 `channels.json`，`release-manifest.sig` 验证发布清单 `release-manifest.json`；backend 与 computer helper 按已验签清单中的文件大小和 SHA-256 校验。

这套发布元数据签名与自动验签流程是拟新增契约，当前尚未实现。它不依赖 Windows Authenticode 代码签名，Windows EXE 和安装包可以不带该代码签名。更新器验签成功不保证消除 SmartScreen、未知发布者或安全软件提示，也不替代操作系统执行许可与平台安装验收。

### 7.1 不可变清单

`release-manifest.json` 使用独立 detached 签名文件 `release-manifest.sig`。采用 minisign 的 Ed25519 格式，Rust 使用 `minisign-verify`，Go 侧 `kn update` 选用兼容 minisign 格式的成熟实现并通过相同测试向量。验签对原始字节进行，解析器拒绝重复键。清单上限 256 KiB，频道目录上限 8 MiB，签名封装上限 8 KiB。以下是单平台最小示例，正式发布需列出所有支持的平台资产。

```json
{
  "schema": 1,
  "repository": "Stack-Cairn/K-brain",
  "kind": "release-manifest",
  "version": "v0.200.0-example.1",
  "revision": "1111111111111111111111111111111111111111",
  "releaseSequence": 1500,
  "channel": "prerelease",
  "protocol": {"name": "kbrain.agent", "major": 1, "minor": 8},
  "startupContract": 1,
  "storage": {
    "readFormats": [1, 2],
    "writeFormats": [1, 2],
    "targetFormat": 2,
    "migrationEdges": [{"from": 1, "to": 2, "mode": "new_generation_import"}]
  },
  "capabilitySchema": 1,
  "capabilities": {"session_exists": 1, "session_changes": 2, "instance_precondition": 1, "backend_drain": 1},
  "platforms": {
    "x86_64-pc-windows-msvc": {
      "osRequirements": {"windowsMinBuild": 19045},
      "backend": {"name": "k-brain-windows-x64.exe", "size": 100000, "sha256": "3333333333333333333333333333333333333333333333333333333333333333"},
      "computer": {"name": "k-brain-computer-windows-x64.exe", "size": 200000, "sha256": "4444444444444444444444444444444444444444444444444444444444444444"}
    }
  }
}
```

示例的大小、摘要和最低系统版本不是实际发布测量。正式值必须来自构建产物和工具链要求。其他平台使用同一 schema 登记最低 macOS 版本或 Linux ABI 要求，不能因为 GOOS/GOARCH 相同就认为可执行。

`targetFormat` 是新建或导入的目标；已有兼容目录可以保持原格式，不能每次启动强制提升。`migrationEdges` 是二进制具备的迁移实现摘要，本机完整计划仍由实际数据决定。能力清单只说明静态实现，运行可用性以 health 为准。正式清单应完整列出能力登记，不把本示例的子集当成全表。

相同清单绑定 backend/helper 的精确大小与摘要。版本查询与实际 health 必须匹配 tag、revision、协议、启动契约。helper 通过隔离握手测试。资产可继续用现有名称，安装到独立目录后保留同目录寻找 helper 的约定，不从 PATH 意外借用另一个版本。

### 7.2 签名频道目录

全仓库只有一条单调目录序号，频道在候选记录中区分。`channels.json` 与 `channels.sig` 发布于固定官方入口，收录各受支持协议线的全部当前可用候选。受支持历史候选不得因最新发布协议变化而丢失。

```json
{
  "schema": 1,
  "repository": "Stack-Cairn/K-brain",
  "kind": "release-catalog",
  "sequence": 420,
  "issuedAt": "2026-10-28T12:00:00Z",
  "expiresAt": "2026-11-04T12:00:00Z",
  "releases": [{
    "version": "v0.200.0-example.1",
    "releaseSequence": 1500,
    "channel": "prerelease",
    "manifestSha256": "5555555555555555555555555555555555555555555555555555555555555555"
  }],
  "withdrawn": []
}
```

发布方至少每七天更新目录或延长有效期，并在到期前完成续签。新发布、撤回和续签都递增 sequence。发布顺序是先完成全部资产与清单签名上传、核对可下载，再发布新签名目录。不能把半发布版本放入目录。

目录中的撤回条目含清单摘要、原因、是否安全撤回及时间。相同 releaseSequence、同 tag 不允许对应另一份清单。若发布错误，撤回并发新 tag。不能覆盖原资产修复。

客户端内置专用 key ID 和公钥，签名封装携带 key ID，初期轮换只通过 LiveAgent 信任配置发版完成。解析后还要核对 repository 与 kind，不能用另一类合法签名文件代替预期元数据。跨语言测试向量在 S3 固定，禁止临时自创密码协议。签名不能证明运行兼容，也不能保护已经泄露的私钥，CI 仍需最小权限与受保护签名步骤。

### 7.3 维护与频道

延续 tag 含 `-` 表示 prerelease 的发布约定，签名清单与 GitHub 标志不一致时不发布。stable 客户端仅选 stable，prerelease 客户端可选全部合格版本，自动选择不降到更低 releaseSequence。切换到更旧固定版本是显式操作，仍需格式检查。

协议主版本维护最多两条线，新主版本正式发布后旧线至少维护十二个月，终止前九十天公告。旧线安全修复使用新的发布序号进入目录。已登记的旧契约在该主版本内一直保持，不能因某客户端停止更新就静默移除。

## 8. TUI、非交互入口和远程部署

TUI 安装目录和 LiveAgent artifacts 分离。LiveAgent 不调用 `kn update`，不改用户 PATH。`kn update` 可以新增指定版本和频道，但这属于独立实现，当前只会调用 latest 安装器的行为不能假装已有版本选择。

用户需要升级 TUI 时给出确定 tag 的安装命令，安装器也应验证同一签名契约。后端升级流程不执行来自 release 的 shell/PowerShell 命令。界面展示的命令由内置模板和严格校验 tag 生成。

交互与非交互由实际终端状态及命令语义共同判断，不能仅按子命令名字分类。管道、acp、mcp、run 和无人值守 backend 都不能自行打开确认界面。缺少有效批准时返回 78；交互 TUI 也先完成规划，再经确认进入独立迁移工具，普通打开不顺便迁移。

远程服务默认由部署者固定 digest 或清单摘要，不由 Gateway 下载。升级前排空、确认存储组合，再换完整 backend/helper 组合。多个副本不能同时持有未经设计的共享自动化存储。如需多实例服务，先实现会话路由和服务端所有权，不从“有共享锁”推导集群安全。

## 9. 测试与发布门禁

每项用例均需要隔离目录、故障注入和真实旧版本 fixture。以下是新增测试要求，本 PR 未执行。

| ID / 类型 | 触发与步骤 | 预期结果 | 对应 LA 验收 |
|---|---|---|---|
| K01 AUTO | 运行冻结 health/JSON/SSE 契约及真实旧桌面/Gateway 消费器 | 字段与事件语义保持；冻结能力数值不变 | A01/A03/A05 |
| K02 AUTO | 给旧后端发送不具能力的新字段；对新后端移除运行时能力 | 前者明确 400；新客户端不发送未知字段，运行修订更新 | A02/A26 |
| K03 AUTO | 已保存审查模式连接缺该模式后端，再调用 run | 未受理执行，客户端提示；不能改为更宽松权限 | A04 |
| K04 AUTO | 各入口遇到损坏标记、更高 minWriter、journal 未完成 | 数据未访问或改写，退出码及错误 code 一致 | A14/A17/A24 |
| K05 AUTO | 对不存在目录运行 -version 和 storage plan，监测文件、网络和模型初始化 | 无写入、无联网、无凭据要求；JSON 可解析 | A07/A18 |
| K06 AUTO | 新写配置/cron/会话/记忆/生图，旧读改写，再新读 | 保留全部数据及语义；不能保真的格式禁止共享升级 | A13 |
| K07 AUTO | A→B 为破坏迁移，C 无新增迁移，从 A 直接规划 C | 返回完整步骤和新代际要求，不漏确认 | A07 |
| K08 AUTO | 旧 kn 在 G0 继续运行，导入快照到 G1，再分别写入 | 目标与源隔离，源未被迁移工具修改，无隐式同步 | A14/A15 |
| K09 AUTO/OPS | 源子系统无法建立一致性边界，尝试生成快照 | 拒绝可迁移导出；明确缺失条件 | A16 |
| K10 AUTO | 对 journal 每一持久化边界注入 kill、磁盘满、部分替换 | 普通入口拒绝半迁移目录；重复恢复结果一致 | A17 |
| K11 AUTO | 快照或计划在批准后变化，重复 import 和 recover | 旧批准拒绝；相同合法步骤幂等，无源写入 | A18 |
| K12 AUTO | drain 期间并发所有生产者，重复 drain/cancel，TUI 持租约 | 新受理停止，已有任务安全结束；占用返回 75 | A19/A20 |
| K13 AUTO | 新旧实例在探测后替换，携带旧实例头写入 | 受理前拒绝 409，原 request ID 未执行 | A25 |
| K14 AUTO | 篡改清单/目录/两类资产，回放序号，替换同 tag 资产 | 发布或客户端验证拒绝，在新代码执行前发现 | A09/A10/A11 |
| K15 AUTO/OPS | 四个平台构建及安装布局验证，helper 隔离握手与系统权限检查 | 资产绑定正确，可执行；不借 PATH 旧 helper | A12/A27 |
| K16 AUTO | 跨进程租约竞争，别名路径，所有者崩溃；网络文件系统 | 互斥与恢复正确；不支持文件系统明确拒绝 | A21 |
| K17 AUTO | 假健康但业务失败、ACK 丢失、同代际与跨代际回退 | 不自动重放写请求；不兼容组合不开放数据 | A22/A23/A24 |
| K18 AUTO/OPS | TUI 启动、读配置、会话读写、退出；管道与 acp/mcp/run 需要迁移 | 正常行为不变；非交互明确退出，无挂起确认 | A01/A15 |

测试记录应写实际命令、fixture tag/revision、系统版本和结果。K-brain 的 `go test ./...` 与 race 检查、LiveAgent 两端 build/lint/test 和 Mirror Check 是实施时的验证入口，不能由本文表格替代。跨仓库契约 fixture 以不可变摘要复制，不在 CI 中写对方仓库或依赖未固定的 main。

### 9.1 发版顺序

1. K-brain 合入新契约与兼容测试，发布 K0；LiveAgent 再发布支持该契约的 L0，更新器保持关闭。
2. K-brain 完成存储保护与签名发布；LiveAgent 内置信任材料、打包映射和状态机，通过平台验收。
3. 普通兼容更新先开放，完整迁移和实例协议放行后再启用对应路径。支持窗口、旧协议维护人和签名值守人必须落实。
4. 发布前运行全部相关门禁，上传不可变资产、清单和签名，最后递增签名目录。更新旧协议线也遵守同一流程。
5. 发现回归先签名撤回并记录影响，修复发新版本；安全问题保留受影响组合与可恢复路径，不要求客户端盲目降级。

## 10. 源码依据与适用限制

本草稿对旧实现的描述固定于 K-brain PR #59 的 `ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc`。它尚未实现本文新增命令与标记。当前 LA 的 `session_changes >= 2` 消费来自 `dd5d41b5a527ded45dde8c1f22fc283135882112`，不据此推断旧 KB 提交具备该能力。

| 依据 | 已核实事实 |
|---|---|
| [cmd/kn/main.go:57](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/cmd/kn/main.go#L57) | -version 在主要命令分派前返回 |
| [cmd/kn/backend.go:31](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/cmd/kn/backend.go#L31) | 存在 parent-stdio、data-dir、prepare-storage 参数 |
| [internal/backend/server.go:317](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/backend/server.go#L317) | health 仍以旧版本和历史能力表示响应 |
| [internal/backend/server.go:1809](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/backend/server.go#L1809) | 请求解码拒绝未知字段 |
| [internal/config/config.go:629](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/config/config.go#L629) | 配置按 typed Config 重建序列化，不普遍保留未知字段 |
| [internal/backend/cron_store.go:37](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/backend/cron_store.go#L37) | cron 解码到固定结构，再整体写回 |
| [internal/session/session.go:210](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/session/session.go#L210) | 既有会话锁限于单次操作，不是全数据目录租约 |
| [.github/workflows/build.yml:126](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/.github/workflows/build.yml#L126) | 发布生成 SHA256SUMS，本文签名目录仍需新增 |
| [internal/computer/helper_path.go:15](https://github.com/Stack-Cairn/K-brain/blob/ef904bbf56391d5ae7e90d220f8d2c0cdb68f2bc/internal/computer/helper_path.go#L15) | helper 先找同目录，另有环境变量与 PATH 兜底 |

Go 侧验签实现与平台持久化实现需要在实施 PR 中固定版本并通过测试向量。没有这些证据就不开放自动更新。本文没有消除旧二进制的未知行为，也没有把快照校验和等同于业务一致性证明。平台打包、macOS helper 权限和安全软件拦截需要真实安装验收。
