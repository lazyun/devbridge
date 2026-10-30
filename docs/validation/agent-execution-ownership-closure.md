> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# AGENT_EXECUTION_OWNERSHIP_UPGRADE_CLOSURE_READY

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-9936b6c18d847fcf)。

日期：2026-09-10。最终审查范围为 HEAD → working tree 的全部 tracked 和 untracked 文件，包含原 ownership upgrade。

本节保留上一轮 closure 的范围与测试记录。此前仅凭显式键 BEFORE INSERT guard 宣称 REPLACE 全面受保护并不完整：普通 rowid table 仍存在隐式键绕过。最终机制为 `WITHOUT ROWID` 加现有显式 identity guards；本次单点 closure 的新证据见文末，不将此前 gates 视为本次重跑结果。

## 基线与 Git

- 实际 branch：`dev_20260908_1913`。
- Starting / final HEAD：`1d16dc7feaac471fec705e4d810aeac7dc9d05a1`，未变化。
- 开始时保留原任务的 75 项未提交变更；未先提交、stash 或切换 worktree。
- Source schema 为 v42；migration 和恢复只操作测试临时数据库。
- 最终 staged 0、tracked modified 79、untracked 28，共 107 个文件（`git status --porcelain -uall`，包含本报告）。
- 未 commit / push / release，未访问 live SQLite、正式 8081、真实设备或模拟器。

## 六项 closure

| 问题 | 修复与直接证据 |
| --- | --- |
| P0 identity / REPLACE | v42 table 使用 WITHOUT ROWID，消除 rowid / _rowid_ / oid 隐式冲突面。现有 BEFORE INSERT 在 SQLite conflict handling 前对 ID 和 semantic-owner 唯一键检查所有 immutable 字段，使用 NULL-safe equality；同 identity replay 为 no-op，不同 identity ABORT。保留 UPDATE/DELETE guards，recursive_triggers 不是安全前提。本次新增无 Run 和三种 alias 的 OFF/ON 测试，证据见文末。 |
| P1 typed nil | Console owner lookup 的成功分支显式返回 nil；真实 HTTP 走 owner resolution、Core、supervisor、SQLite canonical cancellation，不再 panic。 |
| P1 no Run | DEVELOPMENT 无 Run 可省略 runId，验证 exact authorization 后 canonical NO_BACKEND 收敛；receipt 的 interrupt 是 NOT_SENT、quiescence 是 NO_BACKEND。Web capability/button、GPT/Local MCP 同步；不创建 synthetic Run。 |
| P1 legacy startup | InterruptRun、orphaned plans、probe recovery、Review reconciliation、claim recovery 跳过 LEGACY_UNKNOWN；已知 envelope 引用 unknown 当前 Run 也不参与 orphan terminalization。真实 v41 active unknown/no-claim fixture 连续打开 ControlPlane 两次，Run/Task 原始字段不变、无新 claim/receipt、零 fake-backend invocation、foreign_key_check 清洁。旧 pending receipt 的 restart 测试另行证明未分类、未改写、未释放 claim。 |
| P1 universal MCP | GPT/Local `execution_get` 先读 session-scoped Core universal projection，再补 DEVELOPMENT-only authorization；`execution_list` 从同一 store 枚举四类。删除 externalExecutionStatus；兼容 workflow map 只消费 Core status。COMPLETED 不再重算成 SUCCEEDED；Core recovery-required 不被 BUSY 展示覆盖。 |
| P2 docs | README、interfaces、当前 architecture 文档统一 Task / AgentExecution / Run / semantic owner / ExecutionProposal 定义与四类链路；旧 implementation validation 标为历史报告，指向本 closure。 |

## Migration 与 runtime safety

- Fresh v42、actual v41 → v42、四类 historical backfill、同一 DEVELOPMENT 的 Implementation/Verification 双 Run、active unknown history、ledger replay、foreign_key_check、REPLACE negative regressions 均通过。
- 生产 Run INSERT 仅 run_store.go 和 inspection_run.go；事务中绑定 exact Execution。AgentExecution 生产写入口均为 plain INSERT，无 REPLACE 覆盖入口。
- Coordinator 的 Review、automatic first Preparation、explicit/retry Preparation、Implementation、Verification 使用同一 mandatory durable validator；Inspection 使用独立 runner 的 mandatory exact validator。
- 缺失/错误 Execution、Task、owner、Project/FeatureArea、stale epoch、LEGACY_UNKNOWN、orphan 和未绑定 Thread 在适用边界 fail closed；recording backend 证明被拒绝 handoff 的调用数为零。
- 单独的 SQLite admission、真实 SQLite corrupt-handoff、outgoing resume Thread 和每个 thread/start / resume / turn/start validation 测试覆盖底层证明。
- semantic owner、same-FeatureArea serialization、epoch、Host Tool 授权、finalization intent、TaskDefinitionRevision、Preparation 1:N 与 Approval 自动推进保持原协议。

## Cancellation matrix

| Owner / case | 结果与覆盖 |
| --- | --- |
| DEVELOPMENT pending/confirmed，无 Run | SQLite canonical、Core/MCP capability、真实 HTTP、DOM click、稳定 requestId / response-loss replay；NO_BACKEND、零 synthetic Run。 |
| DEVELOPMENT active Implementation | production supervisor + instrumented fake Turn + HTTP；仅一次 interrupt、canonical finalization、重放 receipt 稳定。 |
| DEVELOPMENT Verification | exact active Verification Run；旧 Implementation target 拒绝；既有 SQLite / supervisor fence 回归通过。 |
| DEVELOPMENT terminal | HTTP / SQLite ALREADY_TERMINAL，正常完成赢得终态时不重写历史。 |
| PREPARATION active | HTTP 首次取消，GPT/Local MCP 重放；known terminal 和 lost transport 两种路径通过。 |
| PREPARATION terminal/conflict | 终态取消保留 Run；CONFLICT 保留 domain result，返回 ALREADY_TERMINAL，不生成 implementation authorization。 |
| REVIEW active/terminal | HTTP → Core → supervisor；GPT/Local 重放、known/unknown path，以及 terminal history preservation 通过。 |
| LEGACY_UNKNOWN | 新 cancellation、旧 receipt replay、offline completion、Run mutation、claim acquisition / backend validation 无 authority。启动扫描保留历史。 |
| UNKNOWN / UNKNOWN_ORPHANED | UNKNOWN 保留 claim 至 canonical recovery；orphan 采用 quarantine + permanent fence，迟到写入拒绝，quiescence 仍 UNKNOWN，不宣称物理后端死亡。 |

同一真实 SQLite fixture 的 Core、GPT MCP、Local MCP、Console/Web HTTP 按完整 universal projection 比较（executionId、taskId、kind、owner、Run、stage、status、domain result 等）；四类 inventory 完整，无跨项目授权混入。

## Gates

- Final `go test ./... -count=1 -json -timeout=5m`：57 个有测试包 PASS；14 个包无测试；1682 个测试/子测试 PASS、8 SKIP、0 FAIL。
- affected unit/integration 包含 storage/sqlite、agentexecution、run、execution、executionproposal、taskpreparation、review、recovery、controlplane、controlapi、consoleapi、mcpserver、localmcp、bridgehttp、workflowpresentation，均由全量 gate 覆盖。
- Race：storage/sqlite、probeexecution、controlapi、controlplane、localmcp、mcpserver、bridgehttp 全包通过。execution 初轮并行负载下一个 20 秒 deadline 的 restart 测试超时，无 data race；该测试单独串行通过（12.046 秒）。整包最终串行结果：PASS（163.499 秒，0 race findings）。
- 最后补充的 identity、旧 receipt recovery、terminal cancellation、orphan plan、Core recovery projection、跨客户端和 HTTP cancellation 另行 race 验证通过。
- `go vet ./...` PASS；gofmt verification PASS；`git diff --check` PASS。
- `node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js desktop/frontend/test/*.test.js`：307 total，304 PASS、3 SKIP、0 FAIL。包括 inventory、四种 kind read、no-Run button/payload、既有 DOM/browser contracts。
- 未改模型、reasoning、Card 视觉或产品范围。Console architecture guard 仍检查全部 production import；跨 adapter integration `_test.go` 可组合真实 transport，遵循该 guard 对 UI tests 已有的 production/test 区分。

### Skipped / 未执行

Go 的 8 个 opt-in gates：real authorization ceiling、real read-only→write same-conversation、live dogfood snapshot、real read-only Review smoke，以及 Review Approval V2 / Unified / Retirement / Web Observability candidate-binary acceptance。前端 3 个 candidate-only mounted Card tests 无 candidate fixture 而 skip。遵照任务约束未发真实付费 Codex Turn；未运行设备/模拟器或发布验收。没有将这些 skip 记为 PASS。

## Final whole-diff audit

1. Raw SQL AgentExecution identity 的最终保护依靠 WITHOUT ROWID 与显式 identity guards 共同成立；此前普通 rowid table 的结论已由本次单点 closure 修正。
2. Run 不可绕过 exact envelope admission，backend capability 缺失 fail closed。
3. LEGACY_UNKNOWN startup 可继续启动，无 mutation、claim、Host Tool 或 terminalization authority。
4. Web owner lookup 不再 typed-nil panic，no-Run DEVELOPMENT canonical cancellation 正常。
5. MCP executionId 是 universal ID，四类 get/list 使用同一 Core source，无第二套 Execution reducer。
6. Core / MCP / Console / Web projection 一致；executionProposalId 只标 DEVELOPMENT subtype。
7. UNKNOWN、orphan、cancellation、recovery、finalization、quarantine 和 stale-write safety 无新协议分叉。
8. Canonical ontology 已同步，原 Task → Execution → Run 架构未重设计。
9. 上轮曾报告无未关闭 P0/P1/P2；后续 re-review 找到隐式 rowid replacement P0，最终处理与验证见文末。其余五项 CLOSED finding 未在本次重新设计或修改。

本任务独立 build/cache/temp 使用 `/tmp/devbridge-ownership-closure`，交付时清理。系统共享 Go cache 不进行破坏性清理。

## Rowid immutability final closure — 2026-09-10

### Fresh baseline 与本次变更

- Branch `dev_20260908_1913`；HEAD `1d16dc7feaac471fec705e4d810aeac7dc9d05a1`。
- HEAD 的 DefaultMigrations 结束于 v41；工作区新增的 v42 migration 文件仍未跟踪。按本任务未提交、未发布、无正式 adoption 的 v42 基线直接修正表定义，不增加 v43；未访问生产数据库。
- 开始及最终工作区均为 107 文件：79 modified、28 untracked、0 staged。本次只改 4 个已有工作区文件：v42 migration、identity regression test、本 architecture 文档和本报告；其余 103 文件与 fresh baseline 的 SHA-256 相同。
- Production 变更仅为 `agent_executions ... WITHOUT ROWID` 及原因注释。显式 ID PRIMARY KEY、semantic-owner UNIQUE、所有 INSERT/UPDATE/DELETE guards 原样保留。

### Identity 与 migration 证据

- `TestAgentExecutionWithoutRowidRejectsAliasReplacement`：真实 SQLite 在同一连接确认 `pragma_table_list.wr=1`，并分别读取确认 `recursive_triggers=0/1`。DEVELOPMENT A 已存在且没有关联 Run；合法 Review owner B 已存在但尚无 Execution。三个 alias `rowid`、`_rowid_`、`oid` 均无法读取，使用任一 alias 的 INSERT OR REPLACE 都报 column 不存在，不能形成隐藏唯一键冲突；全部既有 inventory、identity、provenance 保持一致。
- 同一 B 去掉 alias 后 plain INSERT 成功且 affected=1，随后真实 production Run admission 成功并重放既有 envelope；B identity 不变、inventory 只增加 1、A 仍无 Run，排除用非法 B/CHECK/FK 偶然阻挡 replacement 的假证明。
- `TestAgentExecutionReplaceCannotRewriteIdentity`：recursive OFF/ON × Review 有 Run、DEVELOPMENT 有 Run、DEVELOPMENT 无 Run；INSERT / OR REPLACE / OR IGNORE / UPSERT 对 10 个 identity 字段逐一改写，必须由 identity guard 拒绝。覆盖同 semantic owner 不同 ID、同 ID 不同 Task/Project/FeatureArea/kind/owner/proposal/provenance/created_at。每次断言完整 inventory、provenance 和已有 Run 不变；无 Run 场景一直为 0 references。UPDATE/DELETE 也由原 durable guards 拒绝。
- 完全相同 identity 在四种 INSERT/conflict 形式下都成功且 RowsAffected=0，是 no-op 而非 delete/recreate；每次比较全部 envelope 字段、数量及完整已有 Run。
- Fresh v42、真实 v41 → v42 known/LEGACY_UNKNOWN、四类 backfill、DEVELOPMENT Implementation/Verification 双 Run、proposal ID preservation、重复 ApplyMigrations ledger replay、foreign_key_check 由既有 AgentExecution migration 测试验证通过。ControlPlane actual-v41 unknown/no-claim 启动测试同样由全量 gate 覆盖。

### Production write 与最终窄范围审计

- `agent_execution_store.go` 为 Review/Preparation/Inspection admission 执行 plain INSERT；v42 的 proposal trigger 为 DEVELOPMENT 执行 plain INSERT。Replay 由现有 NULL-safe explicit identity comparison 控制，DEVELOPMENT admission 读取 exact existing association。没有正常路径依赖 REPLACE、IGNORE、UPSERT 或原地更新 envelope。
- Migration/backfill 仍为 deterministic plain INSERT：DEVELOPMENT 保留 proposal ID，其他三类使用既有 deterministic owner prefix，unknown 使用原 legacy-run ID；没有更改 backfill、ledger 或 FK 规则。
- AgentExecution 只有显式 ID 与 semantic-owner 两个唯一性约束；project/task indexes 非唯一。WITHOUT ROWID 移除所有隐式 alias，因此唯一性冲突全部进入既有 explicit guard，不依赖 recursive DELETE trigger。
- 所有 AgentExecution query/projection/FK 通过显式 id 关联，没有 rowid、别名或 LastInsertId 依赖。全仓剩余原有 rowid 用法仅为 v2 的 projects migration 和 v32→v33 测试旧表快照，均不涉及 AgentExecution。
- 本次窄范围静态审计未发现新增 P0/P1/P2。上一轮 typed-nil、no-Run cancellation、LEGACY_UNKNOWN startup、universal MCP、canonical ontology/docs 五项 CLOSED finding 的实现未改；全量回归通过。未更改 reducer、runtime protocol、model/reasoning、Card 或前端逻辑。

### 本次 fresh gates

- `go test ./internal/storage/sqlite -run 'TestAgentExecution' -count=1 -json -timeout=5m`：293 测试/子测试 PASS，0 FAIL。
- `go test ./... -count=1 -json -timeout=5m`：57 有测试包 PASS，14 包无测试；1871 测试/子测试 PASS、8 SKIP、0 FAIL。其中 storage/sqlite 全包 676 测试/子测试 PASS。
- 全量首轮在受限沙箱中因 `127.0.0.1:0 bind: operation not permitted` 失败；允许临时 loopback 测试端口后完整重跑，上述 PASS 来自重跑结果。
- `go test -race ./internal/storage/sqlite -count=1 -json -timeout=5m`：全包 676 测试/子测试 PASS，275.170 秒，0 race findings。
- `go test -race ./internal/execution ./internal/probeexecution ./internal/controlplane -run 'AgentExecution|UnprovenExecution|InspectionOwnership|InspectionCompatibility|V41Unknown' -count=1 -json -timeout=5m`：3 包、66 测试/子测试 PASS，0 race findings。分别覆盖 durable corrupt handoff、outgoing resume、mandatory validator、Inspection 和 actual-v41 unknown startup。
- `go vet ./...`：PASS。
- gofmt：全部 ownership 工作区变更 Go 文件 PASS。额外全仓扫描发现 7 个与 HEAD 完全相同的既有文件存在格式差异：`internal/bridgehttp/console.go`、`internal/projectcontext/bootstrap.go`、`internal/consoleapi/logs.go`、`internal/document/service_test.go`、`internal/document/service.go`、`internal/workflowstate/state.go`、`internal/controlplane/dataroot_integration_test.go`；按本次范围保留，未将全仓格式声明为 PASS。

本次没有重跑前端 gates，因为未改前端逻辑。Go 的 8 个 opt-in skips 是 real authorization ceiling、real same-conversation、live dogfood snapshot、real read-only Review、Review Approval V2 / Unified / Retirement / Web Observability candidate acceptance；未将 skip 记为 PASS。未运行 paid Codex Turn、live SQLite、正式 8081、设备/模拟器或 Release；未 git add、commit、stash、reset、push。

`git diff --check` 与全部 untracked 文件的 whitespace check 通过。本任务独立 temp/build/cache 位于 `/tmp/devbridge-rowid-closure-y6ucilsc`，完成时清理；未清理系统共享 Go cache。最终 HEAD/schema/工作区数量不变，保留全部未提交变更等待 re-review。

最终结论：**AGENT_EXECUTION_ROWID_IMMUTABILITY_FINAL_CLOSURE_READY**。

## Projection final closure — 2026-09-10

本节对应 `AGENT_EXECUTION_PROJECTION_FINAL_CLOSURE`，修正最终只读验收发现的两个 P1；不沿用前节的 readiness 或测试通过数。

### Fresh baseline 与实现范围

- Actual branch：`dev_20260908_1913`；HEAD：`1d16dc7feaac471fec705e4d810aeac7dc9d05a1`。
- HEAD DefaultMigrations 结束于 v41；working-tree source 为 v42。开始时 107 文件：79 modified、28 untracked、0 staged；tracked diff 为 +983 / -338。
- 保持 Task → semantic domain owner → AgentExecution → Run → Thread/Turn。v42 schema、identity guards、runtime admission/validator、canonical mutation、UNKNOWN/orphan 协议均不修改。
- Core Projection 的 status 使用字符串；原有 MCP 封闭 enum 无法区分拒绝与取消，因此最小补充 `REJECTED`，同步 MCP execution/result schema、legacy workflow vocabulary 和 Card phase。Web/Console 直接消费 Core 字符串与现有 capability；没有修改 JS/HTML 或视觉。

### REJECTED 与 precedence

共享 `agentexecution.Reduce` 优先保留 authoritative authorization `REJECTED`，即使 outcome 仍为合法默认值 `PENDING`。拒绝保持 `domainStatus=REJECTED/domainResult=PENDING`，不生成 cancellation facts，不转换为 CANCELED。其余结果仍按 canonical outcome 区分 pending、consumed/active、成功、失败和取消；既有 finalization、UNKNOWN/orphan 优先级保留。

Task.Work 对当前 definition revision 的最新 authorization 先做选择，再检查其 nullable Run 和真实 Run 关联。因此无 Run 的拒绝可被读到，旧拒绝也不能遮住后来已创建的 Run。该选择只决定当前展示 envelope，不用于建立 ownership 或关联 receipt。

GPT/Local MCP get/list/workflow、Core CurrentWorkflow、Task.Work、Console/Web HTTP 都读取同一个 Core projection。Console/Web 内嵌 Task.Work 的 cancellationState/backendDisposition 也直接复制该 projection，避免原始 Task 查询遗留其他 owner 的处置。REJECTED 不再满足 pending cancellation capability，CurrentWorkflow 不广告 start，真实 AuthorizeAndStart 拒绝该授权。Card 的 rejection phase 只从 Core REJECTED 映射。

### Exact cancellation readback

新增 `AgentExecutionStore.CancellationFacts(executionID, runID)` 为纯 SELECT：从 immutable envelope ID 解析 Project、Task、semantic owner kind/ID，再与 canonical `execution_cancellations` 精确连接。

- 空 runID 明确表示 SQL NULL；仅匹配 DEVELOPMENT 自身的 proposal/owner，且该 envelope 没有真实 Run。
- 非空 runID 必须匹配同一 envelope、Task、Project、FeatureArea 和 semantic owner 的真实 Run。
- 请求排序仅在该 exact owner/Run target 内进行；不按 Task/FeatureArea 最近 receipt 或最近 Run 推测。
- 不创建 receipt、不修改状态、不 reconciliation、不授予 mutation authority。
- 无 receipt 保持 NOT_STARTED/空 cancellation；无 Run canonical cancel 后回读 CANCELED/NO_BACKEND/CANCELED。已有 RuntimeFacts 继续优先保留 UNKNOWN/orphan disposition。

### 新增 regression

- `TestDevelopmentAuthoritativeTerminalPrecedence`：pending、confirmed、rejected、stale outcome、consumed active、completed、failed、canceled，拒绝不虚构取消。
- `TestRejectedDevelopmentProjectionAcrossClients`：真实 SQLite authorization → Core Reject，证明 persisted REJECTED + PENDING outcome；Core get/list、Task.Work、CurrentWorkflow、GPT/Local get/list/workflow、Console/Web HTTP、Card 均核验明确预期值；无 cancellation capability、无 receipt、不能 start。
- `TestNoRunCancellationExactOwnerReplayAcrossClients`：同 Task 旧授权先拒绝，再显式 canonical cancel，得到 ALREADY_TERMINAL/NO_BACKEND/NOT_SENT 而不取消 Task；合法新授权初始不得继承旧 receipt。随后新授权 canonical cancel 得到 CANCELED/NO_BACKEND/NOT_SENT；首次与 replay 都重新读取各客户端，并比较完整 projection 与 receipt。
- `TestAgentExecutionCancellationFactsExactNullableRun`：真实 store 同 Task 旧/新 authorization 隔离，错误 Run/缺失 envelope 为空，自己的 nullable Run receipt 稳定重放。
- `TestAgentExecutionProjectionReadsExactOrphanFacts`：额外断言 Review/Preparation/Development 的 exact Run receipt，空 Run lookup 不借用已有 Run；保留 UNKNOWN_ORPHANED。
- `TestRejectedAuthorizationDoesNotHideNewerRun`：旧拒绝后创建新 authorization 与真实 Run，Task.Work 必须指向新 Run。

### Validation

- Focused affected packages：`go test ./internal/agentexecution ./internal/controlapi ./internal/storage/sqlite ./internal/consoleapi ./internal/mcpserver ./internal/localmcp ./internal/bridgehttp -count=1 -json -timeout=5m`：7 包 PASS，1131 测试/子测试 PASS、6 SKIP、0 FAIL。之后补充的新 Run 选择回归及最终映射由下面的专项和全量 gate 覆盖。
- Affected full-package race：相同 7 包 `-race -count=1 -timeout=8m` 全部 PASS，1131 测试/子测试 PASS、6 SKIP、0 FAIL、0 data race。
- 最终 Task.Work 选择专项 race：storage/sqlite + localmcp，8 测试/子测试 PASS、0 FAIL；包含旧拒绝不得遮住新 Run。
- 最后补充的 Console/Web 内嵌 Task.Work 断言先复现了字段未同步的问题；补齐两个直接映射字段后，4 包最终专项 race（storage/sqlite、localmcp、consoleapi、bridgehttp，AgentExecution/ExecutionCancellation/跨客户端相关测试）：341 测试/子测试 PASS、0 FAIL、0 data race。
- **最终代码** `go test ./... -count=1 -json -timeout=5m`：57 个有测试包 PASS，14 包无测试；**1885 测试/子测试 PASS、8 SKIP、0 FAIL**。最后 Console/Web 修复前也曾全量通过，但本结论使用修复后的独立重跑结果。
- `go vet ./...`：PASS，无输出。
- 全部 91 个 working-tree changed/untracked Go 文件的 gofmt verification：PASS；`git diff --check` 与 untracked whitespace check：PASS。
- 最终 `node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js desktop/frontend/test/*.test.js`：307 total，**304 PASS、3 SKIP、0 FAIL**。本任务 JS/HTML 与开始快照相同，但 API/phase schema 扩展后仍重跑该 gate。
- 初次 transport 验证在受限沙箱内因 `127.0.0.1:0 bind: operation not permitted` 失败；获准使用临时 loopback 测试端口后重跑。上述通过数均来自成功执行的 gate，不将环境拒绝算作 PASS。

Go 的 8 个 opt-in skips：real authorization ceiling、real read-only→write same conversation、persisted dogfood snapshot、real bounded read-only Review、Review Approval V2 / Unified / Retirement / Web Observability candidate acceptance。前端 3 个 skips 是缺少外部候选 fixture 的 mounted Card tests；均未计为 PASS。未运行真实付费 Codex Turn、live SQLite、正式 8081、设备/模拟器或 Release。

### Final whole-diff audit 与交付

1. REJECTED 在共享 Core 中为独立终态，原始 domain facts 保留；没有映射为 CANCELED，没有 cancellation/start capability。
2. no-Run cancel/replay 从 exact canonical receipt 回读 CANCELED/NO_BACKEND；同 Task 旧 authorization receipt 不会进入当前 Execution 或 Console/Web Task.Work。
3. 有 Run 的 exact cancellation、UNKNOWN/orphan、finalization 与安全回归通过；读取 API 不提供任何 mutation authority。
4. 没有新增 client-specific Execution reducer；新 adapter 分支只映射 shared Core REJECTED 或复制 Core 字段。
5. rowid immutability、typed-nil closure、LEGACY_UNKNOWN startup、universal MCP identity、canonical ontology 未回归。v42 migration、identity regression、runtime admission/validator、canonical cancellation mutation、canonical architecture/README/interfaces 与 fresh baseline 的对应文件内容一致；Console owner lookup 的成功 nil 分支未变。
6. Final audit 无未关闭 P0/P1/P2。HEAD → working tree 的其余 ownership 实现保持，未夹带 model/reasoning 配置、视觉改造、临时 probe、secret、live DB 或 build/cache/log 产物。
7. 最终 branch/HEAD 不变，source schema v42；**108 文件：80 modified、28 untracked、0 staged**。比本任务开始增加的 tracked diff 文件是 `internal/workflowstate/state.go`，用于 REJECTED 的 shared workflow vocabulary。此 closure 共修改 14 个文件（10 production、3 tests、此报告）；原 ownership scope 的其余 94 文件与开始 SHA-256 快照一致。
8. 本任务独立临时目录 `/tmp/devbridge-projection-closure-a7o5xasj`（baseline、测试输出、Go cache）在完成后清理；不清理共享 Go cache。所有变更保持未提交，等待最终 acceptance re-review；未 git add/commit/stash/reset/checkout/push。

最终结论：**AGENT_EXECUTION_PROJECTION_FINAL_CLOSURE_READY**。

## Read projection final closure — 2026-09-10

本节对应 `AGENT_EXECUTION_READ_PROJECTION_FINAL_CLOSURE`，替代上一节关于历史 Card 和公共 Task HTTP detail 已完整闭合的结论。本轮先复现两个 production read-path P1，再修复、验证；不沿用上一轮通过数。

### Fresh baseline 与窄范围实现

- Branch：`dev_20260908_1913`；HEAD：`1d16dc7feaac471fec705e4d810aeac7dc9d05a1`。HEAD schema v41，working-tree source v42。
- 开始为 108 文件：80 modified、28 untracked、0 staged；tracked diff +1016 / -359。
- 历史 Card：`resolveUITaskWorkflow` 保留已有 session / Project / Task / preparation / proposal 校验。无 Run 时使用已验证 proposal 对应的 universal ID 读取 Core projection，不再要求它等于当前 Task.Work.ExecutionID。
- Task detail：将已有 Core `enrichTaskExecution` 公开为 `EnrichTaskExecution`，保留实现；session HTTP 在 AIContext 授权成功后调用，Console project list/detail 在共同的、已完成 Project/Task 过滤的 `makeTaskSummary` 入口调用。原 Core GetTask/ListTasks/CurrentWorkflow 复用同一 helper。
- Raw storage Work 仍是低层 snapshot；只修正其职责注释，没有修改 SQL。产品态 Execution status、domain facts、cancellationState、backendDisposition 经 Core exact owner/nullable Run projection 覆盖，不直接展示 Task 最近 receipt。
- 历史 Card 的通用 MCP 包装器不再为显式选择的终态 Execution 附加当前 Task workflow guidance，避免借用 successor 的 nextAction。该分支仅消费已有 terminal 结果，不重算 Execution 状态。
- 取消后 Card 的额外回读断言复现了 raw authorization 仍为 PENDING_CONFIRMATION 时显示 Execute/Cancel 对象的问题。现在该对象仅在 Core Execution.status=PENDING 时保留；REJECTED/CANCELED 等状态不暴露该动作对象。未改前端视觉、schema 或 mutation。

### 真实生产路径 regression

新增 `internal/localmcp/agent_execution_read_closure_test.go`，使用真实 SQLite、Core、HTTP handlers、GPT/Local MCP transports 和现有 held/fake backend fixture：

1. `TestHistoricalRejectedCardUsesSelectedExecutionAfterSuccessor`：真实 Confirm → Reject A，保留 REJECTED/PENDING 原始 facts；通过 Core Create 创建相同 Task/Preparation/revision 的 B，确认 Task.Work 已指向 B；读取 A Card 必须精确等于 Core(A)、phase=REJECTED、terminal=true，无 Run、authorization 控件、continuation 或当前 workflow guidance。读取 B 仍为自身 PENDING/NOT_STARTED。Core start(A) 拒绝，history read 不产生 receipt；跨 Task 和跨 Project Card selector 均被拒绝。
2. `TestTaskHTTPExactExecutionOwnerIsolationAndReplay`：A Reject 后显式 canonical cancel 得 ALREADY_TERMINAL/NO_BACKEND；创建 B 后分别 GET `/v1/tasks/{id}?ai_session_id=...` 与 `?project_id=...`，以及 project Task list，完整 Work 必须等于 Core Task.Work，B 为 PENDING/NOT_STARTED/空 cancellation。canonical cancel B 得 CANCELED/NO_BACKEND/NOT_SENT；同 requestId replay 的 receipt 和两条 HTTP detail 完整响应保持相同。
3. 对 A 历史 Execution 和 B 当前 Execution，比较 Core、GPT/Local get、Console、Web workspace HTTP 和 Card；B 另比较 Task.Work、CurrentWorkflow、MCP get/list/workflow。A 的 projection/receipt 在 B 取消后不变，双方 owner/request identity 不混用，所有 no-Run 断言保持零实际 Run。
4. 两条 Task HTTP 路径保留原来的拒绝行为：foreign project Console detail 返回 404；session 路径原有 AIContext 错误映射返回 500/INTERNAL_ERROR。没有改变该既有错误映射，也不返回越权 Task。Card negative cases 返回 tool error。

初始两个回归分别复现历史 A Card 的 READY_TO_EXECUTE/非终态，以及 session detail、project detail、project list 的 B identity + A ALREADY_TERMINAL/NO_BACKEND 混合。补充 terminal authorization 控件断言也先复现取消后的可操作对象，再通过 Core status gate 修正。

### Validation 与最终审计

- Focused：`go test ./internal/agentexecution ./internal/controlapi ./internal/consoleapi ./internal/bridgehttp ./internal/mcpserver ./internal/localmcp ./internal/storage/sqlite -count=1 -json -timeout=5m`，7 包 PASS，1134 测试/子测试 PASS、6 SKIP、0 FAIL。
- 补齐历史 guidance 与 terminal authorization 控件后，最终完整重跑 `go test ./internal/mcpserver ./internal/localmcp -count=1 -json -timeout=5m`：2 包 PASS，232 测试/子测试 PASS、6 SKIP、0 FAIL。
- 最终 affected race：上述 7 包 `go test -race ... -count=1 -json -timeout=8m`，全部 PASS，1134 测试/子测试 PASS、6 SKIP、0 FAIL、0 data race。SQLite 288.498 秒，Local MCP 433.850 秒；包含完整 immutable identity、migration、exact cancellation/replay 与跨客户端回归。
- 最终 `go test ./... -count=1 -json -timeout=5m`：57 个有测试包 PASS，14 包无测试；1887 测试/子测试 PASS、8 SKIP、0 FAIL。
- `go vet ./...`：PASS，无输出。
- 95 个 working-tree changed/untracked Go 文件的 gofmt verification：PASS；`git diff --check` 与全部 untracked whitespace check：PASS。
- `node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js desktop/frontend/test/*.test.js`：307 total，304 PASS、3 SKIP、0 FAIL。
- 初始 sandbox 验证因 `127.0.0.1:0 bind: operation not permitted` 未进入业务断言；允许临时 loopback 端口后完成原问题复现及后续全部 gates。Core helper 导出后首次编译发现一个遗漏的 CurrentWorkflow 调用点，已修正并由最终完整 gates 覆盖。

Go 的 8 个 skips：真实 authorization ceiling、真实 read-only→write same conversation、持久 dogfood snapshot、真实 bounded read-only Review，以及 Review Approval V2 / Unified / Retirement / Web Observability candidate acceptance。前端 3 个 skips 为缺少外部 candidate fixture 的 mounted Card contracts。未将 skips 记为 PASS；未运行付费 Codex、live SQLite、正式 8081、设备/模拟器或 Release。

最终静态审计：

1. A 被 Reject 后创建 B，再按历史 selector 读取 A，身份、REJECTED/domain facts 和不可操作性来自 exact Core(A)，不回退 READY_TO_EXECUTE，不附加 B/current Task 的 workflow guidance。
2. B 的 session/project Task detail 与 project Task list 均消费原有 Core enrichment；取消前 NOT_STARTED/空 cancellation，取消后 CANCELED/NO_BACKEND/CANCELED，完整 HTTP response-loss replay 稳定。
3. Core、Task.Work、GPT/Local MCP、Console/Web、Card 的相应视图一致；A 的 historical projection/receipt 不受 B 取消影响，未创建 synthetic Run。
4. 本次没有新增 reducer、改变 SQL/schema、创建新 authority 或修改 cancellation mutation。Card 只读取 Core status/已有 terminal presentation，HTTP 只在原授权成功后调用共享 helper。
5. BEFORE INSERT/UPDATE/DELETE identity guards、WITHOUT ROWID、typed-nil fix、LEGACY_UNKNOWN startup、universal MCP、runtime admission/validator、claim/epoch、Host Tool、UNKNOWN/orphan、finalization 和 canonical ontology 与开始快照一致；最终全量和受影响包 race 未发现回归。本轮 final audit 无新增或剩余 P0/P1/P2。
6. 本次修改 9 文件：7 production（其中 storage 仅注释）、1 regression test、此报告。原 108 文件中其余 103 文件与开始 SHA-256 相同；新增到原 scope 的是 `bridgehttp/server.go`、`consoleapi/api.go`、`mcpserver/review_progression.go` 及新的 read-closure test。没有 unrelated feature、模型/reasoning、Card 视觉、secret 或 build/cache/log 产物。
7. 最终 branch/HEAD 不变，HEAD v41 / source v42；112 文件：83 modified、29 untracked、0 staged；tracked diff +1042 / -361。未 git add、commit、stash、reset、checkout、rebase、push 或 Release。
8. 本任务独立目录 `/tmp/devbridge-read-projection-closure-vlwadijg` 保存 baseline、测试输出及 Go cache，交付时整体清理；共享 Go cache 不清理。全部 working-tree changes 保持未提交，等待最终 acceptance。

最终结论：**AGENT_EXECUTION_READ_PROJECTION_FINAL_CLOSURE_READY**。

## MCP / Card authoritative projection final closure — 2026-09-10

本节对应 `AGENT_EXECUTION_MCP_CARD_AUTHORITATIVE_PROJECTION_FINAL_CLOSURE`，关闭最终只读验收指出的两处 presentation P1：取消后的 authorization decision，以及 orphan Card 的普通 CANCELED 映射。

### Fresh baseline 与最终范围

- Actual branch `dev_20260908_1913`，HEAD `1d16dc7feaac471fec705e4d810aeac7dc9d05a1`；HEAD schema v41，working-tree source v42。
- 开始为 112 文件：83 modified、29 untracked、0 staged；tracked diff +1042 / -361。开始前保存完整 HEAD diff、status 和所有 source 文件的 SHA-256，供本轮结束时独立比对。
- 本次修改 18 文件：9 production、8 test、此报告。最终 scope 为 119 文件：88 modified、31 untracked、0 staged；原 scope 外增加 5 个 tracked 文件及 2 个 regression test。新增范围均属于本次 MCP/Card closure。
- SQLite migration/schema/store、Run admission、Coordinator、Inspection runtime、epoch/claim、canonical cancellation/recovery/finalization、HTTP/Console Task enrichment 与开始 SHA-256 一致。没有改变这些 authority，也没有新持久化状态。

### Authoritative read contract

1. Core `agentexecution.Projection` 增加 `Terminal()` 和 `AuthorizationActionable()` 两个只读 helper；消费已有 Reduce 输出，不读取 domain rows，不修改 Reduce 的状态优先级。`UNKNOWN_ORPHANED` 的 terminal 仅表示 Bridge-owned work 已结束；status/backendDisposition 仍明确保留物理 backend 未知语义。
2. MCP 共同 correlation helper 校验 universal Execution、proposal、semantic owner、Task、Project 精确对应后使用 Core actionability。`pendingDecision`、`pendingDecisionHandle`、legacy canExecute 和 proposal Card 都使用该 helper。等待 Verification 的 PENDING 不会产生授权 decision/guidance。
3. Card 的 Execution phase/terminal 最终只映射 Core。COMPLETED 对应现有 SUCCEEDED presentation；UNKNOWN、UNKNOWN_ORPHANED、FINALIZING 使用最小新增 phase enum。TaskPlan、Preparation verdict、proposal outcome 仅保留各自 domain facts，不能覆盖 universal state。
4. `ExecutionProposalOutput.authorizationActionable` 由 Core 显式提供；独立 proposal UI 不再从 raw PENDING_CONFIRMATION 构造 Execute/Cancel。Task confirmation 的 durable receipt readback 也读取 exact Core，避免另一入口恢复旧 action。
5. 所有带 exact Core Execution 的 Card 已含自身状态及允许的 decision，wrapper 不再追加 current Task 的 workflow guidance；这覆盖 proposal selector、Run selector、当前与历史 Card，且不再依赖旧 terminal mapping。非 Execution domain Card 保留既有行为。
6. Decision UI 显示 universal status/backend disposition；UNKNOWN_ORPHANED 明确说明 Bridge authority 已撤销、workspace/context 已 quarantine，物理 backend termination 尚未证明。structured Card 无 authorization/approval/continuation/guidance，DOM 无 Execute、Cancel、Start、ordinary Retry。
7. 新增 Core helper 不是 runtime cancellation 或 recovery grant。实际 mutation 仍使用现有 semantic owner protocol。`entity_decision.go` 保留的 raw status 用于既有 CAS/已记录选择的幂等读回，不再被读侧用于构造 Execution capability。

### 真实 transport / DOM regression

- `TestAuthoritativeWorkflowCancellationDecisionAndReplay`：真实 SQLite Confirm/Reject A → Core Create B。两个 MCP transport 对 A 为 REJECTED/无 decision，对 B 为 PENDING/有 exact EXECUTE+CANCEL decision。canonical HTTP cancel B 后直接确认 raw status 仍 PENDING_CONFIRMATION、outcome CANCELED、NO_BACKEND/NOT_SENT、无 Run；两个 transport 的 workflow_get 均没有 decision，execute/cancel capability 为 false。同 requestId replay 后 receipt 和完整 workflow output 保持相同。历史 A 不受 B 影响。
- `TestAuthoritativeOrphanCardThroughProductionCancellation`：真实 admission/supervisor 启动 fake backend Turn，canonical cancellation 的 interrupt 返回 transport loss，等待现有 orphan convergence。直接检查 receipt UNKNOWN_ORPHANED、workspace quarantine、持久 UNKNOWN boundary；读取 exact Core、proposal/Run selector 的 Card 和两个 MCP transport，保留 orphan state、Core terminal，移除 stale actions/guidance。历史 REJECTED A 保持不变。
- 将上述真实 Card structured payload 输出到本任务临时 fixture，现有 mounted DOM harness 直接加载它，断言 UNKNOWN_ORPHANED badge、quarantine 与 physical-death 未证明文案，且没有 Execute/Cancel/Start/Retry。不是只用手工 fake Core struct 证明 orphan 路径。
- 补充 actionability/subject/terminal matrix：pending、rejected、canceled、completed、failed、unknown、orphan、readonly、verification pending，以及不同 Execution/proposal/Task/Project/semantic owner。decision 与 handle 一致，Card terminal 等于 Core helper。
- 保留已有 read-projection closure 的两条 Task HTTP detail、A historical receipt → B current no Run、完整 readback/replay、跨 Task/Project selector rejection 回归。
- 调整旧 unit/DOM fixtures，使它们提供相符的 Core envelope 或显式 actionability；没有通过恢复 raw status fallback 来满足旧 fixture。

### Validation

- Focused：`go test ./internal/agentexecution ./internal/controlapi ./internal/consoleapi ./internal/bridgehttp ./internal/mcpserver ./internal/localmcp ./internal/storage/sqlite -count=1 -json -timeout=5m`，7 packages PASS，1,163 tests/subtests PASS，6 SKIP，0 FAIL。
- Affected race：相同 7 packages，增加 `-race`，`-count=1 -json -timeout=8m`；7 packages PASS，1,163 tests/subtests PASS，6 SKIP，0 FAIL，无 data race。
- Full Go：`go test ./... -count=1 -json -timeout=5m`，57 packages PASS，1,916 tests/subtests PASS，8 tests SKIP，14 packages 无测试，0 FAIL。
- Frontend/DOM：`node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js desktop/frontend/test/*.test.js`，309 total，306 PASS，3 SKIP，0 FAIL。上述 Go commands 与 DOM command 均设置 `DEVBRIDGE_ORPHAN_CARD_FIXTURE` 指向本任务临时文件；新 production orphan payload 的 mounted DOM 测试实际 PASS，未跳过。
- `go vet ./...` PASS；全部 101 个 changed/untracked Go 文件 `gofmt -l` 无输出；`git diff --check` PASS。
- Go 的 8 个 opt-in SKIP：`TestRealAuthorizationCeilingRebindsSameThread`、`TestRealCodexReadOnlyToWriteSameConversation`、`TestDogfoodPersistedTaskSnapshot`、`TestDogfoodBoundedRealReadOnlyReview`、`TestReviewApprovalV2CandidateRuntimeAcceptance`、`TestUnifiedCandidateRuntimeAcceptance`、`TestRetirementCandidateRuntimeAcceptance`、`TestWebObservabilityCandidateRuntimeAcceptance`。Focused/race 包含其中后 6 项。
- DOM 的 3 个 SKIP 均为未提供 candidate fixture：`candidate authoritative Review and Preparation snapshots replace one mounted Card`、`candidate Approval action continues into Preparation on the same mounted Card`、`candidate exact durable Review completion sends one host follow-up while Approval remains pending`。
- 未运行 real paid Codex、live DB、production 8081、device/simulator。本轮真实 production transition regression 使用临时 SQLite、临时 Git workspace、loopback MCP transport 与 fake backend；没有调用付费 backend。

### Final static audit / cleanup

- Fresh 搜索 MCP/Card production 中 `PENDING_CONFIRMATION`、proposal status/outcome、EXECUTE/CANCEL、pendingDecision、READY_TO_EXECUTE、terminal、UNKNOWN_ORPHANED/CANCELED。剩余 raw 读取用于 domain facts、TaskPlan/Scope decision、exact identifiers、既有 mutation CAS/idempotence，或无 Core 的旧 Run compatibility presentation；production Core-backed Execution 的 phase/terminal/actionability 不由这些字段覆盖。
- canonical canceled B 与同 requestId replay 均无 authorization pendingDecision；genuine pending B 仍有 exact EXECUTE/CANCEL。Core helpers 约束 decision、handle、Card 和独立 proposal UI，无新的 MCP Execution reducer。
- UNKNOWN_ORPHANED 保留 universal status/backend disposition；terminal 消费 Core helper。structured Card 与真实 payload DOM 均无 stale Execute/Cancel/Start/Retry，wrapper 不拼接其他 subject guidance，也不声称已证明物理 backend death。
- 历史 A / 当前 B、HTTP enrichment、receipt isolation 与已关闭的 ownership/immutability/cancellation 回归通过上述 focused、race 和 full gates。与开始 SHA-256 比对，254 个冻结目录文件全部一致；初始 112 个 scope 文件中 101 个字节未变。变更限于本节列出的 18 文件，未重开历史 findings。
- 本轮 final static audit 未发现新增 P0/P1/P2；两个目标 P1 均关闭。
- 清理本任务保存的 baseline/diff/hash、测试日志与 orphan DOM 临时 fixture；不清理共享 cache。branch/HEAD 保持不变，119 个 working-tree 文件全部未提交，0 staged；未执行 git add/commit/stash/reset/checkout/rebase/push 或 Release。

`AGENT_EXECUTION_MCP_CARD_AUTHORITATIVE_PROJECTION_FINAL_CLOSURE_READY`
