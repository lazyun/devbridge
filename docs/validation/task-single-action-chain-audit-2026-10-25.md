# Task 唯一待操作链路审计

日期：2026-10-25。范围：当前工作区 Core、GPT 统一面板及 WebUI 的共同流程合同。基线为 `c4ae1d30afb401ed2b9e7200f7781460b1e79a5f` 加现有未提交改动，并非对该 commit 单独审计。本次只审计，不修复或部署；未操作生产任务、发送 GPT 消息或消费真实确认。

## 结论

当前不能保证「一个 Task 只有一个当前待操作链路」。现有机制能保护单张确认的身份、请求幂等、冻结定义、部分阶段和交付回执，但缺少覆盖所有入口的 Task 级当前节点及代际准入。

这里的「一个链路」允许一个决策节点包含延续、暂存等互斥选项，也允许运行节点包含多条原生权限请求；不要求界面只剩一个按钮。要求是：所有选项归属于同一当前节点，消费任一选项后，旧节点不能再产生新副作用；结果未明时，只能核对或恢复该原节点。

## 已复现问题

### A1 / P1：同 Task 可以创建多个待确认，UNKNOWN 不占据统一准入位置

入口：`internal/controlapi/confirmation.go:81`；持久化：`internal/storage/sqlite/confirmation_store.go:252`、`:294`；消费：同文件 `:423`。

准备按 Session + requestId 幂等。新 requestId 会建立独立 PENDING intent，没有通用 Task 当前节点的唯一约束。BeginCommit 在写事务内检查准确 intent、请求、项目、选项和特定业务 fence，但不检查该 Task 是否已有其他未决 intent。

隔离复现：验证 Run 成功结束、尚未提交 GPT 分析时，在会话 A 为同一 Task 准备两张 CONTINUE_TASK 和一张 DEFER_TASK，全部成功；绑定同一项目的会话 B 再准备一张 CONTINUE_TASK，也成功。

再通过真实 ConfirmationStore.BeginCommit / RecordStep 模拟原请求已消费、RECORD_TASK_CHOICE 结果 UNKNOWN 且无业务回执的前缀。准确读取返回 UNKNOWN、canResume=false。此时仍能为同一 Task 准备新确认，WebUI 对应的 operator 命令以及其他 GPT 确认仍能写入业务状态。原 UNKNOWN 全程保留，未被其他回执冒认完成。

影响：单请求的未知结果保护成立，但不能阻止绕到同 Task 的其他请求继续操作，形成多条未决链路。

### A2 / P1：同阶段的新决策不会使旧确认失效

业务写入：`internal/storage/sqlite/task_hold_store.go:84`、`:91`、`:99`；旧单展示分类：`internal/controlapi/task_confirmation_feed.go:69`；WebUI 入口：`internal/consoleapi/task_action.go:113`、`internal/controlapi/task_verification_decisions.go:211`。

ChangeTaskHold 检查 Task/Execution/定义身份、业务阶段、运行终止和交付占用，但没有验证预期的 hold/continuation 状态或决策代际。延续和暂存不改变这些身份，因此旧确认持续符合检查条件。feed 的 STALE 分类也不包含这类变化。

隔离复现：

1. GPT 先准备 CONTINUE_TASK 和 DEFER_TASK，两张都不消费。
2. operator 执行 DEFER_TASK，代表 WebUI 的正式暂存操作。
3. 消费之前准备的 GPT CONTINUE_TASK，成功完成，刚才的暂存被解除。
4. 消费步骤 1 的旧 DEFER_TASK，仍成功完成，再次暂存。

这些调用使用不同 requestId，不违反单请求幂等，却会让用户后来作出的决定被之前的旧卡片改变。需要冻结并比较 Task 流程代际；仅冻结 definitionRevision 不足以覆盖同版本、同阶段内的决策。

### A3 / P1：Review 当前 selector 没有成为普通提交的准入条件

selector 定义与更新：`internal/storage/sqlite/confirmation_replacement_store.go:34`、`:60`；selector 比较：同文件 `:87`、`:309`；普通提交 fence：`internal/storage/sqlite/confirmation_store.go:19`、`:28`。

confirmation_current_decisions 仅覆盖 Review 的 REVISE / REJECT，并以 Session + Decision 为主键。普通 prepare 会把 selector 指向新 intent，但不会同步封存旧 intent；普通 CommitConfirmation 的 fence 核对 Review、阶段、业务 Decision 等，不核对该 selector。currentReplacementDecisionTx 的比较用于 ReprepareDecision。

隔离复现：同一 Review Decision 先准备 REVISE，再准备 REJECT。确认数据库 selector 已指向新 REJECT 后，消费旧 REVISE 仍返回 COMPLETED。

影响：即使局部已经存在「当前确认」选择器，也不能将它当作 Task 全局互斥保护。修复时需同时覆盖普通消费与显式替换，并跨 Session 统一任务节点。

### A4 / P2：GPT 与 WebUI 没有读取共同的当前待操作节点

Session feed：`internal/controlapi/panel_confirmation_feed.go:30`、`:64`；SQLite 过滤：`internal/storage/sqlite/session_confirmation_feed.go:31`、`internal/storage/sqlite/task_confirmation_feed.go:29`。

GPT 面板：`internal/mcpserver/dev_bridge_ui.html:1393`、`:1420`、`:1426`、`:1431`。面板将多条 PENDING / CONFIRMING / UNKNOWN / PARTIAL 都作为后续操作，只按 action 对部分重复按钮去重；该行为没有 Task 全局互斥的含义。

WebUI：`internal/bridgehttp/ui/product-app.js:1211`、`:1227`；operator 命令直接到业务 owner，见 A2。WebUI 操作区根据能力与 verificationDecisions 渲染，不以 GPT 正式确认队列为共同当前节点。

隔离复现中，会话 A 的同 Task feed 显示 3 张待确认，会话 B 显示 1 张。Session 隔离本身是正确的权限边界；问题是这些列表之外没有安全的 Task 级当前节点摘要，导致每端都可能把自己的局部入口当成当前主路径。

修复不能简单放开读取其他 Session 的确认内容。应由 Core 返回经过授权过滤的共同节点摘要，其他会话可以知道「此任务正等待原确认核对」，操作正文、会话身份及恢复权限继续遵守原访问边界。

## 需要统一的产品合同

验证后分析和延续的优先级目前不一致：`internal/workflowpresentation/task_next_step.go:109` 将待分析展示为 ANALYZE_RUN_OUTPUT，文案要求先分析；`internal/controlapi/task_verification_decisions.go:90`、`:150` 仍允许在运行终止后准备延续或暂存。隔离测试在 phase=VERIFYING / nextStep=ANALYZE_RUN_OUTPUT 时成功准备并消费这些操作。

不能据此声称「未分析就可完成合入」：COMPLETE_TASK 有额外的验证完成门禁。延续或暂存也可能是合法的当前节点选项，未必需要强制 GPT 先分析。应在新合同中明确：它们是分析节点允许的显式分流，还是分析完成后的下一节点。两种设计都可以维持唯一链路，但不能由 GPT 文案和不同 UI 各自决定。

## 分阶段覆盖

| 阶段 | 现有保护及本次结论 |
| --- | --- |
| 创建 Task 前 | 尚无 TaskID；创建请求的幂等和正式采纳链路需要保留，不能机械套用 Task 唯一键。创建后应原子绑定任务当前节点。 |
| 审查 / 等待审批 | Review/Decision 身份和阶段有保护；A1、A3 表明不能保证唯一待确认。 |
| 已批准 / 准备实施 | 冻结定义和运行准入是已有保护；通用 prepare/consume 未见 Task 当前节点。未在本次复现重复启动 Run。 |
| 原生运行 / 权限审批 | 原生请求按准确 Run 绑定。多条权限请求可以属于同一运行节点，不应误判为多个业务链路。 |
| 验证 / 分析 / 延续 / 暂存 | A1、A2、A4 已用实际 facade、SQLite 及假 Codex 运行进程复现。 |
| 验收 / 逐仓提交合入 | completion plan、业务验收回执和 task_completion_intents 已有独立保护。现有完成合入测试纳入本次检查；不能据前述问题推断能够绕过必需验证或重复合入。 |
| UNKNOWN / PARTIAL | 单 intent 的准确回读与禁止盲目重放成立；Task 其他入口没有统一占用约束。 |

## 建议修复顺序

1. **先建立 Core 任务节点。** 为每个已有 Task 持久化当前节点和单调递增的 generation，绑定准确 Review/Decision/Execution/Run/定义及节点状态，节点可包含多个互斥选项。Session 是发起者或访问上下文，不能成为任务链路唯一性的分区键。
2. **统一所有写入口。** GPT prepare/commit、WebUI operator、恢复、运行完成和分析入库都通过同一节点迁移合同。提交冻结 expectedGeneration + nodeId。写事务内比较并占据当前节点；后续业务写入核对该准确占用。不能只在 facade 读前检查，也不能只在 UI 隐藏按钮。
3. **未知结果留在原节点。** 已消费、未明的请求持续占据当前节点，仅允许原回执核对及 Core 证明安全的恢复。未消费撤回沿 .74 的持久拒绝机制；确认封存后才允许新一轮显式提交。部分步骤完成时保留同一父节点及原步骤身份，不能另起后继绕过。
4. **完成一次决策就推进代际。** 包括同 Execution、同定义下的暂存/延续。旧卡片可以继续读历史回执，但旧代际的新提交必须被明确拒绝；已有已消费请求的回放继续返回自己的不可变结果。
5. **两个 UI 消费同一投影。** Core 统一返回当前节点、等待原因、允许动作与经授权的恢复入口。Session feed 保留历史和私有内容，不再承担决定当前业务权威的职责。刷新、跨会话及多标签必须看到同一个节点。
6. **迁移不能猜测最新卡片。** 对已有多 PENDING / UNKNOWN，先核对准确业务回执，保留全部历史。无法唯一确定时进入一个显式冲突处理节点，不按时间戳挑选、不自动消费、不抹掉未知事实。

验收至少覆盖：跨 Session 并发 prepare、WebUI 与 GPT 竞争、同版本旧卡片、REVISE/REJECT selector、消费事务与业务写入间崩溃、未知撤回与迟到提交、部分合入恢复、重启和刷新。成功标准是只出现一个当前节点，且失败的一方没有产生新业务副作用。

## 验证记录

临时探针复用 runCodingDeliveryFixture / newUpperLifecycleFixture、真实 Core facade 和 SQLite。Codex 运行使用仓库现有的独立进程协议 fixture，修改和 Git 操作只发生在测试临时目录。UNKNOWN 前缀通过真实 BeginCommit / RecordStep 构造，不把客户端发送失败等同于 Core 已消费。

探针断言的是当前缺口能否复现，PASS 不表示唯一链路已经修复。探针不加入正常回归测试，避免将缺陷固化为预期产品行为。可按 A1—A3 步骤使用相同 fixtures 重建。

本次未进行完整全仓回归，也未把静态阅读标记为生产端到端验证。测试结果与范围见下方记录。

执行：`go test ./internal/controlapi -run '^TestTaskSingleChainAudit|^TestVerificationPrimaryChoicesSameTaskAndMergedCompletion$|^TestConfirmationSubmissionWithdrawalFencesLateCommit$|^TestConfirmationSubmissionWithdrawalPreservesConsumedResult$' -count=1 -v`。

结果：5 项 PASS，包耗时 17.633s。

```text
PASS TestConfirmationSubmissionWithdrawalFencesLateCommit
PASS TestConfirmationSubmissionWithdrawalPreservesConsumedResult
PASS TestTaskSingleChainAuditProbe
  Before analysis: phase=VERIFYING nextStep=ANALYZE_RUN_OUTPUT
  Session feed has 3 pending Task choices
  Session feed has 1 pending Task choices
  New Task confirmation prepared while original consumed request remained UNKNOWN
  Old GPT continuation consumed after operator defer and cleared its hold
  Old GPT defer consumed after continuation and restored hold
  Original UNKNOWN remained unresolved throughout the operator and other GPT writes
PASS TestTaskSingleChainAuditReviewSelectorProbe
  New REJECT is selected, but old REVISE still consumed
PASS TestVerificationPrimaryChoicesSameTaskAndMergedCompletion
```
