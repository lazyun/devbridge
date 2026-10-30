# CODEX_REQUEST v1：回答消费状态与安全恢复

2026-10-02：真实 Codex 0.159.2 的 pending/active-Turn 恢复已完成 [独立验收](../validation/codex-01592-active-turn-continuity-2026-10-02.md)。已消费但 resolved 确认丢失的窗口仍保持 UNKNOWN；该版本支持不改变下述无证明重发禁令。

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

状态：`CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`、`CODEX_REQUEST_USER_DECISION_V1_CLOSED`、`CODEX_PENDING_REQUEST_CROSS_CORE_REATTACH_CLOSED`。

闭合范围是本文冻结的 v1 安全合同：durable decision、无未证明重发、UNKNOWN 阻断与正式取消/successor。固定协议限制仍为 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`；项目整体继续 `PARTIAL_IMPLEMENTATION`。

## 冻结合同

用户决定先持久化且不可变。未回答绝不自动回答；已回答绝不再次询问、改写、翻译或生成替代答案。请求精确绑定 operation、Run、Thread、Turn、App Server instance/epoch、connection generation 和 backend request/item。

权威模型复用 RuntimeRequest 的 State（lifecycle）和 Delivery（response disposition），通过唯一 Core projection 输出 lifecycleState/responseDisposition；兼容读取历史值，不允许客户端重建状态。新 CODEX_REQUEST 使用 WAITING_FOR_USER、USER_DECISION_RECORDED、RESPONDING、RESPONDED、CANCELED、STALE；Delivery 使用 NOT_DISPATCHED、DISPATCHING、CONSUMED、UNKNOWN、NOT_APPLICABLE。历史 generic UNKNOWN 仅在 durable decision 与 dispatch facts 足够时视为回答消费未知，否则标注 LEGACY_RECOVERY_REQUIRED。

最小 v80 增加持久化 dispatch attempt/evidence：intent 与 RuntimeRequest snapshot 同一 SQLite transaction 写入，包含原 request、decision、完整 identity、payload bytes/hash、command identity、开始时间。调用后保存结果；SQLite 和 backend effect 不具备跨系统原子性。旧 v77/v78/v79 和不可变 UserDecision 保留。普通 respond 在 UNKNOWN 时拒绝；同命令重放仅返回原状态，不重复 effect，不同 payload 冲突。

只有经验证同实例精确 pending request 重投递，或协议明确的 pre-effect rejection，证明未消费。内部 recovery 记录证据，以原 request、原决定、原 payload bytes/hash 创建新的 transport attempt。禁止 force/retry/assumeNotConsumed 后门。

只有 exact serverRequest/resolved（当前实例、connection、Thread/Turn/backend request 精确关联）或协议等价 receipt 证明消费。active、idle、terminal、结构化结果、worktree 修改、缺少重投递、Core 重启、rollout 与 Codex SQLite 均不能证明已消费或未消费。

UNKNOWN 阻断所有 owner 的成功 finalization 与 gate progression，保留原业务 phase、Run 与 claim。后续结果仅能作为原 Run 的观察；不能产生 Approval、Acceptance 或 delivery。没有新 Task.phase、Decision entity、workflow owner 或 Runtime Broker。

UNKNOWN 仅提供 CONTINUE_WAITING、CANCEL_OPERATION、STOP_AND_REVISE。等待纯读；后两者调用既有 cancellation，保留原决定与 attempt，等待原 operation quiescence/recovery disposition 后才可通过既有 purpose-scoped confirmation 创建 successor。旧答案不进入新 Turn。迟到旧 epoch/request 通知不能推进 successor。

唯一 Core projection 向 JSON/MCP/Card/WebUI/Desktop/Console 提供相同字段与动作。主状态“回答状态无法确认”；说明“你的回答已经保存。Codex 可能已经收到，但当前协议无法确认。系统不会自动重发。” reasonCode=CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE，resendAllowed=false。实例丢失单独使用 CODEX_RUNTIME_SESSION_LOST。

GPT 必须说明：“你的回答已经记录。Codex 可能已经收到该回答，但当前协议无法确认。Dev Bridge 不会自动重发。”即使用户要求再发一次也不得无证明重发，不得重复询问或追加修改原 UserDecision。

v1 保证 durable decision + no unproven redispatch + fail-closed unknown + canonical cancellation/successor。固定 0.149.0 和本次实测 0.159.2 均无法补取已消费但丢失的 resolved，保留 CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED；不声明 exactly-once consumption confirmation。

## 本轮基线

分支 dev_20260908_1913；HEAD fd8a6afeb213972e8aed00ff23f090ae058b8530；schema v79；index 空。逐文件 `git status --porcelain=v1 -uall` 314 条（此前默认目录折叠统计 292 条）；1821 个文件的字节快照及 SHA-256 manifest：`/private/tmp/devbridge-unknown-baseline-bhnp6n1r`。本轮增量仅对该快照计算。

## 实际生产边界

`internal/agentruntime/response_disposition.go` 是唯一 disposition/projection 解释器。`Request.State` 与 `Request.Delivery` 是当前权威字段，`Request.Projection` 是派生只读视图；attempt/evidence 集合是不可变审计事实，不是第二份当前状态。新记录不再把 ownership/session loss 与 response UNKNOWN 合并。已取消 request 可以保持 Delivery=UNKNOWN：取消证明 operation 安全结束，不证明回答未被消费。

v80 的 `runtime_response_dispatch_attempts` 记录完整 identity、原决定、backend request/item、response bytes/SHA-256、command request ID、startedAt 和 recovery evidence ID。dispatch command 复用原 UserDecision 的 SubmissionRequestID；恢复传输有新的 attempt ID。`runtime_response_evidence` 记录 EXACT_PENDING_REPLAY、PRE_EFFECT_REJECTED、SERVER_REQUEST_RESOLVED、TRANSPORT_OUTCOME_UNKNOWN。两表 append-only，FK 保留历史请求及决定；intent 与 snapshot 同事务。retry proof 只可使用一次，而且必须绑定前一 attempt，后续 attempt 不得改变原 bytes/hash/decision。

`BeforeDispatch` 持久化 intent 成功后才允许 wire write。intent 失败尚未进入 effect；独立连接静默 detach，避免发送隐式 error response。wire write 成功只意味着 UNKNOWN；exact resolved 与当前请求关联后才写 CONSUMED。resolved 持久化失败仍以 durable state 为准，不能用内存中曾收到通知推进 workflow。

Coordinator 和 Inspection runner 在后续结果返回后保留实时 owner，并等待精确 disposition 或 canonical cancellation。Codex Client drain 通知后，UNKNOWN 仍保留原 Session 的通知路由，允许迟到 resolved。SQLite Run/finalization/Inspection 写事务再次检查阻断，防止仅绕过 executor 就能推进。原结果保存在原 Run 的 runtime Items 观察中。Core shutdown 不把 UNKNOWN 终态化；有独立 binding 的原 terminal Turn 也可重连观察，保持 UNKNOWN，不 cold resume 或创建 Turn。

`runtime_request_recovery` 对外只接受 exact identity、runtimeRequestId、commandRequestId 与三个 action。CONTINUE_WAITING 是纯读；取消/调整任务调用已有 cancellation service。只有原 terminal/quiescence/finalization 的权威证据完成后才把 request 标为 CANCELED；原回答与 attempt 不变。调整任务返回 successorRequiresConfirmation，接着走原有 purpose-scoped confirmation。恢复接口不创建新 revision/Run，不接受答案或 force/retry flags。

`operation_get(include=RUNTIME_REQUESTS)` 的每个 request 包含 `projection`：lifecycleState、responseDisposition、ownerOperation、userDecisionRecorded/summary、dispatch/resolved 时间、instance/thread/turn/recovery 状态、reasonCode、resendAllowed、availableActions 和 lastObservedAt。WebUI/Console、Desktop 内嵌 workbench、ChatGPT Card、MCP/JSON 使用同一 Core 视图。UNKNOWN 隐藏答题表单；原问题和原答案仍可读。通知/wakeup 与消费状态分离。

## 验收口径

生产 facade 故障 fixture 实际消费并记录 response，然后抑制 resolved，继续产生 structured result/terminal；用于四类 owner 的 gate 阻断与取消、迟到 receipt、同文多请求隔离、不同 Run 隔离。它是协议 fixture，不冒充真实模型或真实 App Server。

真实固定 App Server 离线测试仅将 Responses provider 替换为本地确定性 fixture。新增故障注入在生产 RuntimeRequest store 边界：

- intent 持久化前拒绝，服务器仍 pending，Core B 重连并发送原决定；
- intent 已提交但调用方未取得成功结果，wire 未进入；Core B 必须先取得同实例 exact pending replay 才能重试，保留两次 attempt；
- 服务器消费并继续，但 resolved receipt 持久化失败；Core B 无可补取 receipt，必须 UNKNOWN，禁止第二次 response，保留原阶段和 claim。

第三项精确证明的是 durable receipt 丢失窗口；wire 上直接抑制 resolved 由协议 fixture 覆盖，两类故障不混写成“真实模型协议能够补取确认”。所有真实模型 smoke 默认未运行。

## G1–G13 证据对应

| Gate | 实际路径 |
|---|---|
| G1 | 原 UserDecision production Implementation/Verification/Review/Inspection 显式回答；真实 native continuity 的 waiting/recorded 恢复收到 exact resolved |
| G2 | `TestRuntimeUnknownOfflinePreEffectRejectedCoreReopen`：intent 失败无 wire effect、静默断连、原实例重投递、原决定仅一次有效 dispatch |
| G3 | `TestRuntimeUnknownOfflineConsumedReceiptLostCoreReopen`：真实 native 消费后 receipt 落库故障、Core 重开；另有直接抑制 resolved 的协议 fixture |
| G4 | 所有 UNKNOWN production 合同断言 ordinary submission/response 拒绝；HTTP/SDK adapter 拒绝；同 command 不同 payload 为 REQUEST_CONFLICT |
| G5 | `TestRuntimeUnknownOfflineExactPendingProofCoreReopen`：原 intent durable 但 wire 前故障，同实例 pending replay 后第二 attempt、同 UserDecision/Run/Turn、CONSUMED；store 测试拒绝重复使用 proof |
| G6 | `TestRuntimeUnknownProductionLateExactResolved`：terminal 后错误 request 通知不改变 UNKNOWN，迟到 exact resolved 恢复原结果，无第二 dispatch；store epoch fencing 拒绝旧 callback |
| G7 | 四类 owner 后续 output/terminal 保持原 phase/claim；runtime/workflow 专项分别覆盖 active、idle、terminal、structured result、unsolicited interrupted |
| G8 | UNKNOWN 下正式取消，保留原 UserDecision/attempt/UNKNOWN 消费事实；active 与已观察 terminal 两种窗口均验证 |
| G9 | UNKNOWN STOP_AND_REVISE 先取消并释放原 authority，再复用 purpose-scoped confirmation 创建新 revision/Execution/Turn；旧 request 拒绝写入 successor |
| G10 | 同 Run 同文两个 request 一 unknown 一待答，另有两个独立 Run 同文隔离，第二条消费不改变第一条 |
| G11 | Implementation、Verification、Coding Review、Inspection 的生产 facade/SQLite/runtime supervisor 故障测试；均保持原 owner、阻止正常推进并走正式取消 |
| G12 | 原问题/答案与 Core projection；真实本地 HTTP + SQLite/facade 回读/拒绝/等待/取消，真实 MCP SDK admission/projection/actions；动态 Workbench（Console/Desktop 内嵌）与 Card follow-up failure；禁止 force/retry/assumeNotConsumed |
| G13 | 原真实 continuity、原 UserDecision、Coding/Direct/Upper、v76/v77/v78/v79/v80、七项 legacy 与七项安全回归、定点 race、root/Desktop compile、vet、JS、diff-check，详见最终结果表 |

协议 fixture 的 owner 场景使用生产 stdio Client；真实 native 的 binding、进程身份与 Core reopen 由单列离线测试验收。HTTP 使用真实 loopback listener 的 local operator admission；MCP adapter 测试使用真实 SDK 和 caller scope，但 service fixture 只证明 adapter 契约，不代替 SQLite 业务验收。没有把源码正则、无匹配、helper-only、compile-only 或 SKIP 当成运行 gate。

## 保留边界

`CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED` 保留。pending 重连与“已消费但 receipt 丢失”是不同能力；后者安全停留 UNKNOWN 正是本轮接受的 v1 行为。

保留 `MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`、`TRUSTED_EXECUTOR_MODEL_V1`、`CODEX_SANDBOX_HARDENING_DEFERRED`、`PARTIAL_IMPLEMENTATION`、`SAFE_REPOSITORY_UNLINK_CLOSED`、`MANUAL_DESKTOP_ACCEPTANCE_PENDING`、`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`。

真实模型 smoke 均未启用：`REAL_CODING_AGENT_E2E_NOT_RUN`、`REAL_CODEX_REQUEST_E2E_NOT_RUN`、`REAL_CODEX_RESPONSE_UNKNOWN_E2E_NOT_RUN`。没有调用收费模型、修改 live DataRoot/全局配置或 Codex binary，没有 Desktop UI 操作。

## 开工调用链与历史分类

开工 v79 `RuntimeRequest` 已有 State、Delivery、Resolution、ResolvedAt、OriginalIdentity、PendingReplayVerified 和 UserDecision，但没有 dispatch attempt ledger，也没有统一 responseDisposition projection。UserDecision 绑定原 identity 与 submission request ID，v78 已不可变；AppServer binding 包含 instance/epoch、进程启动标识、binary/schema hash、socket 与 DataRoot dev/inode。原始完整定义保存在本轮字节快照；摘录位于 `/private/tmp/devbridge-unknown-final-audit/starting-contracts.md`。

旧链路：facade → durable SubmitUserDecision → Session.resolve → BeforeDispatch → transport write → Delivered → generic UNKNOWN；resolved 通知映射 RESPONDED。closePending、store.Read 的连接丢失覆盖又复用了 UNKNOWN，Console/Workbench 各自解释 State/UserDecision。旧 terminal 路径会关闭 Session，使迟到 receipt 无路由，且 workflow 没有统一 gate guard。本轮分别修正这些权威边界。

v80 不重写旧 JSON、决定或 migration；读取时仅将有 durable facts 的旧 NOT_SENT、DISPATCHING、TRANSPORT_WRITTEN、RESPONDED+resolvedAt 归一化，无法证明的 generic UNKNOWN 标为 LEGACY_RECOVERY_REQUIRED 并阻止推进，不制造 consumed 或 retry authority。重复 migration、失败 migration transaction rollback、历史 hash/FK、append-only ledger 和原回答 bytes 均单列测试。

## 最终运行记录

以下顶层/子测试数量分别统计，不把父子相加后冒充独立场景。每行对应完整日志；重复覆盖不累计成一个夸大的“总通过数”。

| 执行集 | 实际结果 | 日志 |
|---|---|---|
| Coding/Direct/Upper + UserDecision + UNKNOWN + facade | 57 顶层、36 子测试 PASS；其中 1 个 AppServerHelper 普通调用无业务执行，明确排除运行 gate | `/private/tmp/dbunknown-production-final.jsonl` |
| 最终 runtime acceptance | 21 顶层、22 子测试 PASS、0 SKIP：continuity 3/5，native UNKNOWN fault 3/0，UNKNOWN production 7/6，原 UserDecision 8/11 | `/private/tmp/dbunknown-runtime-acceptance-final.jsonl` |
| 真实 HTTP/SDK 与额外字段拒绝 | 5 顶层、6 子测试 PASS；HTTP 生产 SQLite 1 顶层，另含 facade/SDK/decoder | `/private/tmp/dbunknown-adapters-final.jsonl` |
| v76/v77/v78/v79/workset/history | 5 顶层、3 子测试 PASS | `/private/tmp/dbunknown-history.jsonl` |
| 最终 conflict + v80/状态 | 6 顶层、8 子测试 PASS：controlapi 2/2、runtime 2/6、SQLite 2/0 | `/private/tmp/dbunknown-conflict-final-rerun.jsonl` |
| 七 legacy + 七安全回归 | 14 顶层、9 子测试 PASS | `/private/tmp/dbunknown-legacy-current.jsonl` |
| 定点 race | 22 顶层、21 子测试 PASS，0 race warning：runtime 2/6、SQLite 3/3、controlapi 6/5、execution 5/4、codex 6/3 | `/private/tmp/dbunknown-race-final.jsonl` |
| JS | 5 个 Workbench/Card/hash/wakeup 测试 + 1 个 Product UI 行为测试 PASS；两个改动脚本 syntax PASS | `/private/tmp/dbunknown-js-final.log`、`/private/tmp/dbunknown-ui-final.log` |
| root/Desktop 全包编译 | PASS；仅 compile-only，不计 runtime acceptance | `/private/tmp/dbunknown-root-compile-current.log`、`/private/tmp/dbunknown-desktop-compile-current.log` |
| 定点 vet | PASS，exit 0 | `/private/tmp/dbunknown-vet-final-rerun.log` |
| diff-check/index | diff-check PASS；index 空 | 最终审计 `index.txt` |
| 真实模型 | 三项 NOT_RUN，未开启 smoke，不把 SKIP 计 PASS | 见保留边界 |

复现命令使用 `GOCACHE=/private/tmp/devbridge-go-cache`。真实离线组另设置 `DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY` 为已安装 native 路径 `/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`，不设置任何真实模型 smoke 开关。

```sh
go test -p 1 ./internal/controlapi -run '^Test(RuntimeUnknownProduction|RuntimeUserDecisionProduction|UnknownResponseFacade|UnknownRecovery|MultiRepositoryCoding|MultiRepositoryDirect|MultiRepositoryUpper|UpperV1Facade)' -count=1 -timeout=240s -json
go test -p 1 ./internal/controlapi -run '^TestRuntime(ContinuityOffline|UnknownOffline|UnknownProduction|UserDecisionProduction)' -count=1 -timeout=240s -json
go test -p 1 ./internal/controlapi ./internal/mcpserver ./internal/bridgehttp -run 'Test(RuntimeUnknownProductionHTTPAdapter|UnknownDispositionActualSDKAdapters|UnknownRecoveryHTTPRejectsRedispatchFlags|UnknownResponseFacade|UnknownRecoveryUsesCanonical|RuntimeUserDecisionTool|RuntimeUserDecisionInstructions)' -count=1 -timeout=120s -json
go test ./internal/storage/sqlite -run 'TestAppServerContinuityMigration|TestAppServerContinuityReopen|TestV76Preserves|TestProjectCentricMigrationPreserves|TestRunRuntimeWorksetStore|TestResponseDisposition' -count=1 -timeout=90s -json
go test -p 1 ./internal/controlapi ./internal/agentruntime ./internal/storage/sqlite -run 'Test(RuntimeUnknownProductionLateExactResolved|ResponseDisposition|ResponseDispatch|RuntimeUserDecisionProductionImplementationVerificationDelivery)' -count=1 -timeout=120s -json
go test -race -p 1 ./internal/agentruntime ./internal/storage/sqlite ./internal/controlapi ./internal/execution ./internal/codex -run 'Test(ResponseDisposition|ResponseDispatch|AppServerContinuityReopenExactPendingDecision|RuntimeUnknownProduction(LateExactResolved|CancelAndSuccessor|HTTPAdapter)|RuntimeUnknownOfflineConsumedReceiptLostCoreReopen|UnknownResponseFacade|UnknownRecoveryUsesCanonical|RuntimeSupervisorIndependent|LateCancellationOutcome|RPCDetach|Rejoin|AppServerQuiescence|DetachedStartTurn|RecoveredTerminalRead)' -count=1 -timeout=240s -json
go test -p 1 ./... -run '^$' -vet=off -timeout=120s
# 上一条分别在 root 与 desktop Go module 执行。
go vet -p 1 ./internal/agentruntime ./internal/storage/sqlite ./internal/execution ./internal/codex ./internal/controlapi ./internal/mcpserver ./internal/bridgehttp ./internal/probeexecution ./internal/controlplane
node --test internal/bridgehttp/product_session_user_decision_test.js internal/mcpserver/dev_bridge_runtime_request_wakeup_test.js
node --test --test-name-pattern='runtime user decision uses' internal/bridgehttp/product_ui_test.js
node --check internal/bridgehttp/ui/product-session-workbench.js
node --check internal/bridgehttp/ui/product-app.js
git diff --check
```

legacy/safety 14 项使用 `/private/tmp/db-unknown-legacy-regex.txt` 中的完整正则运行 `go test ./internal/execution -run '<该正则>' -count=1 -timeout=120s -json`。七个原 execution fixture 与七个 cancellation/restart/stale-callback 场景均实际命中。

### 失败记录与继承问题

本轮早期磁盘不足导致部分 Git fixture、native schema generation、race/normal build、vet 失败，保留原日志，不将失败改写为 PASS。只清理文件 birth time 晚于本轮基线的 Go cache blob；三个 cleanup manifest 位于 `/private/tmp/dbunknown-owned-cache-cleanup-*.json` 和 `...-second.json`、`...-third.json`，没有清理继承缓存、非本轮进程或用户目录。受影响要求由表中成功复验覆盖。

native 首次 G2/G5 验收因测试把 resolved 到达误当作 provider 后续调用已完成而失败（实际 resolved 已成功，provider calls=1）。修正为等待独立的 provider 观察后，两项完整复验 PASS；不以 provider 状态作为生产 disposition authority。早期 HTTP recorder 缺少真实 listener admission，以及错误使用 AI Session route 的 scope 失败，改为真实 loopback listener + 正式 operator workbench routes 后完整 PASS。

额外的全 legacy race 运行在 fixture 的 20 秒 migration deadline 与整组 120 秒时限处失败：`/private/tmp/db-unknown-legacy14-race.jsonl`。不声称该整条命令通过；同一 14 项普通运行通过，本轮修改的 workflow/transport/production unknown 定点 race 通过。

继承问题单列：旧 MCP `RuntimePayloadSurfaceActualSDK` / `RuntimePayloadProductionAdapters` 的 Session admission fixture 仍不满足现行准入合同；旧广泛 execution suite 的 frozen-backend fixture 与 approval-correlation 等待问题沿用先前记录。本轮不把这些旧 suite 的失败归入新 UNKNOWN gate，也不宣称全仓 runtime suite 通过。新 SDK test 使用实际 Session admission，真实 HTTP 使用 operator admission，不能用旧 fixture 的失败抵消新的成功验收。


最终 payload 冲突校验后的真实 native G3 独立 race：`TestRuntimeUnknownOfflineConsumedReceiptLostCoreReopen` 1 顶层 PASS、0 SKIP、0 race warning，测试 31.43s，日志 `/private/tmp/dbunknown-current-native-race.jsonl`。命令为相同离线 native/GOCACHE 环境下 `go test -race -p 1 ./internal/controlapi -run '^TestRuntimeUnknownOfflineConsumedReceiptLostCoreReopen$' -count=1 -timeout=120s -json`。

## 最终交接与增量

Starting / Final branch 均为 `dev_20260908_1913`；HEAD 均为 `fd8a6afeb213972e8aed00ff23f090ae058b8530`；index 均为空。schema source/test fixture v79 → v80；没有打开或迁移用户 live SQLite。

继承逐文件 dirty 314 条（旧默认目录折叠口径 292 条）；最终逐文件 dirty 338 条。本轮基线及完整文件 bytes 位于 `/private/tmp/devbridge-unknown-baseline-bhnp6n1r`。最终 SHA-256 manifest、每文件增量、统一 patch、branch/head/index/status 与起始 schema 摘录位于 `/private/tmp/devbridge-unknown-final-audit/`；增量只相对这轮基线计算。

<!-- UNKNOWN_FINAL_INCREMENT -->
本轮变化 72 个文件（新增 19 个、删除 0 个），相对开工 bytes +137890；新增 2814 行、删除 94 行。该统计包含本报告，不混入继承 dirty bytes。

未暂存、提交、合并、push、reset、clean、checkout 或 rebase 正式仓库；所有 Git mutation 均限测试自有 fixture。
