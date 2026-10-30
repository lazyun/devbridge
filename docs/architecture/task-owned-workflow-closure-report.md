# Task-owned Workflow Architecture Closure 交付报告

集成说明：本次开发直接发生在 `dev_20260908_1913`，后续按用户授权提交到该分支；没有本任务的独立工作分支。下文 HEAD、git status 和未提交说明均为实施验证完成时的快照。

本次交付把 FeatureArea engineering workflow 收口为 Task-owned。实现保留在工作区，未 commit、push、tag 或 release。当前架构以 [v44 contract](/Users/phoenix/Documents/devbridge/docs/architecture/task-execution-v0.1.md) 为准。

1. **Starting HEAD**：`08129744efd52613e69706671cab80cb8978effc`；开始时核验分支 `dev_20260908_1913`、HEAD 与用户 baseline 一致、工作区干净。最终 HEAD 和分支仍相同。
2. **Final schema**：v44；唯一默认目标见 [migration entry](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/migrate.go)、[schema v44](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/schema_v44.sql)。
3. **最终 ontology**：Project → FeatureArea → Task；Task → Review [0:N]，每次 Review 一个 immutable Run；Task → Execution [0:N]，每个 Execution 最多一个 Implementation 和一个 Verification Run。`agent_executions` 是跨 operation 的运行时诊断/取消 envelope，不是第二个 semantic Task。
4. **Task status / phase**：status 为 ACTIVE / SUCCEEDED / CANCELED / FAILED；独立持久化八个命名 phase，见 [Task phase](/Users/phoenix/Documents/devbridge/internal/task/phase.go)。Review/Execution 数量不参与 phase 状态空间。
5. **Task phase transition**：见下表。关键 gate、phase、指针和 receipt 由 canonical SQLite transaction 一起写入。
6. **创建时序**：`task_create` 先 durable 创建 title/goal、problem、constraints、FeatureArea/repository 精确归属和请求 receipt；返回 ACTIVE / REVIEW_REQUIRED、START_TASK_REVIEW。无 Review、无 Execution、无 implementation-authoritative revision。请求重放在可变运行前置条件之前读取既有 receipt；同 requestId 改意图冲突。证据：[CreateIntent/GetIntent](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_create.go)、[TaskCreateCore](/Users/phoenix/Documents/devbridge/internal/mcpserver/adapter_core.go)、[跨 transport 重放测试](/Users/phoenix/Documents/devbridge/internal/localmcp/task_gate_decision_integration_test.go)。
7. **Review ownership / cardinality**：reviews.task_id NOT NULL；Project/FeatureArea/repository 与 Task 精确一致；UNIQUE(task_id,ordinal)。正式 admission 原子创建 Review、Run、claim 和请求 receipt；linked Run 不可换绑，runs.review_id 唯一。证据：[Review store](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/review_store.go)。
8. **current/latest Review fence**：Task.current_review_id durable 保留 terminal Review；新 admission 按 ordinal 更新。数据库 pointer/owner guard 和 transaction 阻止回退及错绑，不用 created_at 猜 latest。
9. **Approval latest-only**：business subject = TASK_REVIEW_GATE / Task.ID；source Review 单独保存为 provenance。新 transition 要求 ACTIVE、REVIEW_APPROVAL_PENDING、source_review == current_review；旧审批返回 STALE_REVIEW_APPROVAL。外部无 reviewId 选择参数。已提交 receipt 的精确重放只返回原结果，不再次授权历史 Review。证据：[Approval resolver](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/review_approval_store.go)、[receipt identity](/Users/phoenix/Documents/devbridge/internal/decisionresolution/decision.go)。
10. **Approval 不创建 Task**：resolver 无 INSERT INTO tasks；APPROVE 更新既有 Task、写入 initial revision 和 receipt；测试在审批前后断言 Task count 均为 1、Execution count 为 0。见 [approval transaction tests](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/review_approval_store_test.go) 和 [端到端测试](/Users/phoenix/Documents/devbridge/internal/localmcp/task_owned_integration_test.go)。
11. **初始 TaskDefinitionRevision**：仅当前 gate APPROVE 时冻结原始意图/约束与 approved proposal，并写 exact approval provenance；不是 Task 创建或每次 Review 时。revision 保持 immutable。
12. **Execution cycle**：Task.current_execution_id durable fence；每轮绑定 exact Task + current immutable revision。初次 explicit `execution_start` 原子写内部 authorization、Execution、current pointer、Run、claim 与 IMPLEMENTING。Acceptance 已授权 successor 的既有 write-side continuation 保留。证据：[Task execution reservation](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_execution_start.go)。
13. **Run cardinality**：一个 Execution 的 Implementation [0..1]、Verification [0..1]；execution/purpose 唯一约束不变。backend retry / reconnect / response loss 复用业务 Execution/Run，不以新 cycle 表达 retry。
14. **REQUEST_CHANGES**：同一 Task，新 immutable revision、新 Execution、IMPLEMENTATION_READY；保留原 goal/constraints 和非扩张 scope 边界。历史 Execution/Run 不换绑。
15. **REQUEST_REVERIFICATION**：同一 Task、同一 revision，新 verification-only Execution、VERIFICATION_READY；新 cycle 无 Implementation Run。14/15 见 [Task Acceptance store](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_acceptance_store.go)、[cycle/cold resume tests](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_owned_workflow_test.go)。
16. **Infra failure**：Review/Implementation/Verification infra failure 保持 Task ACTIVE 和原 phase；transport/report/acceptance 分层。安全终态后可由明确 Acceptance 请求 changes/reverification 或关闭 Task；no-Run successor 取消后也可显式继续。UNKNOWN/UNKNOWN_ORPHANED 的物理后端隔离约束保留，不是 legacy workflow fallback。证据：[canonical phase transitions](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_phase.go)、[restart / infra adapter test](/Users/phoenix/Documents/devbridge/internal/localmcp/approved_continuation_integration_test.go)。
17. **Review pseudo Task 清理**：active schema Task type 只允许 DEVELOPMENT / INSPECTION；移除 TypeReview、work_review_ 创建、review_task_id、Review→Task materialization。Project Inspection 内部 operation 表示保留；它不进入 FeatureArea semantic Task。见静态核对条目 24。
18. **FeatureArea lifecycle**：保持 DISCOVERING / ACTIVE / RETIRING / RETIRED / DECOMPOSED；未新增 workflow 状态。DISCOVERING 允许 Task；首个 Task gate APPROVE 在同一 transaction 激活 FeatureArea。Task terminal 不代表 FeatureArea retire。
19. **Workspace / AgentConversation**：仍由 FeatureArea canonical ownership 管理；Review 和多 Task / 多 Execution 复用；同区域一个 active operation，不同区域并行。见 [three-Task continuity integration](/Users/phoenix/Documents/devbridge/internal/localmcp/feature_area_continuation_test.go)、[claim / conversation tests](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/agent_conversation_store_test.go)。Project Discovery 与忙碌区域 context 隔离仍有覆盖。
20. **API / tools**：task_create 真正创建 Task；task_review_start 精确 Task；移除 featureArea_review_start alias；review_approval_respond / decision_respond 的 Review 分支要求 Task identity；execution_start 以 Task 为主体。旧 app-only ui_decision_get、ui_task_decide、ui_execution_decide 已删除，旧独立 resolver 已删除；runtime permission UI 保留。见 [interface reference](/Users/phoenix/Documents/devbridge/docs/interfaces.md)。
21. **Guidance**：直接读 Task.phase / current pointers，再叠加 lifecycle、runtime permission、recovery 可用性限制；FeatureArea 只汇总 current Task。Read 不启动 backend；Review gate 未满足的安全终态可再次 START_TASK_REVIEW；终态 Task 为 WORKFLOW_COMPLETE。
22. **Card / Console / JSON**：Task panel/inventory 的 currentTaskId、title、status、phase 指 semantic Task；selectedTaskId 在 Review 阶段仍为 Task.ID。五段 rail 只机械映射 Task.phase/status。子 Run 是 Activity/Details；旧 TaskPlan/Execution 按钮不可触发。Review wakeup 校验 Task gate identity，但本次不完成生产 host 集成。证据：[phase-only rail](/Users/phoenix/Documents/devbridge/internal/workflowpresentation/task_phase.go)、[child-history independence tests](/Users/phoenix/Documents/devbridge/internal/workflowpresentation/task_phase_test.go)、[Card tests](/Users/phoenix/Documents/devbridge/internal/mcpserver/dev_bridge_ui_test.js)。
23. **Schema / migration 限制**：fresh DB 直接 v44；无 Task 历史且其它数据满足新结构的 v43 可原子重建并保留合法数据。含 pre-Task workflow 的 v43、其它旧开发版本或缺失最终契约的同版本数据库明确拒绝启动，提示显式重建开发 DB；没有猜历史 phase、双写或兼容 reducer。本次未重建/打开 live DB。见 [migration implementation](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/final_schema_migration.go)、[migration tests](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_owned_schema_test.go)、[startup rejection preserves history](/Users/phoenix/Documents/devbridge/internal/controlplane/agent_execution_legacy_startup_test.go)。
24. **Legacy authority 静态核对**：搜索 TypeReview、task_type='REVIEW'、review_task_id、work_review_、review_workflow_sources、featureArea_review_start。目标生产 Go 与 schema v44 无上述旧 Review Task path；命中仅 historical schema_v43.sql / historical migration fixtures。review_workflow_sources 已重构为 task_review_sources 并有 exact Task owner trigger。内部旧 TaskPlan 接口没有外部授权入口，DEVELOPMENT mutation 被 canonical guard 拒绝；没有新/旧两条 engineering authority。通用 operation activity reducer 和 Project Inspection 不负责 Task.phase。完整搜索记录见下方验证附件。
25. **Modified files**：见 [完整工作区文件清单](/Users/phoenix/Documents/devbridge/docs/architecture/task-owned-workflow-changes.txt)，包括生产代码、schema、fixture/契约测试和文档。无其它 worktree 修改。
26. **Go tests**：最终统一命令与逐包结果见验证附件；覆盖用户列出的 11 个核心包，以及 decisionresolution、controlplane、run、scopereview、scopeexpansion、workflowtest。所有数据均临时、后端均 fake。
27. **Race tests**：Task、Review、decisionresolution 全量 race 通过；Task phase、Review succession / admission、Approval、Acceptance、Execution transition 采用 SQLite / Control API / Execution / ExecutionProposal 的 focused race。额外尝试的全 SQLite race 在历史 `TestMigrationsUpgradeV3ThroughV9ToV14` 的 v43 重建路径触发 240 秒总时限，未出现 data race 报告；不把该超时计为通过。普通全量 SQLite 回归通过。最终 focused 结果见附件。
28. **Node tests**：六个 MCP Card 文件：240 项、236 pass、4 个环境 fixture gate skip、0 fail。含 exact Task selection、过期按钮/导航 fencing、请求摘要异步替换、response-loss 同请求重放、runtime approval delivery 分离及旧工具不可用。跳过项不作为 host 集成通过证据。
29. **go vet**：对全部受影响核心和上述关联包执行，结果见附件。
30. **git diff --check**：最终执行，结果见附件。
31. **Final git status**：所有改动保留为 unstaged / untracked；分支与 HEAD 未动。完整 status 见验证附件；没有自动 commit。
32. **SCOPE_CONFLICT**：无。未破坏 canonical Workspace / AgentConversation ownership、Review Run 唯一、Execution purpose 唯一、revision immutable 或 Acceptance 已冻结语义。
33. **Retire**：Retire UI 未改。后端 retirement/cancellation 有本架构必需的适配：取消子 operation 不自动 terminal Task；显式 FeatureArea retirement 在 exact operation quiesced/fenced 后关闭剩余 ACTIVE Task。相关取消/退休回归仍覆盖，因此不能称“Retire 完全零改动”。
34. **FAST/DEEP**：未实现 depth enum / policy。本结构允许未来通过同 Task 的多次 Review 扩展，Review 1:1 Run 不需改变。
35. **Production recall host**：仍为 **HOST_INTEGRATION_PENDING**。未调用用户设备、真实 Codex 或完成生产 host recall 验证。
36. **交付边界**：明确未 push；未 commit、tag、release；未调用真实 Codex、访问外网、live DB、设备 UI 或修改其它 worktree。

## Phase transition table

| Trigger | Before | After / atomic facts |
| --- | --- | --- |
| task_create | 无 Task | ACTIVE / REVIEW_REQUIRED + intent receipt |
| task_review_start | REVIEW_REQUIRED 或 terminal-unsatisfied REVIEWING | REVIEWING + Review + Run + current_review_id + claim + receipt |
| 当前 Review gate satisfied | REVIEWING | REVIEW_APPROVAL_PENDING + terminal result + gate decision |
| gate 不满足 / infra failure | 原业务 phase | phase 不推进；等待安全重试/恢复 |
| APPROVE | REVIEW_APPROVAL_PENDING | IMPLEMENTATION_READY + initial immutable revision + receipt；无新 Task/Execution |
| REVISE | REVIEW_APPROVAL_PENDING | REVIEW_REQUIRED；下一次 Review 必须新建 |
| REJECT review gate | REVIEW_APPROVAL_PENDING | Task CANCELED |
| execution_start | IMPLEMENTATION_READY | IMPLEMENTING + current Execution + Implementation Run + claim |
| Implementation success | IMPLEMENTING | VERIFICATION_READY；明确 user waiver 可直接 ACCEPTANCE_PENDING |
| Verification start | VERIFICATION_READY | VERIFYING + Verification Run + claim |
| Verification report completion | VERIFYING | ACCEPTANCE_PENDING |
| ACCEPT | 满足 Acceptance gate | SUCCEEDED |
| REJECT acceptance | 安全终态 Acceptance gate | CANCELED |
| REQUEST_CHANGES | 安全终态 Acceptance gate | 新 revision / Execution，IMPLEMENTATION_READY |
| REQUEST_REVERIFICATION | 安全终态 Acceptance gate | 同 revision，新 verification-only Execution，VERIFICATION_READY |

关键原子性、并发 exactly-one、stale completion、latest-only Approval、初始 revision 时点、两类 successor、cold restart、response-loss 和 rollback 的集中测试：[Task-owned store tests](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_owned_workflow_test.go)、[cross-adapter gate tests](/Users/phoenix/Documents/devbridge/internal/localmcp/task_gate_decision_integration_test.go)。

## 最终验证附件

验证命令均使用 `GOCACHE=/private/tmp/devbridge-task-owned-go-cache GOPROXY=off GOSUMDB=off`；Go 使用 `-p 1 -count=1`。loopback 所需执行权限只用于临时测试 server。

- [Go 全量结果](/private/tmp/task-owned-go-complete.log)
- [最终 Task progression focused race](/private/tmp/task-owned-race-final-focused.log)
- [额外全量 race 尝试：小包通过，历史 migration 超时](/private/tmp/task-owned-race-complete.log)
- [Execution transition focused race](/private/tmp/task-owned-final-race.log)
- [Node 结果](/private/tmp/task-owned-node-final9.log)
- [go vet](/private/tmp/task-owned-vet-complete.log)
- [静态搜索 / diff check / final status](/private/tmp/task-owned-final-audit.txt)

最终验证状态：**完成**。统一 Go 回归 16 个有测试包全部通过，workflowtest 无独立测试；目标 progression race 四包全部通过，Task / Review / decisionresolution 全量 race 通过；Node 236 pass / 4 skip / 0 fail；go vet 与 git diff --check exit 0。额外全 SQLite race 的历史迁移超时如第 27 项记录。最终 199 个变更文件，全部保留工作区。

可复现命令（统一使用上面的离线 Go 环境变量）：

```sh
go test -p 1 ./internal/task ./internal/review ./internal/execution ./internal/executionproposal ./internal/workflowprogression ./internal/workflowpresentation ./internal/controlapi ./internal/storage/sqlite ./internal/mcpserver ./internal/consoleapi ./internal/localmcp ./internal/decisionresolution ./internal/controlplane ./internal/run ./internal/scopereview ./internal/scopeexpansion ./internal/workflowtest -count=1 -timeout=180s
go test -race -p 1 ./internal/storage/sqlite ./internal/controlapi ./internal/execution ./internal/executionproposal -run 'TaskOwned|TaskPhase|TaskAcceptance|ReviewApproval|ReviewAdmission|ConcurrentReview|ExecutionCycles|AtomicRollback|TaskCreate|ResumeReserved|Infra|Reservation' -count=1 -timeout=180s
node --test internal/mcpserver/*_test.js
go vet -p 1 ./internal/task ./internal/review ./internal/execution ./internal/executionproposal ./internal/workflowprogression ./internal/workflowpresentation ./internal/controlapi ./internal/storage/sqlite ./internal/mcpserver ./internal/consoleapi ./internal/localmcp ./internal/decisionresolution ./internal/controlplane ./internal/run ./internal/scopereview ./internal/scopeexpansion ./internal/workflowtest
git diff --check
```

## 2026-09-14 审批职责边界更正

本历史报告第 11、14 项的原始结论由当前用户审批合同更正：初始定义冻结
用户实际批准的准确 proposal；原始需求单独保留为创建来源。REQUEST_CHANGES
在用户明确确认完整 successor WorkSpec/VerificationSpec 后允许调整目标措辞、
自然语言限制与合法文件集合，不再要求旧文本继承或旧 scope 非扩张。
同 Task 身份、历史 revision/Execution 绑定、路径安全以及无新批准的 replay、
retry 和 resume 内容锁定保留。本更正不重述或补签历史运行验证。
