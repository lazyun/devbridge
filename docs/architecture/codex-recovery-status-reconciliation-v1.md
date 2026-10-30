# Codex 工程状态恢复：实施与验收记录

状态：Recovery Status v1 生产链闭合；产品整体仍保留 `PARTIAL_IMPLEMENTATION`。G1–G16 与 Project Inspection G10 的最终证据支持 `CODEX_RECOVERY_STATUS_RECONCILIATION_V1_CLOSED`，详见 [Project Inspection G10 closure](codex-recovery-status-inspection-g10-closure.md)。

Project Inspection canonical runtime 的真实 owner、Run/Turn、epoch、workset、
conversation、App Server 和观察事实已经接入同一 Recovery Status 服务、store、
Assessment 和 resolution 状态机；详见 [Project Inspection canonical runtime v1](project-inspection-canonical-runtime-v1.md)。

## 基线与变更边界

开始分支 `dev_20260908_1913`，HEAD `fd8a6afeb213972e8aed00ff23f090ae058b8530`，schema v80，index 空，逐文件 dirty 338 条。1840 个文件的原始字节、SHA-256 manifest、branch/head/status/index 位于 `/private/tmp/devbridge-recovery-status-baseline-yb9di5pb`。增量仅相对此快照计算；不把继承变更算入本轮。v81 仅迁移测试数据库，没有打开用户 live SQLite。

## 冻结的权威合同

Recovery Status 是原 Review / Execution 的子 Run，purpose 为 `RECOVERY_STATUS`。它不是新的 Task、Revision、Review、Decision 或 Task.phase。Record 精确连接 source operation/Run/Turn/epoch、revision、RuntimeWorkset、canonical conversation/Thread、App Server binding，以及新的 Recovery Run/Turn。原 RuntimeRequest、UserDecision、dispatch/evidence、conversation interaction 保持原样；原 Run 的工程状态只可在独立 recovery resolution 事务内收敛。

新 Turn 不能成为旧回答的 consumed receipt。普通 `requireResolvedResponsesTx` 继续阻断 UNKNOWN。新的 receipt 只解决工程 owner 的完成状态，不能允许重发、将原请求标 RESPONDED 或声明 exactly-once。`CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED` 继续保留。

prepare 保存 caller 提供的有界、无 NUL instruction 和校验和，不创建 Run、不调用模型。start 重验 canonical owner、revision/workset、原实例/Thread/source Turn、idle/terminal/no pending、工作区 root 和单一 claim。旧 UNKNOWN 仅在有 durable UserDecision 且原 Turn terminal、Thread idle 时允许进入工程恢复。机器字段承载 roots、model/effort/tier、read-only intent 和 structured schema；Core 不拼接自然语言。旧问题与答案不注入新 input。

子 Run 借用原 operation 的 claim，没有第二个独立 writer。持久化 dispatch intent 先于 turn/start；start 响应未知不能再次 start。原 source callback 被 recovery child authority 隔离。新连接只 resume exact 已加载 idle Thread 以订阅通知，不创建替代 Thread。

生产调用链为 scoped facade → `execution.RecoveryStatusService` → `sqlite.RecoveryStatusStore`。Start 先 `ObserveRecoverySource` 读取 exact loaded Thread/原 Turn，再观察冻结成员、创建 child 和 durable dispatch fence，最后 `Client.StartRecoveryStatusTurn`；terminal 后 `finish` 保存 report/after，`assess` 调用 `Coordinator.ValidateRecoveryOwnerFacts` 和确定性 `recoverystatus.Assess`。Resolve 重验事实，进入 store 的单一事务。`controlplane.Open` 先恢复 Recovery 子 Run，再处理普通 App Server continuation；source callbacks 由 recovery authority fence 排除。已知 child 由原 S1 rejoin，缺少 Turn ID 的未知 start 不通过猜测“最新 Turn”重发或取消。

## 报告与事实

`RecoveryStatusReport v1` 严格验证 required fields、版本、source binding、枚举、数组、路径及重复 JSON key；不接受 prose fallback。模型使用 runtime alias 关联成员；其数据库 ID 或建议不授予业务权威。Review 必须附完整合法 v2 report；Verification 可附原 v2 reported checks/acceptance criteria，但 required checks 还需要实际 command/cwd/exit evidence。

每个 managed member 的前后观察包含 canonical root/dev/inode、worktree/member identity、HEAD、Git snapshot、diff digest、changed paths、branch 与 metadata digest。任何 probe mutation、root 替换、HEAD/metadata 漂移或缺失成员都不能推进；实际字节和证据保留，不 reset。

Core 的 immutable Assessment 绑定原 owner、Recovery Run/report、完整 facts hash、report hash、consistency、engineering disposition、reason/actions 和 sourceResponseDisposition。报告自称完成、terminal、idle 或仅存在 diff 均不能单独推进。OwnerFacts 验证原定义、批准目标和各类 result contract；Assessment 再交叉检查实际所有成员与 goals/checks。

v81 保存 spec、原始 report/facts/assessment 字节与 hash、resolution receipt、source linkage。JSON 原始字节另以二进制形式保存，避免 JSON 重新编码改变 hash。唯一 source/Turn/generation 与 stable request identity 约束 exact replay；不同 payload/instruction 冲突。没有启动时自动为历史 operation 创建 probe。

## canonical resolution 与 continuation

resolve 只接受 Core 提供的 exact source/epoch/report/assessment hash command，重新读取原实例 idle/quiescence 与工作区，再在 SQLite 写事务核对 owner、revision/workset、取消/后继和 receipt。客户端不能提供 completed、admission、report 或 assessment。

Implementation complete 通过既有 Task phase helper 进入 VERIFICATION_READY（或原批准 waiver 路径）；Verification 使用实际 required-check result 进入 ACCEPTANCE_PENDING；Review 复用既有 latest-only approval。不会自动 ACCEPT/APPROVE。恢复结果只填补缺失 result 槽位，原交互与旧 UNKNOWN 不变。

Partial/revision resolution 收敛原 attempt，保留工作区，然后使用既有 retry / purpose confirmation。新增 `recoveryStatusId` 与 `recoveryContinuationInstruction` 只供已收敛 partial 的 `RETRY_IMPLEMENTATION` 使用：独立 immutable instruction receipt 保留原 revision 和批准定义，不允许混入 scope/config 修改；Coordinator 再次比对 exact instruction。原生生产链已验证同 revision continuation；后继 Verification 的独立测试同时检查原 S1 和实际检查结果。

报告声称需要用户、但没有实际 CODEX_REQUEST 时保持 insufficient，不合成问题。实际新请求仍必须由用户显式回答；GPT 不代答。

## 接口与统一展示

MCP：`recovery_status_prepare/start/read/resolve`。HTTP：`/v1/recovery-status/prepare`、`/{id}/start`、`/{id}/resolve`、GET `/{id}`；本地 operator 使用 `/ui/api/recovery-status/...`。所有 facade 先验证 Session/operator Project scope 和原 owner，再交给 service/store。Read 返回可复制的 exact start/resolve command，read/refresh 不启动模型。

统一 Core projection 供 operation_get、Task activity、Console、WebUI、Desktop 内嵌 workbench 和 ChatGPT Card 使用。工程已核验且原消费 UNKNOWN 时同时展示：“旧回答是否被 Codex 消费仍无法确认；当前工程状态已通过新的 Recovery Turn 独立核验。”客户端不将两者合并成“回答已确认收到”。

## 重启与尚未完成边界

Prepared/Run-created 重开不启动模型；已知 Recovery Turn 只 rejoin；start 效果未知保持 UNKNOWN；已保存 report 从原 bytes 恢复 Assessment；已保存 Assessment 不自动 resolve；resolution reply-loss 读取原 receipt。八个故障窗口已通过原生离线生产链验证，具体窗口见 G12；源码支持与生产验收分别记录。

Project Inspection 使用 v82 建立的 Project-scope owner、managed Workspace、RuntimeWorkset v2、canonical conversation、App Server checkpoint 和 Thread。v83 只允许这些可验证 source 创建 Recovery child；历史 Inspection 继续 fail closed。没有创建假的 Task、FeatureArea、revision、Execution、conversation、Workspace、workset 或 Thread。

## 验证记录

以下命令表按最终 JSON test events 统计顶层/子测试；子测试含嵌套组，不额外累加为独立场景。compile-only、零匹配、helper 普通调用、SKIP 均不计 runtime acceptance。

保留 `CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`、`CODEX_REQUEST_USER_DECISION_V1_CLOSED`、`CODEX_PENDING_REQUEST_CROSS_CORE_REATTACH_CLOSED`、`MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`、`TRUSTED_EXECUTOR_MODEL_V1`。继续保留 `CODEX_SANDBOX_HARDENING_DEFERRED`、`SAFE_REPOSITORY_UNLINK_CLOSED`、`MANUAL_DESKTOP_ACCEPTANCE_PENDING`、`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`。真实模型未运行：`REAL_CODING_AGENT_E2E_NOT_RUN`、`REAL_CODEX_REQUEST_E2E_NOT_RUN`、`REAL_CODEX_RECOVERY_STATUS_E2E_NOT_RUN`。

## 生产验收矩阵（本轮）

| Gate | 结果与证据 |
| --- | --- |
| G1 / G2 | 原生离线双仓修改、原 Turn terminal/Thread idle、旧回答 UNKNOWN、新 Run/Turn、实际观察、Assessment、canonical resolution → VERIFICATION_READY；旧 request/decision/dispatch/evidence 不变。 |
| G3 | 原生 app-only → PARTIAL → existing RETRY_IMPLEMENTATION → 新 Run/Turn、原 Thread/S1、原 revision，caller 新 instruction 原样上 wire，sdk 完成且 app 成果保留。v77 hash 含 ExecutionID，successor 绑定新 Execution 后 hash 会改变；测试仅归一化该 owner 字段后核对全部批准成员、roots、backend、revision 不变，没有修改旧 v77 hash 算法。 |
| G4 | 报告声称不存在的修改：CONFLICT/INSUFFICIENT，不推进；真实 Git 单元覆盖 root/HEAD/index/config 漂移。 |
| G5 | 独立进程 fake App Server 通过真实 Client/Unix WebSocket 执行修改，故意违反 read-only intent；Core 保留实际修改并判 RECOVERY_FAILED。原生 App Server 的 write-denied 用例另列，不冒充“实际发生修改”验收。无效报告同样在 terminal/quiesced 后保留 after observations。 |
| G6 | 原生新 CODEX_REQUEST，read 不回答；fixture 的显式用户决定后同 Recovery Run/Turn 继续。另一个正向案例从新请求正式取消，不改写旧 UNKNOWN ledger。 |
| G7 | scopeExpansion → REVISION_REQUIRED；报告不触发执行。正式 purpose-scoped REQUEST_CHANGES 确认 exact variant 后保存扩大 PATHS 的新不可变 revision 和新 Execution。未经确认注入 targets、改变 replay targets 被拒绝。 |
| G8 | 原生 Verification required commands 的目录、命令与 exit=0 完成事实在丢失回答 receipt 前已持久化，新 Recovery Turn 核对后进入 ACCEPTANCE_PENDING，不自动 ACCEPT。缺少一个 required check 时保持 VERIFYING/INSUFFICIENT。原生 thread/read 不保证保留 commandExecution items；没有原始命令证据时，provider 输出或模型报告不能补造证明。 |
| G9 | 原生 CODING_AGENT Review 提供完整 v2 result，原 Review/Thread 保留；canonical latest-only approval，不创建替代 Review、不自动 APPROVE。 |
| G10 | **PASS**。固定 Codex App Server 0.149.0 的 11 个主场景和 7 个持久化窗口全部通过；complete/partial/conflict/mutation/request/cancel/revision/concurrency/legacy 均经过 production facade、SQLite、managed worktrees、same S1/Thread、Assessment 和 canonical reducer。 |
| G11 | 原生 pending CODEX_REQUEST、已准备 proposal 后正式取消、已确认 successor 的旧 source 均不启动 probe。另有独立 fake App Server 进程经过真实 Client serializer、facade、SQLite、canonical owner/workset/S1 的五项负向：active Thread 且旧回答已 CONSUMED/无 pending；idle Thread 但 source Turn nonterminal；真实 pending RUNTIME_APPROVAL；丢失回答 receipt/Core 重开后 UNKNOWN+nonterminal；turn/interrupt 回应丢失且 canonical cancellation 仍 CANCEL_REQUESTED/RECOVERY_REQUIRED。各项明确断言先决状态与拒绝层，没有直接改库，零 Recovery child，wire 只有一次原 turn/start。后五项是允许的 production fake backend 验收，不标为原生 App Server。 |
| G12 | 八窗口均有原生生产链：prepared-no-Run；created-no-Turn；uncertain-start；active Turn/Core restart；report-not-saved；report-saved/no-assessment；assessment/no-resolution；resolution effect/reply lost。UNKNOWN 不重发，已知 Turn 只 rejoin；已保存 report/assessment/receipt 使用原 bytes/hash；旧 epoch callback 被拒绝。 |
| G13 | 原生 probe active 时，同 FeatureArea Coding contender 被拒绝；不同 FeatureArea 在同一测试 Project/DataRoot 独立调用 provider，原 probe 随后正常 resolve。观察只比较当前 worktree HEAD/index/config.worktree 与共享 config；不将其他 Area 的 refs 创建/pack-refs 误当 probe mutation。 |
| G14 | source-bound structured schema 用 machine const 传原 owner identity；instruction durable bytes/hash 与实际 provider 输入完全相同。continuation 也验证 exact text；不注入旧回答。 |
| G15 | Core 统一 projection；MCP 实际 SDK read、HTTP closed DTO 拒绝 outcome/admission/force 注入；Console/嵌入 Desktop 与 Card 的动态 JS 验证状态/旧 UNKNOWN 分开展示、文本转义、重复刷新无副作用。已创建未 dispatch 的 child 仍展示显式 START；证据不足且已知 child Turn 时展示 canonical cancel/revise；取消 receipt 后 required=false。未进行人工 UI 验收。 |
| G16 | 定点回归、历史 migration/FK、legacy fixtures、root/Desktop compile、vet、race、JS 与 diff-check 见命令表。宽泛旧测试集存在基线可复现失败，单独列出，不宣称整个仓库测试全绿。 |

## Inspection G10 owner 变体

Project Inspection owner Task 保持 `scope=PROJECT`、`FeatureArea=NULL`。Recovery child 是原 operation 的子 Run，复用 source conversation、S1、Thread、Workspace、RuntimeWorkset 和唯一 Project Inspection claim authority，并创建新的 Turn。Report v2 必须携带由 canonical Inspection parser 验证的 `inspectionResult`。Resolution 只消费 immutable Assessment，并通过同一 Inspection reducer 收敛 complete、verified partial 或 revision-required source。

Verified partial 先把 source attempt 收敛为 BLOCKED，再由 caller-authored continuation receipt 创建同 operation 的新 Run/Turn；target expansion 只能经过新的 Project Inspection prepare/confirm。v83 的 source、claim、binding、epoch、status-transition 与 callback fences 都只接受精确 receipt/current pointer/current claim。历史 Inspection 缺少 v82 canonical runtime facts 时保持可读并以 `LEGACY_INSPECTION_NO_CANONICAL_RUNTIME` 拒绝 Recovery。

## 已发现并修复的问题

- 原 S1 fresh connection 只有 thread/read 不会收到新 Turn 通知：显式 resume exact 已加载 Thread，保持配置与身份。
- recovery resolution 后源执行器本地 reservation 残留：只在 durable resolution/cancel 后、源 Run terminal 且无 live session 时释放 exact owner。
- 完整恢复、partial continuation 及恢复 Review 批准后的后继曾创建 S2，触发 native active-writer 冲突：通过 durable recovery lineage、当前 owner/claim/epoch 与 exact conversation entry boundary 复用原 S1；不会按最新 Thread 猜 owner。普通 continuation 成功后的 idle cleanup 也曾提前停止 S1，现仅为同 canonical owner 明确待执行的 Implementation/Verification 保留，进入 ACCEPTANCE_PENDING 后不再要求保留。保留进程不授予启动权限。
- REQUEST_CHANGES 的已确认 targets 曾在 adapter/store 间丢失：传递并验证 exact committed variant，targets 保存在新 definition；不放宽未经确认的输入。
- 源 FAILED Run 的 recovery claim 曾可能被普通 startup 收回：有 recovery record 的 source 从普通 startup 恢复路径排除。
- 历史其他 Run 的 CANCELED receipt 不应阻止当前合法 source：completed cancellation 仅匹配 exact source/child，仍阻断 Task 的未收敛取消。
- source commandActions 曾从 bounded metadata 省略：保留有界实际命令事实，Verification 只读补充原 Turn 仍可读取的 items；不写回原 history。
- typed-nil API error、RUN_CREATED 无启动 guidance、取消仍显示 required，以及不相关 refs 引起 mutation 误报均已修正并验证。

## 继承失败与运行条件

在本轮原始字节快照目录直接运行同名测试，复现了相同失败：

- SQLite `TestLifecycleTaskReviewHistoryPreservedAndEmptyAreaDecomposeReplay`：Feature Area agent context unavailable；`TestOwnershipMigrationPreservesLegacyHistoryAndRuntimeFences`：旧 migration fixture 缺少 backend_selection_json。宽泛 history/migration 选择为 27 顶层 + 27 子测试 PASS、2 顶层 FAIL；v81 专属 rollback/repeat/populated-history/FK 用例通过。
- 旧 continuation/Review fixtures 的 starts=0、NO_START conflict、SESSION_INITIALIZATION_REQUIRED；baseline 复现记录 `/private/tmp/recovery-continuation-baseline.jsonl`。本轮 required legacy 14 个顶层 + 9 子测试通过，不将宽泛旧 fixture 失败算成这 14 项失败。
- 旧 Web workbench navigation 期望缺少 sessions tab：1 FAIL；宽泛旧 Card task-centric/harness 112 FAIL。合并原始快照复验 126 项，13 PASS/113 FAIL；本轮直接相关 JS 7 项全部通过。未以修改旧断言或隐藏失败制造全绿。

第一次普通沙盒运行个别 native/HTTP tests 因 loopback bind 权限失败；之后使用获准的测试自有离线网络/进程权限复验。不是自动审批拒绝。早期 native fixture 的 subscribe、workset、continuation、metadata、target persistence 和 command-evidence timing 失败均保留在 `/private/tmp/recovery-status-*.jsonl`；只有最终成功复验计入 PASS。

真实模型 opt-in 默认未启用，门控测试的 SKIP 仅记 `REAL_CODEX_RECOVERY_STATUS_E2E_NOT_RUN`。测试要求显式 isolated config/auth 文件，并将真实 CODEX_REQUEST 留给用户填写 exact UserDecisionInput，不能替用户回答。没有调用真实模型、读取默认 home credentials、操作 UI 或截图。


## 命令与实际验证结果

所有 Go 命令使用 `GOCACHE=/private/tmp/devbridge-go-cache`、`-count=1`（compile-only 除外）。原生离线用例显式设置 `DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY` 为已安装固定二进制：

`/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`

每个 fixture 独立 DataRoot、CODEX_HOME、Unix socket、Git repository 和 loopback fake provider。两个中间整组运行的业务断言通过，但 native plugin catalog 后台 Git clone 与 TempDir 删除竞争，包仍判 FAIL，未计 PASS。新 fixture 的自有 config 关闭 `plugins/remote_plugin`，并仅停止经原始 binding 验证、属于该 DataRoot 的 App Server 进程组；未更改产品策略、全局配置或清理其他进程。该隔离修复后的整组运行通过。

| 选择与命令 | 实际结果 | 原始日志（`/private/tmp/`） |
| --- | --- | --- |
| `go test -p 1 ./internal/controlapi -run '^TestRecoveryStatus' -count=1 -timeout=360s -json` | **14 顶层 / 26 子测试 PASS**（226.287s），0 FAIL/SKIP；其中 1 顶层是纯 projection/command unit，13 顶层为生产验收入口。已包含三条后继链和 G11 五项新门禁。下方定点复验与本组重叠，不累加为新测试。 | `dbrecovery-final-production.jsonl` |
| `go test ./internal/controlapi -run '^TestRecoveryStatusOffline(CompleteThenVerificationSameServer\|PartialThenVerificationSameServer\|ReviewThenExecutionSameServer)$' -count=1 -timeout=240s -json` | 3 顶层 / 2 子测试 PASS，原 S1/Thread、真实 required checks、ACCEPTANCE_PENDING；含 unrelated/terminal binding 与 retention 负向。 | `recovery-status-successor-all-final.jsonl` |
| `go test ./internal/controlapi -run '^TestRecoveryStatusProductionAdmissionWindows$' -count=1 -timeout=90s -json` | 1 顶层 / 5 子测试 PASS，G11 五个独立生产 fake backend 门禁；无数据库状态注入。 | `recovery-admission-gates-final.jsonl` |
| 同一 G11 选择增加 `-race -timeout=180s` | 1 顶层 / 5 子测试 PASS（170.17s），无 race 报告；120s 中间尝试超时，不计通过。 | `recovery-admission-gates-final-race-fast.jsonl` |
| `go test -p 1 ./internal/controlapi -run '^TestRuntime(ContinuityOffline\|UnknownOffline\|UnknownProduction\|UserDecisionProduction)' -count=1 -timeout=300s -json` | 22 顶层 / 22 子测试 PASS，无 FAIL/SKIP。 | `dbrecovery-runtime-final.jsonl` |
| `go test -p 1 ./internal/controlapi -run '^Test(MultiRepositoryCoding\|MultiRepositoryDirect\|MultiRepositoryUpper\|UpperV1Facade)' -count=1 -timeout=240s -json` | 40 顶层 / 16 子测试 PASS，其中 1 个 App Server helper 不计验收；39 顶层实际测试。 | `dbrecovery-product-final.jsonl` |
| `go test -p 1 ./internal/execution -run "$(cat /private/tmp/db-unknown-legacy-regex.txt)" -count=1 -timeout=240s -json` | 必需的七类 legacy fixtures 当前等价集合：14 顶层 / 9 子测试 PASS。 | `dbrecovery-legacy-release.jsonl` |
| `go test -race -p 1 ./internal/storage/sqlite -run '^TestRecoveryStatus' -count=1 -timeout=180s -json` | 4 顶层 / 5 子测试 PASS，包括 v81 repeat/rollback/history/FK、source exact owner 和失败 child 不可被新 generation 绕过。 | `dbrecovery-final-store-race.jsonl` |
| domain + facade + HTTP + MCP 定点 race（下方 exact regex） | 14 顶层 / 31 子测试 PASS，实际 SDK read/closed DTO 拒绝外部事实注入。 | `dbrecovery-adapter-projection-race-final.jsonl` |
| runtime + Client 定点 race（下方 exact regex） | 7 顶层 / 13 子测试 PASS。 | `recovery-runtime-unit-final.jsonl` |
| root `go test -p 1 ./... -run '^$'`；desktop 同命令 | 编译通过；不计 runtime acceptance。 | `dbrecovery-final-root-compile.log` / `dbrecovery-final-desktop-compile.log` |
| `go vet ./internal/recoverystatus ./internal/execution ./internal/storage/sqlite ./internal/controlapi ./internal/bridgehttp ./internal/mcpserver ./internal/codex ./internal/agentruntime ./internal/workflowprogression` | 定点 vet 通过。 | `dbrecovery-final-vet.log` |
| `node --test internal/bridgehttp/product_session_user_decision_test.js internal/mcpserver/dev_bridge_runtime_request_wakeup_test.js` | 7 PASS，0 FAIL。 | `dbrecovery-final-js.log` |
| `DEVBRIDGE_REAL_CODEX_RECOVERY_STATUS=0 go test -p 1 ./internal/controlapi -run '^TestRealCodexRecoveryStatus$' -count=1 -json` | 1 SKIP，0 测试 PASS；真实模型 NOT_RUN。 | `dbrecovery-real-smoke-final-not-run.jsonl` |
| `git diff --check` | 通过；index 仍为空。 | final audit |

Domain/adapter race 的完整选择：

```sh
go test -race -p 1 ./internal/recoverystatus ./internal/controlapi ./internal/bridgehttp ./internal/mcpserver -run '^(TestProjection.*|TestRecoveryStructured.*|TestRecoveryAssessment.*|TestRecoveryProbeMutation.*|TestRecoveryVerification.*|TestRecoveryReview.*|TestRecoveryReported.*|TestRecoverySchema.*|TestRecoveryObservation.*|TestRecoveryStatusReadCommandsBoundToDurableAssessment|TestRecoveryStatusHTTPRejectsClientFactsBeforeCore|TestRecoveryStatusActualSDKReadAndOutcomeInjectionRejection)$' -count=1 -timeout=180s -json
```

Runtime/Client race 的完整选择：

```sh
go test -race -p 1 ./internal/agentruntime ./internal/codex ./internal/execution -run '^(TestCommandMetadataRetainsBoundedAuthoritativeActions|TestRejoinLoadedTurnUsesOriginalTurnWithoutStart|TestRejoinRejectsUnloadedThreadWithoutColdResume|TestRecoveryStatusSameThreadExactPromptAndPreEffectFence|TestRecoveryStatusRejectsUnprovenThreadBeforeDispatch|TestRecoveryStatusLostStartResponseLeavesTurnUnproven|TestRecoveryStatusRequestAdmissionSeparatesOldUnknown)$' -count=1 -timeout=180s -json
```

实际 test events 与日志 SHA-256 汇总保存在 `/private/tmp/devbridge-recovery-status-final-audit/validation.json`。原始字节差异、逐文件 before/after hash、最终 status/index 与当前 manifest 保存在同目录；该目录是本轮自有审计产物，不是正式仓库提交。


## v81 阶段审计（历史记录）

本节记录 v81 阶段当时的增量；包含 G10 的最终 v83 审计见
[`codex-recovery-status-inspection-g10-closure.md`](./codex-recovery-status-inspection-g10-closure.md)。

Starting / Final branch 均为 `dev_20260908_1913`；HEAD 均为 `fd8a6afeb213972e8aed00ff23f090ae058b8530`；index 始终为空。源码/test schema v80 → v81，未迁移 live 数据。

逐文件 dirty：338 → 391。相对开工 1840 文件字节快照的实际增量：89 个文件（45 新增、44 修改、0 删除），+8402 / −70 行；不是相对 HEAD 的继承总 diff。开工 manifest 对保存字节的 SHA-256 复核全部一致。

产品整体仍保留 `PARTIAL_IMPLEMENTATION`，但 Recovery Status v1 与 Inspection G10 已通过生产回归并闭合。真实模型/人工 Desktop/Card 验收仍 NOT_RUN/PENDING，继承的 readback、sandbox hardening 与 safe unlink 状态保持不变。
