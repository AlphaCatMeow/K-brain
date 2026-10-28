# 会话摘要同步实现验证记录

> 状态：本地实现与真实自动化已完成（2026-10-28），最终开发产物为 final4；PR #60 的 CI 修复提交 `b4f5508` 已通过三平台检查，后续仅证据文档更新仍需核对最终 head 的检查。本文区分模块、真机、性能及基线失败，不把运行中或环境失败写成通过。

## 基线与产物

- 上游基线：`origin/main`，`0dad9fdec4adbb2a6324974edc1d610c69b2a79b`。
- 任务分支：`feat/session-sync-resources`，工作树 `D:/Documents/Projects/Go/K-brain-worktrees/session-sync-resources`。
- 主检出 `D:/Documents/Projects/Go/K-brain` 原有未提交改动未读取、未修改、未纳入任务。
- 基线二进制：`%TEMP%/kbrain-session-sync-baseline-0dad9fd/kn.exe`。
- 最终开发联调二进制：`%TEMP%/kbrain-session-sync-implementation-0dad9fd-final4/kn.exe`，SHA-256 `16A864EB2ADF697AC114763BB38DFF62E5E1309818B85CF0F2A518D76640E174`；同目录 computer SHA-256 `1A792849F7F29CB7B5EEFC4D4D04BAF94052F4E877741A473F1F95458D93E628`。
- 开发生产源码清单 SHA-256：`27143745D5D2BC7F60BFF8E60901B5FB6D4D4A198BC293223768BAF0B13F3CED`，版本 `0dad9fd-session-sync-27143745D5D2`。该清单仅包含相对上述基线新增或修改的生产 Go 文件，其余源码由完整基线 SHA 定义；不包含后续文档与测试修订。`kbrain-artifact.json` 明确标记为 development override。
- 此前 B7228F 构建保留在独立 `final` 目录；final2 是只修快照构建预约/关闭竞态的中间候选（backend SHA-256 `AAB751F5D9C830A7599BD50EF4267F8593350C559E15FB4ACD423F437250027B`），final3 另补用量嵌套指针深拷贝（backend SHA-256 `225E53F9D7D94EA1BFA5AA7BF551B48621AF1C9182E909E081698933698D8818`），final4 再补空快照页的非 null 数组。没有覆盖任何旧目录或正在联调的二进制。r4 的热缓存测量只代表当时路径，最终组合仍须重新完成真实联调与资源采样。
- 上述是未提交源码的开发产物，不是发布版本，不用于更新正式锁文件。

## 需求与测试映射

| 设计项 | 实现/验证证据 | 当前结果 |
|---|---|---|
| A01 热缓存 | `TestSummaryCacheAvoidsTranscriptReadsWhenFilesAreUnchanged`，read observer 断言 cold=8、warm=0 | 通过 |
| A02 写入入口 | `TestSummaryCacheCoversEveryWriteEntryPoint` 逐项执行 create、Save、ImportHistory、SetSnapshot、SaveTask、SetTitle、project-scoped SetCWD、Delete，并断言后续列表不重读 transcript；`write` 成功后同锁更新摘要 | 通过；消费者地图已核对 |
| A03 返回值隔离 | `cloneMeta` 深拷贝 Tags/SubUsage/ModelUsage；`TestSummaryReviewUsageDetailsDoNotAliasReturnedMetadata` 另覆盖两个用量 map 的 `PromptTokensDetails` 嵌套指针，通过修改 Page、Snapshot、Changes 返回值并重读/重放证明隔离 | 通过；嵌套指针首败与修复见下文 |
| A04 外部写入/扫描失败 | `TestSummaryCacheDetectsExternalWriteAndDelete`、`TestSummaryScanFailureDoesNotPublishPartialState`、`TestSummaryColdReadRetriesAnUnstableExternalFile`、`TestSummaryScannerFailureWakesLongPoll` | 通过 |
| A05 冷启动顺序 | `TestSummaryChangesFirstInitializesWithoutSyntheticChanges`；Open/Create/Save 不初始化 HTTP 同步状态；`TestSessionSnapshotEmptyPageReturnsAnArray` 真实 HTTP 验证空库快照页 `sessions:[]`、`next_offset:null` | 通过；空页 null 首败已修 |
| A06 跨页快照 | `TestSessionSnapshotIsImmutableAndChangesResumeFromItsRevision`，真实 HTTP snapshot+分页+并发 mutation+catch-up | 通过 |
| A07 快照失效 | epoch/minSince 校验及 410；`TestSummaryTombstonePruningRaisesFloor`、`TestSessionSnapshotExpiryReturnsGoneAndReleasesBudget` | 通过 |
| A08 快照资源 | 每认证后端最多 4 份（含在途），合计 64MiB、单构建者、60 秒 timer；`TestSessionSnapshotBudgetAndIdempotentDelete`、`TestSessionSnapshotCloseReleasesResources`、`TestSessionSnapshotInFlightCannotSurviveServerClose`、`TestSessionSnapshotInFlightBudgetAndCancellation`、`TestSessionSnapshotFullBudgetRejectsBeforeStoreWork`、`TestSessionSnapshotFailedBuildReleasesAdmission`、`TestSummarySubscriptionResourcesAreReleased` | 通过；关闭竞态首败与修复复测见下文 |
| A09 增量分批 | 既有 `TestSummaryChangesBatchCursorNeverJumpsToHead` 覆盖 601 条；独立 `TestSummaryReviewThreeBatchesConvergeAfterConcurrentChanges` 覆盖 1,201 条、500/500/剩余三批，翻批期间同时修改已交付与未交付 ID、新增及删除，并比较最终完整投影 | 通过 |
| A10 游标边界 | uint64 字符串解析、`TestSummaryChangesFutureCursorResets`、`TestSummaryRevisionOverflowStartsNewEpochWithoutWrap`、HTTP 大于 2^53 | 通过 |
| A11 合并/超时 | Store 级固定窗口 timer；`TestSummaryChangesCoalesceCursorAndReset`、`TestSummaryChangesCoalescesWaiterWakeups`（50 writes，waiter 循环不超过 3） | 通过 |
| A12 等待竞态 | `TestSummaryChangesCancellationReleasesWaiter`、`TestSummaryChangesCaughtUpPollDoesNotSpin`、`TestSummaryChangesLimitsWaitersAndRescans`、`TestSummarySubscriptionResourcesAreReleased`；每认证后端 4 waiter、rescan 限频 | 通过 |
| A13 删除回收 | 每次写入立即按 10,000/24h 修剪 tombstone，并推进 minSince；`TestSummaryTombstonePruningRaisesFloor` 覆盖 TTL，`TestSummaryReviewTombstoneCapacityPreservesFloor` 构造 10,005 个删除，断言仅留 10,000、floor 以下 reset、等于 floor 可续传 | 通过 |
| A14 投影等价 | 既有 `page_test.go` 过滤/游标测试；新增 pinned/workdirs HTTP 测试 | 通过 |
| A22 权限 | 新路由继续使用 Server Bearer 鉴权；`TestSessionChangesRejectUnauthorizedAndInvalidInputs` | 通过（单 token 全量投影边界） |

## 独立 TUI、ACP 与 CLI

- `TestTUIStoreDoesNotStartSummaryBackgroundTasks` 覆盖 Open/Create/Save/List/SetTitle/Close。允许惰性摘要缓存，但断言 `scannerStop=nil`、`publishTimer=nil`、`waiters=0`、HTTP changes map 为空；`-count=20` 全部通过。
- HTTP changes 激活时，共用一个 scanner 和一个固定窗口 timer；90 秒租约后停止 scanner，Store.Close 停止 scanner/timer。
- 会话文件格式未修改，没有新增磁盘索引。
- `go test ./internal/acp -run 'Test(ListSessions|SessionListWirePaginationAndProjectFilter)$' -count=20`：通过。
- `go test ./cmd/kn -run 'TestSessionsCLI' -count=20`：通过。
- 真实 PTY 自动化已通过创建/保存、改名、fork、archive、resume、退出/重启和 CLI 删除。取消语义核对：忙碌时 Ctrl+C 设计为双击取消，单击只武装；现有 `TestCtrlCBusyInterruptsNotQuits` 覆盖该约定。最终 PTY 双 Ctrl+C 复测由自动化报告补录。

## 版本兼容与发布边界

- K-brain `main` 相对 LiveAgent v2 当前锁定提交 `60e614f07c600b7aa5c0acbb9631d708e33dbb8b` 保留 `version: kbrain.agent.v1`、原 `/v1/sessions` 字段和 transcript v1 格式；新列表字段为 JSON 加法，旧 LiveAgent 会忽略。
- 未探测 `capabilities.session_changes >= 2` 的客户端不会调用新路由。旧 LiveAgent + 新 K-brain 只获得服务端摘要缓存带来的列表提速，既有轮询和 TUI 行为保持。
- 新 LiveAgent 在旧 K-brain 或不完整能力下保留旧轮询，不把单一路由成功当作完整能力。
- `kn backend` 的既有启动参数与 `requiredBackendFlags` 没有修改；独立 TUI、ACP、`kn run` 不依赖 HTTP changes。
- 当前只有开发联调二进制，未创建 release、tag、正式下载地址或锁文件哈希。LiveAgent PR 必须明确依赖尚未发布的 K-brain PR；在真实发布产物存在前，不能更新正式 `kbrain.lock.json`，也不能宣称发行组合已经完成。

## 跨平台 CI

新增 `.github/workflows/session-sync-tests.yml`，弥补当前仓库只有 tag release、memory 和 tool 专项检查而没有 session/backend PR 检查的问题。工作流只在相关路径变更时运行：

- `ubuntu-latest` + `linux-amd64`
- `windows-latest` + `windows-amd64`
- `macos-15` + `darwin-arm64`

各平台先用 `scripts/prepare-cua.py --target <platform-arch>` 下载、校验并嵌入锁定的真实 CUA runtime，再执行改动文件 gofmt、`go build ./...`、受影响包 vet、session/protocol、新 backend HTTP、ACP session/list、`kn sessions` 和 TUI 双 Ctrl+C 定向测试。Linux 额外执行 `go test ./...` 和新增 session/backend race30。没有放入占位 runtime，也没有修改发布 workflow。

## 构建、格式、vet 与并发

- 系统 Go：`go version go1.27.3 windows/amd64`，符合 `go.mod` 的 Go 1.27 要求。
- `go build ./...`：通过。
- 改动文件 LF 副本逐文件 `gofmt -l`：无输出。
- `go vet ./internal/session ./internal/backend ./internal/protocol`：通过。
- `git diff --check`：通过（Git 仅提示工作树 CRLF/LF 归一化，无空白错误）。
- `go test -race ./internal/session -run 'Test(Summary|TUIStore)' -count=30`：通过，26.433 秒。
- `go test -race ./internal/backend -run 'TestSession(Snapshot|Changes)' -count=30`：通过，5.411 秒。
- final2 复核修复后执行 `go test -race ./internal/session ./internal/backend -run '^Test(Summary|TUIStore|SessionSnapshot|SessionChanges)' -count=30 -timeout=120s`：通过，session 41.336 秒、backend 8.638 秒；覆盖新增四个 admission/关闭回归与 1,201 条三批用例，无 DATA RACE。日志 `%TEMP%/kbrain-session-sync-review-race30.log`。
- final3 深拷贝修复后，相同 race30 命令再次通过，session 50.801 秒、backend 8.260 秒；包含嵌套隔离、1,201 条三批和 10,005 个 tombstone 边界，无 DATA RACE。日志 `%TEMP%/kbrain-session-sync-review-final3-race30.log`。独立 `TestSummaryReview` 子集 `-count=20` 通过，2.115 秒；`go build ./...` 与受影响包 `go vet` 再次通过。
- final4 空页修复后，`go test -race ./internal/backend -run '^TestSession(Snapshot|Changes)' -count=30 -timeout=60s` 通过，7.956 秒；`go build ./...` 与受影响包 `go vet` 再次通过。此次只修改分页空数组构造，session 生产源码与 final3 race30 对象一致。
- 早一轮重点并发子集 race30 同样通过；最终 race30 覆盖全部新增 summary/TUI 和 backend HTTP 测试。
- `kb-full-tests.sh` 无法在本机执行：系统的 `bash` 解析到缺少 `/bin/bash` 的 WSL。已按脚本内容使用系统 Go 逐项执行等价检查；改动包全量 race10 的实际结果见下文。

改动包 race10 首轮没有通过，但没有出现 DATA RACE：`internal/backend/session_sync_test.go` 漏导入 `time`，`TestSummarySubscriptionResourcesAreReleased` 在 scanner 退出后立即检查略晚到期的 journal timer。补齐导入并等待 journal timer 的实际租约后，两个测试分别 `-count=20` 通过。首轮失败不从报告删除。

修正后执行：

```powershell
go test -race -timeout 120m -count=10 ./internal/session ./internal/backend ./internal/protocol
```

结果：

- `internal/session`：通过，58.324 秒。
- `internal/protocol`：通过，10.698 秒。
- `internal/backend`：运行 2082.042 秒，无 `WARNING: DATA RACE`；每轮仅失败 `TestFilesystemRootConfinementAndText` 和 `TestBackendQueueFIFOInterruptAndShutdown`，均为已在 detached clean baseline 复现的 Windows 基线失败。

日志：`%TEMP%/kbrain-session-sync-race10-rerun.txt`。按预检规则，该命令总体退出 1，不能写成全绿；能证明改动包没有新增 race 失败，且非基线测试全部通过。

## 最终独立复核修复

`TestSessionSnapshotInFlightCannotSurviveServerClose` 先在未修复代码上失败：请求已进入快照路径、Store 读取尚未完成时调用 `Server.Close`，关闭返回后继续请求，仍得到 HTTP 200，并重新留下 1 份快照和过期计时器。首败日志 `%TEMP%/kbrain-session-sync-review-close-first.log`。

修复在 `snapshotMu` 下预约构建槽位，并在同一锁内完成最终关闭/取消检查后才保存快照；关闭会取消正在构建的 context。槽位在失败、取消和正常返回路径统一释放。最多一个构建者，因此并发请求不能先各自复制/序列化全量摘要再等待容量检查；在途加已保存快照最多四份。64MiB 是保存摘要的 JSON 总预算，不能将它误称为整个 Store 或 Go 进程的内存上限。

新增回归覆盖关闭后迟到创建、并发构建 429、取消后重新创建、构建错误后的槽位回收，以及满槽时不再读取损坏 Store。`go build ./...`、受影响包 `go vet` 和新增/既有同步测试 race30 均通过。未修改主检出的用户开发，也未改 transcript 格式或正式发布锁。

随后 `TestSummaryReviewUsageDetailsDoNotAliasReturnedMetadata` 在 final2 源码上首次失败：`SubUsage` 与 `ModelUsage` 的 map 已复制，但各 value 中 `ai.Usage.PromptTokensDetails` 仍指向缓存对象。修改返回 Page 的 CachedTokens 为 700/1100 后，后续热列表也读到这两个污染值。首败日志 `%TEMP%/kbrain-session-sync-review-nested-clone-first.log`。final3 对该唯一的嵌套用量指针复制对象；新增测试同时验证 Page、Snapshot、Changes 及重复重放的隔离，`-count=20` 和 race30 均通过。

最后 `TestSessionSnapshotEmptyPageReturnsAnArray` 通过真实 HTTP 在空库上复现 `sessions:null`：分页复制使用 nil slice，JSON 将其编码成 null，而桌面协调器要求数组，导致空库初始化无法健康。首败日志 `%TEMP%/kbrain-session-sync-review-empty-page-first.log`。final4 用非 nil slice 复制分页，空页返回 `sessions:[]`、`next_offset:null`；backend 同步用例 race30 通过。

快照 admission 修复后的 backend 全包复测仅排除已单独复现的两个 Windows 基线测试 `TestFilesystemRootConfinementAndText`、`TestBackendQueueFIFOInterruptAndShutdown`，其余全部通过（60.396 秒）；日志 `%TEMP%/kbrain-session-sync-review-backend-full.log`。它不代表未排除的全量命令通过，也不替代此前保留的 8 项全仓基线失败记录。

## 全量测试与基线失败

执行：

```powershell
go test ./... -timeout 60m -count=1
```

最终源码日志：`%TEMP%/kbrain-session-sync-full-tests-final.log`。结果是 **43 个包通过，8 个测试失败**，不能写成全量通过。早期 r4 日志 `%TEMP%/kbrain-session-sync-full-tests-r4.log` 的失败集合相同：

1. `TestRestoreSessionPreservesHistoryAndCacheIdentity`
2. `TestCompactThresholdExplicitOverride`
3. `TestWaitingOnSubagentsDuringForegroundSubagent`
4. `TestFilesystemRootConfinementAndText`
5. `TestBackendQueueFIFOInterruptAndShutdown`
6. `TestPersistenceCASIdempotencyAndRollback`
7. `TestLiveAgentManagedProcessWaitCursorAndStop`
8. `TestLiveAgentRealHTTPProviderFileLoop`

前六项与本机 `known-failures.json` 一致，并在 detached clean baseline `0dad9fd` 上按对应测试复现。第 7、8 项也在 detached clean baseline 复现；原因是 Windows 测试进程使用 PowerShell 执行 Unix `printf`，并非本任务代码路径。当前分支对第 7、8 项单独 `-count=20` 仍稳定失败，完整保留首轮失败事实。

基线复现使用 detached 工作树 `%TEMP%/kbrain-baseline-validation-0dad9fd`，执行：

```powershell
go test ./internal/acp -run '^TestRestoreSessionPreservesHistoryAndCacheIdentity$' -count=1
go test ./internal/agent -run '^(TestCompactThresholdExplicitOverride|TestWaitingOnSubagentsDuringForegroundSubagent)$' -count=1
go test ./internal/backend -run '^(TestFilesystemRootConfinementAndText|TestBackendQueueFIFOInterruptAndShutdown)$' -count=1
go test ./internal/planning -run '^TestPersistenceCASIdempotencyAndRollback$' -count=1
go test ./internal/tools -run '^(TestLiveAgentManagedProcessWaitCursorAndStop|TestLiveAgentRealHTTPProviderFileLoop)$' -count=1
```

最后一条在任务分支另以 `-count=20` 执行，两个测试 20/20 失败；输出持续显示 PowerShell 无法识别 `printf`，以及依赖该 shell 命令的 managed process 状态为 failed。基线与任务分支的失败文本一致。

除 Windows 符号链接权限测试外，以下范围通过：

```powershell
go test ./internal/session ./internal/protocol ./internal/backend -skip TestFilesystemRootConfinementAndText -count=1
```

其中 session、protocol、backend 均通过；backend 用时 46.908 秒。

## 资源数据

相同的 248 个会话、约 251MiB 数据集，5 分钟初步对照：

| 指标 | 基线 | r4 实现 | 说明 |
|---|---:|---:|---|
| CPU 累计 | 48.5s | 1.14s | r4 采样与部分测试有重叠，最终安静 30 分钟数据待补 |
| 列表平均延迟 | 2936ms | 101.62ms | r4 min 14.45ms、max 1273.25ms |
| 私有内存峰值 | 75MB | 72.5MB | 缓存没有使进程峰值升高 |
| 进程读取字节 | 3.684GB | 151.8MB | r4 采样晚于 cold read，不能据此声称完整解析为零 |

30 分钟安静采样结果：

| 指标 | 基线 | r4 实现 | 变化 |
|---|---:|---:|---:|
| CPU 累计 | 268.73s | 4.28s | 减少 98.4% |
| 私有内存峰值 | 75.40MB | 72.43MB | 减少 2.97MB |
| 90 次列表平均延迟 | 2804.37ms | 49.38ms | 减少 98.2% |
| 进程读取字节 | 21,318,598,656 | 169,084,158 | 减少 99.2% |
| Working Set 峰值 | 41,967,616 | 41,926,656 | 减少 40,960 bytes |
| 私有内存起止增量 | +6,320,128 | -5,566,464 | 只描述该轮起止状态，不单独用于判断泄漏 |
| 私有内存斜率/分钟 | +210,755 | -185,619 | 同上，受启动、GC 与采样起点影响 |
| 句柄峰值/起止增量 | 288 / +56 | 252 / +13 | 只作同负载对照，不据起止增量断言线性趋势 |
| 线程峰值/起止增量 | 21 / +2 | 20 / -3 | 只作同负载对照，不据起止增量断言线性趋势 |

基线采样时长 `1,799,279ms`，实现采样时长 `1,799,321ms`，各完成 90 次等负载列表请求。请求范围分别为基线 2533.61–3641.19ms、实现 12.33–1560.12ms。30 分钟采样覆盖 warm cache 和周期 stat；完整解析是否为零仍以 A01 的 read observer 为准，而不是从进程 I/O 倒推。

权威原始文件为 `%TEMP%/liveagent-session-sync-baseline-30min/summary.json` 和 `%TEMP%/liveagent-session-sync-implementation-r4-30min/summary.json`。同机测试和构建曾与资源采样时段部分重叠，因此这里写“30 分钟同负载采样”，不声称整个区间完全安静；CPU、内存、句柄和线程结论以原始计数为准，不从单轮起止 delta 推断长期无泄漏。

分配与延迟使用 248 个会话的固定 fixture，每个会话在 cold/warm/mutation 基准中含 32KiB 消息；执行 `go test ./internal/session -run '^$' -bench 'BenchmarkSummary(ListCold|ListWarm|Mutation|Changes)$' -benchtime=10x -benchmem -count=3`：

| Benchmark | 三次 ns/op | B/op 范围 | allocs/op 范围 |
|---|---|---:|---:|
| `BenchmarkSummaryListCold` | 66.28ms / 76.60ms / 66.38ms | 50,685,579–50,698,428 | 15,312–15,347 |
| `BenchmarkSummaryListWarm` | 8.45ms / 8.43ms / 9.77ms | 856,499–877,524 | 1,941 |
| `BenchmarkSummaryMutation` | 5.05ms / 4.57ms / 6.24ms | 289,178–303,402 | 244–250 |
| `BenchmarkSummaryChanges` | 4.48ms / 4.97ms / 4.84ms | 48,999–58,000 | 251–256 |

基准日志：`%TEMP%/kbrain-session-sync-benchmem.txt`。Cold 基准每轮主动丢弃摘要缓存，用于量化完整解析；Warm 基准保留缓存，读取计数仍由 A01 的精确 observer 断言证明为零。基准包含目录扫描、stat、摘要复制和排序，因此 Warm 的分配不等于 transcript 解析。

“热缓存完整 transcript 解析为零”由 `TestSummaryCacheAvoidsTranscriptReadsWhenFilesAreUnchanged` 的读取计数断言证明；进程 I/O 包含目录、stat、启动和其他运行时读取，两者不混称。

## 最终真实客户端与取舍

- final4正式桌面加两个WebUI循环30分钟，106/106轮创建、置顶、改名、删除及三端DOM一致性通过；实际空库桌面初始化healthy=true、0会话/0cwd/revision0、单订阅且Gateway权威快照已交接。新LA+旧KB保持legacy并实际改名成功；旧b204LA+final4在有效fixture cwd/model下真实菜单改名成功。最初synthetic cwd/model不存在的失败日志保留，未改变后端契约。
- 真实PTY双Ctrl+C取消、随后改名/退出0、resume恢复原prompt与标题、再次退出0通过，证据 `%TEMP%/kbrain-session-sync-final4-tui/pty-result.json`；LiveAgent新协议真实HTTP与Gateway直连最终14项亦通过。定时任务实际生成结果和外部kn共享home写入被扫描发现也已由整应用自动化覆盖。
- 早期B7228F组合的100轮约+86MB、30分钟约+775MB WebView增长样本保留。最初将完整reset重建视为候选原因，改为真实增量后仍增长；最终heap/timeline诊断确认React 19 DEV保留约378,777条native PerformanceMeasure，清除后renderer private约831.1→319.5MB。产品现仅在DEV为React轨迹设预算并保留应用timing；final4组合50轮真实短采renderer private峰约332MB、末315MB且相对起点-16.3MB，正式5/30分钟数据由LiveAgent报告收敛。不得把早期“每次full reset造成全部增长”的假设当成已证实根因。K-brain同轮私有内存仅小幅波动，不能据此忽略WebView开销。
- 最终三端空闲303.817秒：K-brain累计CPU0.719秒、进程读取字节增量0、private+0.311MB、句柄增量0；允许目录/stat，不能称零读盘。正式106轮写入循环K-brainCPU538.594秒、read193.45MB、private70.51→71.18MB、峰75.58MB，句柄/线程均无增量。大量kernelCPU的创建/metadata成本另用相同短负载分项检查，不能将此写入循环与只读90次列表基准混比。
- 无订阅小库相同10轮HTTP create/pin/rename/delete：旧0dad累计CPU0.421875秒（user0.171875/kernel0.25），final4CPU0.34375秒（user0.125/kernel0.21875），各10/10通过。该结果不排除大库与订阅组合的其他成本。
- 独立TUI前后同248会话/251MiB、无HTTP，各约40秒，实际picker50条、导航3项、Esc、quit0：baseline CPU5.891秒、read528.323MB、private峰558.76MB/end76.07MB；final4CPU6.281秒、read531.508MB、private峰593.26MB/end87.94MB。冷启动/preview仍严格解析；final4峰约+34.5MB、末值+11.9MB是真实缓存/分配取舍，不宣称TUI全面提速。导航时点有数秒差异，不计算精确百分比。
- 旧LA+旧KB两个WebUI未达到列表ready，虽然桌面248条、后端列表200约2.5秒；未取得可比的三端空闲旧基线。记录为限制，不能将失败界面负载当作同视图对照；已有独立后端同90次列表30分钟对照保持有效。

## 提交与检查范围

- 独立对抗性复审最终轮已完成，关闭/admission、嵌套深拷贝及空页缺陷已补先失败回归并修复。实现提交为 `482fd2d`，CI 配置修复为 `b4f5508`，PR #60 目标为上游 `main`，没有合并或发布。
- 以下成功结果对应 `b4f5508`；仅证据文档更新后仍须确认最新 PR head 的所有 checks 进入成功终态。开发构建仍是开发覆盖，不能替代真实发布依赖。

## PR CI 收敛记录

PR #60 的首轮运行 `40620223448`（提交 `482fd2d`）已进入终态：macOS 通过，Windows 和 Linux 失败。

- Windows 在 gofmt 阶段列出全部 13 个任务 Go 文件。Git checkout 的自动 CRLF 转换改变了工作树字节；workflow 改为 checkout 前设置 `core.autocrlf=false`，继续检查原始提交的 LF 内容，不重写源码或跳过格式检查。
- Linux 完整测试只有 `internal/browser` 失败，共 9 个用例：`TestE2EHeadless`、`TestE2EDedicated`、`TestEvalImmediatelyAfterAttach`、`TestE2ELiveFallsBackToLaunched`、`TestE2EDedicatedReattach`、`TestConcurrentSessionsDriveIsolatedBrowsers`、`TestChurnOpenClose`、`TestRecoverFromClosedBrowser`、`TestManySequentialCalls`。全部日志均为 Chromium `No usable sandbox`；其他包通过。首轮原始失败日志保存在 `%TEMP%/kbrain-pr60-ci-first.log`。
- Linux workflow 在临时 runner 上允许非特权 user namespace，并通过 `unshare` 检查该能力；完整测试用 Xvfb 提供 dedicated 浏览器所需的显示服务。Chromium sandbox 仍开启，没有传入 `--no-sandbox` 或跳过浏览器测试。
- 原 race30 步骤因完整测试失败而被跳过。现在 session 和 backend race30 各为独立步骤，使用 `!cancelled()`，完整测试失败也会继续执行两组检查。
- 多命令 PowerShell 步骤逐条检查 native exit code，防止早期失败被后续成功命令覆盖。

修正提交 `b4f5508434d337a17a1f01899466db42543552c0` 的运行 [40620963007](https://github.com/Stack-Cairn/K-brain/actions/runs/40620963007) 已全部成功：

| Job / 检查 | 结果 |
|---|---|
| Windows | 2分43秒；LF 格式检查、构建、vet、session/protocol/backend/ACP/CLI/TUI 定向测试全部通过 |
| macOS arm64 | 1分33秒；格式、构建、vet 和相同定向测试通过 |
| Linux | 3分8秒；格式、构建、vet、定向测试及完整 `go test ./...` 通过，完整测试共 47 个包报告 ok |
| Linux 浏览器包 | `internal/browser` 47.857秒通过，Chromium sandbox 开启，没有新增测试跳过 |
| Linux session race30 | 25.808秒通过 |
| Linux backend race30 | 3.152秒通过 |

原始成功日志 `%TEMP%/kbrain-pr60-ci-b4f5508-success.log`。本次生产 Go 源码和正式发布锁未随 CI 修复改变；浏览器包相对 `0dad9fd` 也无源码差异。首轮失败保留，不能用后续通过抹去初次结果。

仅文档提交 `a0cbc8f` 的下一轮 [40621444022](https://github.com/Stack-Cairn/K-brain/actions/runs/40621444022) 中，Windows/macOS 和两组 Linux race30 都通过；Linux 完整测试新增一项失败：`internal/tui.TestRunningTaskViewReplaysThenStreams` 在 `tasks_test.go:503` 等待 pre-open SSE 文本 5 秒超时。浏览器包本轮通过，未再发生 sandbox 失败。首败日志 `%TEMP%/kbrain-pr60-ci-a0cbc8f-failed.log`。

该 TUI 包相对 `0dad9fd` 没有源码修改；相同生产源码的上一轮完整测试已通过。因此不修改业务或扩大测试超时，也不通过重试覆盖这次失败。下一轮 workflow 在同一 Linux runner 上，对只读 clean `0dad9fd` 与当前源码分别执行精确用例 `-count=30` 并保留输出，当前用例仍是必须通过的检查；完整套件将 package 并发限制为 2，避免浏览器进程与测试内可执行文件构建在小 runner 上同时争用资源，全部原测试保持执行。资源竞争目前是待验证原因，不视为已经定位。
