# UNIFIED_REVIEW_EXECUTOR_AND_UPPER_LLM_RESULT_CLOSURE

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/unified-review-executor.md)中。

本报告及配套日志 记录实现验证结束、提交前的快照；后续提交和分支整合状态以 Git 历史为准。文中的临时 worktree 路径为验证来源，清理后仍可通过当前仓库中的相对链接读取交付文件。

本轮在专用隔离 worktree 实现了统一 Review executor 和上层 LLM 正式报告闭环。
实际 MCP 调用已验证：UPPER_LLM 启动 → 工作区准备 → 受控读取 → 报告提交 →
授权关闭 → claim 释放 → 既有审批/继续审查入口。没有调用真实业务实例做验收。

## 基线、工作区和变更状态

- Starting HEAD / current HEAD：`2ea108707b50b5c6e6db8d41998069aeec50de4d`。
- 实施工作区：`/private/tmp/devbridge-unified-review-executor`。
- 实施分支：`codex/unified-review-executor`；全部修改**未提交**。
- 原工作区：`/Users/phoenix/Documents/devbridge`，分支 `dev_20260908_1913`；核对时保持干净。
- 只读诊断观察到运行服务版本 `0.1.0-dev+g4ed8e3e`、ready=true。这不是完整部署 HEAD
  证明，也不表示运行服务包含本轮未提交代码。
- [完整文件清单](changed-files.md)、[Git 状态](../archive/unified-review-executor.md#file-b792a12addb7361e)、[差异检查](../archive/unified-review-executor.md#file-48772b49390a6387)。

没有操作 live SQLite、对运行实例迁移/重启、终止外部进程、处理其他领域工作区，
也没有 commit、merge、push、release 或 deploy。所有写入和测试在获准隔离资源内。

## 现有服务与扩展点

| 既有权威 | 本次扩展 |
|---|---|
| `review` / `scopereview` | 不可变 executorKind、结构化 ReviewReport、同一启动准入和重放 |
| Review-owned `Run` / FeatureArea claim | Upper 正式 attempt；不创建 Coding conversation、Thread/Turn 或隐藏 Execution |
| workspace / execution coordinator | 直接准备合法 FeatureArea 工作树；失败保留可定位 admission，重试不重复创建 |
| review_grants / review_access_evidence | Upper 只读 target、epoch/generation、允许/禁止路径、成功内容证据 |
| existing Run.start_snapshot / Root | 有界授权内容快照；严格拒绝符号链接组件；不会扫描被禁止子树 |
| SQLite canonical finalization | 同事务 report、receipt、grant、Run、Task/approval 和 claim 收尾 |
| cancellation / recovery | exact no-backend 取消；重启保留占用并显式恢复同 Review/Run |
| MCP / Local MCP / HTTP / Console | 共享 typed 服务；新增 report/access/evidence 入口；Console 按需只读报告 |
| workflowpresentation / WebUI / Card | executor、真实阶段、unavailable telemetry、准确取消 owner 与上层绑定限制 |

正式迁移为 **48**，通过既有迁移机制执行。使用现有 grant/evidence/recovery 表扩展；
新增表仅为不可变正式报告及其必要提交回执，没有平行 workflow registry。

## 最终接口与完成条件

完整请求、目标、报告、回执、错误码及兼容策略见
[canonical contract](../../architecture/unified-review-executors.md)。可运行的完整
APPROVAL_READY 请求位于
[跨适配器集成测试](../../../internal/localmcp/upper_review_integration_test.go)。

最终调用顺序：

1. `project_open` → 已存在或 `task_create` 创建的正式 Task。
2. `task_review_start(executorKind=UPPER_LLM, reviewDepth, reviewInstruction, reviewPlan, forbiddenPaths?)`。
3. `review_access_get` → 复制 Core 返回的 reviewTarget；`file_list` / `file_read`。
4. 可选 `review_evidence_get`；`workflowComplete` 只进入 AWAITING_REPORT，仍持有 claim。
5. `review_result_submit(target + requestId + report)` → COMPLETED 回执。
6. `review_result_get` 回读正式报告或指定 requestId 的原回执。
7. 进入既有 Review Approval 或 DEEP Review Required，等待必要的用户决策。

ReviewReport v1 的必填字段为 schemaVersion、summary、disposition、coverage、findings、
limitations、recommendedNextSteps、evidenceRefs。findings 可为空；每个非空 finding
具有稳定 ID、severity、title、description、impact、evidenceRefs、suggestedChange。
APPROVAL_READY 必须有真实内容证据及完整显式 workSpec / RUN verificationSpec；
BLOCKED 和 FURTHER_REVIEW_REQUIRED 可以无成功证据，并且不能使 Task 可批准实施。
Core 不分析报告自然语言来生成实现指令或判定工程正确性。

旧客户端省略 executorKind 仍按 CODING_AGENT 处理；新客户端明确提交。Coding 的
模型/effort 校验与 callback fencing 没有因 Upper 被放宽。Upper 未获得可信宿主
模型信息时 provenance 和 telemetry 均为 UNAVAILABLE。旧报告不被改写。

提交在同一个 writer transaction 内进入 FINALIZING，保存意图和完整 payload digest，
写入正式报告、Run 终态、Task/审批变化，关闭 grant、释放 claim、完成 receipt。
文件 I/O 和证据记录也持有该 writer transaction，因此正常终结不需要 GPT 手工释放。
故障注入已验证“报告写入后、grant 关闭前失败”整体回滚，重放同请求能完成。
完成回执先于 staleness 检查读取，因此旧 epoch 的成功原请求仍可合法回放。

## A–J 验收矩阵

| 项 | 实际执行及结果 | 代码/证据 |
|---|---|---|
| A 正常路径 | **通过**：真实临时 Core、SQLite、Git worktree；GPT MCP 启动/读，Local MCP 提交，跨适配器回放与全文回读；0 Coding 调用、0 conversation、0 隐藏 Execution，claim=0 | `TestUpperReviewCrossAdapterClosure`；[upper-e2e.log](../archive/unified-review-executor.md#file-4f3b3ff50baf7df5) |
| B Coding 兼容 | **通过**：全仓 59 个有测试包通过，包含历史迁移、model/effort、Review 结果/通知/取消/审批、原有 epoch/callback fencing | [go-test.log](../archive/unified-review-executor.md#file-a916a57c9e5a5774)；SQLite、execution、controlapi、mcpserver、localmcp 既有回归 |
| C 创建/准备 | **通过**：Task-only 既有测试；准备中断保留正式身份与 blocker；原 request 重放只有一条 Review/Run/workspace/worktree；完成后 start replay 不重新启动 | `TestUpperReviewPreparationFailureRetainsAdmission`、coordinator/scopereview 测试 |
| D 文件边界 | **通过**：禁止文件/目录、遍历、Git 元数据、内部 symlink 绕路、写入口、跨领域 target、旧 epoch；禁止内容不会出现在 listing 或参与 scope snapshot；未绑定 search 不能绕过 active Review | `TestUpperReviewCrossAdapterClosure`、`TestUpperReviewNegativeBusyAreaAndWorkflowCompleteClaim`、`TestScopedSnapshot*` |
| E 报告约束 | **通过**：空摘要/coverage/limitations、非法结论、超限、缺显式规格拒绝；无成功 READ 不能审批；伪造及跨 Review evidence 拒绝；空 findings 有效报告可终结；受阻不审批 | `TestReviewReportStructuralBoundary`、`TestUpperReviewResultBlockedFinalizesAndReplays`、Upper 集成 |
| F 幂等 | **通过**：同请求同 payload 回原 receipt；不同 payload 冲突；新 request 不能覆盖；跨 Session 不泄露；完成后重启仍能回放旧 epoch 原请求；禁止无正文的 COMPLETED receipt | [fault-injection.log](../archive/unified-review-executor.md#file-dae250853008963e)、Upper restart/cross-adapter 集成 |
| G 竞争/重启 | **通过所列隔离场景**：cancel→submit、submit→cancel 唯一终态；PREPARING 三种 Run 阶段可精确取消；重启保持 Review/Run 并更新 epoch/generation；同领域 busy、不同领域并行；终结事务故障整体回滚 | Upper negative/restart 测试、`TestExecutionCancellationUpper*`、[fault-injection.log](../archive/unified-review-executor.md#file-dae250853008963e)；在途读取收敛由共用 writer transaction 及独立代码复核确认 |
| H 基线 | **通过**：临时 development ref 前移而工作树未变，报告保留原 baseline/startSnapshot；授权源码改动拒绝；forbidden 内容变化不造成范围外读或改变 snapshot；startSnapshot 数据库不可变 | Cross-adapter 集成和 ScopedSnapshot 测试；`upper_review_result.go` |
| I 工具目录 | **通过**：实际 GPT 与 Local MCP catalog/schema/result 验证；31 个共享业务工具、GPT 另有 3 个 presentation 工具；混合 executor 配置拒绝；diagnostics 提供 catalog/schema 指纹 | [contract.log](../archive/unified-review-executor.md#file-1412c017bdc9e434)、[projection.log](../archive/unified-review-executor.md#file-3742b2532050bc50)、`TestFinalCatalogAcceptance` |
| J UI/责任 | **隔离投影/静态验证通过**：Upper 真实阶段、无伪 telemetry；无报告不 COMPLETE；Standalone WebUI 不冒充上层；准确取消 owner；既有刷新/迟到响应/只读详情与唤醒测试 392 项通过。**真实宿主未执行** | `TestUpperReviewStageUsesDurableFacts`；[node.log](../archive/unified-review-executor.md#file-cf1ba6a1a7da56ff)；WebUI、Card 定点改动 |

验收范围说明：没有对每个操作系统 I/O 调度交错或突然断电做独立故障注入。
本轮验证了 SQLite 收尾事务的失败回滚与实际关闭/重开临时 Core；不能把它表述为
所有硬件故障的实测。授权内容快照是有界内容观察，不是 OS 提供的文件系统隔离副本。

## 独立复核与已修正问题

独立只读复核针对本轮关键不变量进行，发现并已修正：

- 初始 snapshot 身份缺失：现已保存并校验授权内容 startSnapshot。
- grant 非终态跳转过宽：现由迁移 48 的状态转换和恢复 receipt 约束。
- 完成 receipt 可能没有正文：现由数据库 trigger 要求有效完成正文及同一正式 report。
- 取消没有下层 supervisor 时被拒绝：现有严格 Upper no-backend 分支，并保留 Coding 限制。
- 文件接口吞掉 REVIEW_TARGET_STALE：现保留 Core 稳定错误码。

实施期间出现过并已修复的失败还包括目录条目的非相对句柄 stat、快照格式不符合
Run 字段长度约束、诊断字段数量/全局指令的契约快照落后，以及 vet 发现的 JSON tag。
最终日志反映修正后的代码，不以早期失败掩盖剩余问题。

## 检查命令与结果

所有命令在专用 worktree 执行；Go cache 使用 `/private/tmp/devbridge-review-gocache`。
测试的 SQLite 与 HTTP 服务均为临时 fixture，不是运行中的业务实例。

- `go test ./...`：通过，59 个有测试包；[日志](../archive/unified-review-executor.md#file-a916a57c9e5a5774)。
- `go build ./...`：通过；[日志](../archive/unified-review-executor.md#file-76f95d06d0612b66)（成功无输出）。
- `go vet ./...`：通过；[日志](../archive/unified-review-executor.md#file-7fb2aa7d5de616d5)（成功无输出）。
- `node --check internal/bridgehttp/ui/product-app.js`：通过。
- 3 个相关 Card/刷新/唤醒 Node 套件：392 passed / 0 failed；[日志](../archive/unified-review-executor.md#file-cf1ba6a1a7da56ff)。
- `git diff --check`：通过；[日志](../archive/unified-review-executor.md#file-48772b49390a6387)。
- 针对性 `go test -race`：3 个包通过；[race.log](../archive/unified-review-executor.md#file-c545daca03703537)。
- 全仓回归后对 JSON tag 修正及取消保留原审查深度做最终定点复验：通过；[projection.log](../archive/unified-review-executor.md#file-3742b2532050bc50)、[cancel-final.log](../archive/unified-review-executor.md#file-d3a6e06feb386b29)。

## 仍需真实宿主验收

未启动真实浏览器或操作用户设备。以下尚无真实宿主证据，**未宣称通过**：

1. 用户选择部署/加载本轮版本后，刷新 ChatGPT 实际工具目录，核对 diagnostics 指纹。
2. 真实 ChatGPT 发起/继续 Upper Review、跨消息恢复、报告按需展开及 Card 展示。
3. 真实通知是否送达，以及 Coding 通知仍正常、Upper 不触发自我分析循环。
4. 独立 WebUI 的实际宿主渲染、表单/滚动/展开交互验收。

这些是未执行的宿主验收项，不是用 READY 或本地 mock 代替的部署结果。
没有已知未实现的主流程步骤；实现与隔离验证保留在未提交 worktree，等待用户审阅。
