# GPT 入口至任务合入：当前源码复审

日期：2026-10-24（Asia/Shanghai）。审查基准为当前工作区，包含已有未提交改动；不是仅审查 HEAD，也不是沿用 2026-10-23 的修复结论。当前面板源码标记为 task-panel-v33。

修复进展见 [修复记录](full-flow-repair-2026-10-24.md)；本文保留修复前的发现及复现结果。

结论：正常路径已有持久化授权、独立验证与“合入后完成”约束，但异常路径仍存在两个阻断合入的缺陷、一个重复续接请求身份缺陷，以及 GPT 入口指令与实际流程不一致的问题。不能据此认定全流程已闭合。

本次没有修改产品实现，没有操作生产任务、点击业务确认、启动真实模型、部署或合并用户工作分支。仅新增本报告与 build/flow-audit-20261024 下的隔离复现材料；Git 操作发生在测试创建的临时仓库中。

## 1. P1：释放任务完成的合入计划后，原任务无法完成

触发顺序：验证通过 → 用户确认 COMPLETE_TASK → 任务代码已提交 → 合入前开发分支出现另一个提交 → 原合入获得明确的副作用前拒绝 → 使用返回的 canRelease 资格释放旧计划。

释放操作只更新 feature_area_commit_receipts.released_at。task_completion_intents 仍永久绑定原 merge_request_id 和原计划；其身份不能修改或删除。后续 FinishTaskCompletion 必定重放已释放的计划。任务保持 ACTIVE / DELIVERING，无法按正常 API 完成。

隔离复现实际结果：

- 初次交付：`DELIVERY_REPREPARE_REQUIRED: RECOVERY_REQUIRED`，任务保持 ACTIVE / DELIVERING。
- 返回的候选确实允许释放，释放调用成功。
- 重试完成：`MERGE_PLAN_RELEASED`。
- 重新准备任务完成：`execution proposal conflict`。
- 重新准备领域计划可以成功，但提交新计划被数据库拒绝：`Merge operation requires exact accepted member (1811)`，因为新请求不是原 completion intent 的请求。

代码：[释放计划](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/merge_plan_release.go:48)、[固定重放原计划](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_completion_store.go:332)、[完成身份不可变](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_verification_decisions_v131_migration.go:24)。

建议：为任务完成增加显式的后继合入计划及用户重新确认协议，保留已验收提交和已完成成员的原回执。新目标只能由新的授权绑定；不能修改旧不可变计划，也不能通过倒退开发分支绕过冲突。在后继协议建立前，至少不要对 completion-owned 计划提供一个释放后无恢复出口的操作。

复现：`build/flow-audit-20261024/completion_probe_test.go`、`completion-probe.log`。用 Go overlay 加载测试，未向产品包写入测试源文件。

## 2. P1：新增字段改变持久化请求编码，旧合入无法续接

当前 mergeDispatchInput 新增 CompletionTaskID 字符串字段，但没有省略空值或显式协议版本。即使是普通领域合入，新的 JSON 也多出 `"CompletionTaskID":""`。旧版保存的操作没有该字段。

dispatchMerge 将当前结构重新编码后的 JSON 和 hash 与持久化载荷逐字比较，比较发生在读取既有 APPLIED / REJECTED 回执之前。旧版待完成合入因此直接返回 RECOVERY_REQUIRED，无法沿原计划续接；该错误也不会产生可重新准备的明确拒绝回执。

现有回归测试 TestMixedProtocolAreaMergePreservesLegacyDelivery/historical-partial-plan 在全包和单独复跑中均失败，位置 mixed_protocol_merge_test.go:224。对照实验仅通过 overlay 给历史测试夹具的载荷结构补上空 CompletionTaskID 字段，保持产品代码、任务、成员和实际 Git 行为不变，原用例转为通过。这说明回归来自持久化编码变化，而非 Git 冲突或普通测试时序。

代码：[新增无版本字段](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/merge_delivery_dispatch.go:15)、[先于历史结果读取的字节比较](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/merge_delivery_dispatch.go:146)。

建议：对持久化操作提供明确的旧协议解码和比较规则，保留原 payload/hash/回执证据；不能把合法旧格式拒绝成未知结果，也不能为兼容而跳过来源身份或 UNKNOWN 守卫。兼容测试应同时保留真正的旧格式和新格式，不能把历史夹具全部改成新格式来掩盖问题。

证据：`build/flow-audit-20261024/control-targeted.log`（原格式失败）、`historical-shape-control.log`（新格式对照通过）、`shape-overlay.json`。对照不是产品修复。

## 3. P2：同一定义下的第二次实施重试复用旧 prepare requestId

触发：一次实施失败，用户通过面板准备 RETRY_IMPLEMENTATION；后继 Execution 再次失败，用户以相同说明再次请求继续实施。沿原方案重试本来可以保留 taskDefinitionRevisionId，同时更换 executionId / decisionId。

面板的消息去重 key 包含完整 cap.target，但 prepareRequestId 的缓存 key 只有 Session/Project/Area/Task/定义版本、操作和说明，遗漏了目标 Execution/Decision。因此第二次消息是新请求，却被要求沿用第一次准备的 requestId。Core 按原请求的 submitted hash 校验，新的目标会得到确认请求冲突；刷新恢复仍会保存这个错误关联。

复现用当前 UI 的真实函数和可见 RETRY_IMPLEMENTATION 入口：两个不同目标获得不同 message key，却获得相同 prepareRequestId，断言失败。

代码：[遗漏目标的缓存键](/Users/phoenix/Documents/devbridge/internal/mcpserver/dev_bridge_ui.html:839)、[Core 请求冲突守卫](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/confirmation_outcome_store.go:39)、[重试可沿用原定义](/Users/phoenix/Documents/devbridge/internal/storage/sqlite/task_implementation_retry_store.go:111)。

建议：准备请求身份同时包含完整、规范化的 action/target，至少覆盖 Execution、Decision、Review、恢复版本等权属字段；仅同一逻辑操作在响应丢失后的重试复用原 ID。目标发生变化必须产生新 ID。不要放松 Core 的幂等校验。

复现：`build/flow-audit-20261024/panel-probe.test.cjs`、`panel-probe.log`。

## 4. P2：GPT 初始化收到的主流程说明仍混用旧合同

这不是仅有历史文档过期。server.go 实际将 ChatGPTConfirmationInstructions 作为 MCP 初始化 instructions 发给 GPT，但该字符串仍然：

- 把待验收动作描述为 ACCEPT / REJECT / REQUEST_REVERIFICATION，没有说明 COMPLETE_TASK / CONTINUE_TASK / DEFER_TASK 是当前主流程，也没有标注此处 ACCEPT 仅为兼容验收。
- 明确说文件模式不带 Codex 配置，而当前 DIRECT_FILE + RUN 必须冻结 Codex 验证配置。
- 明确说 ui/message / sendFollowUpMessage 不属于该流程，而当前面板的授权后交接与请求准备正使用这些消息机制。

工具描述和当前实现已经采用新合同，初始化说明却仍给 GPT 另一套规则。普通对话中的任务完成可能被导向兼容 ACCEPT；该路径可以记录成功而不执行新完成计划的合入。文件实施与授权交接也可能被要求重复人工续接。这是确定的公开指令冲突；本次未运行真实 GPT 来测量其具体选择概率。

代码：[实际注入初始化说明](/Users/phoenix/Documents/devbridge/internal/mcpserver/server.go:116)、[旧验收动作说明](/Users/phoenix/Documents/devbridge/internal/mcpserver/external_workflow.go:780)、[文件模式配置说明](/Users/phoenix/Documents/devbridge/internal/mcpserver/external_workflow.go:791)、[消息机制说明](/Users/phoenix/Documents/devbridge/internal/mcpserver/external_workflow.go:802)。新工具描述见 [execution_prepare](/Users/phoenix/Documents/devbridge/internal/mcpserver/confirmation_tools.go:413)。

建议：统一初始化 instructions、工具 description 和面板生成的续接提示；新主流程明确要求完成包含合入，兼容入口限定为明确的历史/兼容用途。补充跨说明的语义一致性测试，而不是只断言初始化返回某个旧常量。

## 流程覆盖与验证边界

| 阶段 | 本次检查重点 | 结论 |
| --- | --- | --- |
| GPT 唤起与项目选择 | Session 初始化、同 Session 绑定、面板入口、逻辑项目与开发分支选择 | 未发现新的身份串用缺陷；入口说明存在问题 4 |
| 创建与审查 | 创建提案不等于创建对象；GPT/Codex/不审查分支；原始回执与文件授权 | 当前正式确认和文件授权边界仍在；未复现上一轮已修复的卡片消失问题 |
| 审批与执行 | APPROVE_AND_START、冻结运行配置、失败后继、执行工具边界 | 已批准启动链有持久化支持；多次失败后面板准备身份存在问题 3 |
| 验证与分析 | 独立验证、原始输出分析、必需 CUSTOM、缺失证据禁止完成 | 不能用原生运行成功替代工程验收的约束仍在 |
| 提交与合入 | 完成计划冻结、真实临时 Git 仓库、合入响应丢失、开发分支前移 | 正常路径守住合入后完成；释放旧计划和旧版请求编码存在问题 1、2 |

“不审查”路径仍有 CONFIRM_IMPLEMENTATION 和 START_APPROVED 两次正式确认。这是当前明确实现并有测试的产品合同，本次不重复列为运行错误。

## 测试结果

- 现有 MCP 界面 Node 测试：386 项，382 通过，4 跳过。
- MCP 后端全包通过；execution、workflowpresentation、agentexecution、review、task 全包通过。
- Codex 执行边界定向测试通过；原生环境用例可能依赖环境开关，本次不将此结果表述为真实 Codex 运行验收。
- SQLite 全包发现两组、共 10 个子场景失败：TestAgentRuntimeRealTransportSQLiteAcceptance（8 个）及 TestUsageClientPersistsAcrossThreadBindingAndEarlyCompletion（2 个）。均在 StartThread 返回 `CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: effective config missing`。脱离沙箱单独复跑结果相同。
- 失败原因可追踪到两套 stdio 测试 helper 未实现新启动合同的 config/read 返回值；新门禁因缺失配置正确拒绝启动。这意味着权限询问、打断、断连、steering 及用量场景在这些测试中尚未真正执行，不能把全包写成通过。后续应补齐真实形状的 config/read 和 mcpServerStatus/list 夹具响应并重跑原断言，不能关闭生产门禁来让测试变绿。
- 本次新增的两个缺陷探针按预期失败，用于证明问题存在，不是修复后回归通过。
- 控制层全包在默认 10 分钟时限终止，不能报告全包通过。期间出现一次验收恢复用例的 HANDOFF_IN_PROGRESS（定向复跑通过），以及历史合入用例稳定失败（已定位为问题 2）。超时时正在执行恢复审查测试，不据此认定该测试自身死锁。
- 控制层定向复跑 8 组测试，67.513 秒结束：验收确认回执恢复、同任务暂存/延续/完成合入、required CUSTOM 通过、required CUSTOM 受阻禁止完成、operator 完成、多仓 DIRECT_FILE + Codex 验证、不审查路径等 7 组通过；混合协议合入组只有 historical-partial-plan 子场景失败。
- 对历史载荷编码的单变量对照用例通过，6.146 秒，产品代码未修改。

首次沙箱测试中 MCP/控制层的回环端口创建被拒绝，已获工具批准后重跑；这类环境错误与上述 SQLite 夹具回归分开记录。未进行原 GPT 会话到真实 Codex 再到用户工作分支的生产写入验收。

完整日志、探针和受检源文件 SHA256 保存在 `build/flow-audit-20261024/`。建议先修复问题 1、2，再修复问题 3、4及测试夹具，随后补做真实宿主的全链路验收。
