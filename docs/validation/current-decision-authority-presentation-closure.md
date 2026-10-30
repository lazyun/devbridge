# Current Decision authority presentation closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-54ac2a798028151f)。

`CURRENT_DECISION_AUTHORITY_PRESENTATION_CLOSURE_IMPLEMENTED`

本轮在 `dev_20260908_1913` 工作区实施；起始 HEAD 为 `4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`。开始时已有 Host diagnostics、Review wakeup fallback/context optionality/late claim 的未提交修改，本轮保留这些修改，仅串行修改 Decision presentation/filtering 与相应测试。按任务要求先完成两路并行只读定位，再用测试验证具体原因。

## 复现与根因

1. **服务端按结构体全等比较 owner 字段。** Review Approval 的 FeatureArea projection target（`internal/controlapi/feature_area_presentation.go`）省略 `taskId`，Task projection（`internal/controlapi/workflow_progression.go`）包含它。相同的 `decisionId`、`subjectId` 和准确 Task/FeatureArea 上下文因此仍可能比较失败。`compactShowWorkflow` 丢弃该 pending item，`workspaceAuthorizesActionTarget` 返回 false；独立计算的 `presentationDecisionStatus` 仍是 PENDING。实际 SDK fixture 在修复前复现了 `PENDING / NOT_PENDING_OR_UNAVAILABLE`，对应摘要过滤也使用相同的全等判断。[修复前 Go 日志](archive/current-decision-authority.md#file-bdb0396848ebd999)
2. **客户端把 runtime 的 Run 约束额外套到业务 Decision。** 原 scope-isolation predicate 已用 `REVIEW_APPROVAL:<operationId>` / `TASK_ACCEPTANCE:<operationId>` 验证准确业务对象，但随后再次要求 option target 包含 operation 的 `runId`。生产 Review Approval target 没有该字段，所以正确 Decision 被排除。`pendingOptionBelongsToCurrentScope` 又做一次相同拒绝，连 projection capability fallback 也无法恢复。入口 action target 的 owner 字段全等比较同样存在第一项问题。[修复前 Node 日志](archive/current-decision-authority.md#file-79d245a35ee1a5fe)

两项缺陷都位于 authority 过滤链，因此 APPROVE form 和附着的 Model / Reasoning / Codex Fast Mode 一起消失。无需修改 chooser 或硬加按钮。

## 修复边界

- `internal/mcpserver/surface_presentation.go`：摘要过滤与 actionTargetStatus 共用 `pendingAuthorizesActionTarget`。仅对业务 Decision 的可省略 Task/FeatureArea 字段，使用已验证的准确 owner 补齐后比较；Decision、subject、execution/revision、operation、Run、conversation 等其余字段继续精确比较。显式 foreign owner 或其他字段冲突不会被覆盖。projection fallback 同时验证其 details 与当前选择一致。
- `internal/mcpserver/dev_bridge_ui.html`：业务 gate 按 exact Review/Execution Decision ID 绑定 operation；若其 action target 显式携带 Run，仍检查冲突。Runtime Approval/Request 继续要求准确 Run，并保持既有 request/connection 检查。入口 target 的可省略 owner 字段使用相同规则比较。
- Project inventory 保持原数据和导航用途。`currentPendingInteractions()` 继续从同一清单按当前 scope 派生，没有新增 registry，也不从 phase 构造 Decision。当前 Task 为空或切换时，Task-owned 旧 Decision 不再匹配；这收紧了旧 scope-isolation 报告中“无 Task 的同领域事项仍可操作”的行为，遵循本次冻结要求。
- 渲染、aggregate 去重、`validActionTarget` 与 dispatcher 继续共用 scope 校验。当前 exact item 恢复后，既有 APPROVE form 使用该 item 的 authoritative runtimeOptions，提交既有 `decision_respond` 与 `nextRunRuntimeConfig = { model, reasoningEffort, codexFastMode }`。Decision 消失、snapshot 过期、generation 改变、Task 改变或 Card dispose 后，旧表单不能提交。

没有新增或变更 tool/schema/DTO 字段，没有修改 Decision mutation、Execution runtime config immutable 语义或其他业务 contract。未修改 selector、single-active Card、wakeup/Host capability、ownership、tool catalog、Project/session binding 或 WebUI。

## 验证

| 检查 | 结果 |
| --- | --- |
| `node --test internal/mcpserver/dev_bridge_pending_scope_test.js` | 120 PASS，0 FAIL；包含导入的共享 harness。[日志](archive/current-decision-authority.md#file-b59e98db05b08da8) |
| `node --test internal/mcpserver/dev_bridge_ui_test.js` | 104 PASS，0 FAIL。[日志](archive/current-decision-authority.md#file-c89a9c6b9d32028b) |
| `node --test internal/mcpserver/*test.js` | 868 项，864 PASS、4 SKIP、0 FAIL。[日志](archive/current-decision-authority.md#file-c771acab853ff79f) |
| `go test ./internal/mcpserver` | PASS。[日志](archive/current-decision-authority.md#file-f210839c3bbdf914) |
| `git diff --check` | PASS |

新增测试集中于 `dev_bridge_pending_scope_test.js` 和 `pending_decisions_scope_test.go`：实际 SDK workspace_get → workspace_show / dev_bridge_card_get 保留准确 Decision、三项 option 与 foreign navigation item；验证现有 OutputSchema；两种 owner target 表达均得到 PENDING。DOM/transport 测试验证每项审批操作只出现一次、三个 runtime controls、model/effort/Fast Mode 参数、处理后控件消失和 detached callback 拒绝。其余测试覆盖 exact operation、替换 Decision、Runtime Run 隔离、空 Task/切换及 stale/disposed authority。

Node 的 4 项 SKIP 来自未提供真实候选环境 snapshot/orphan fixture，不代表真实 Host 验收。Go 测试清空 `DEVBRIDGE_REAL_CODEX` 和 `DEVBRIDGE_REAL_REVIEW_SMOKE`；沙箱首次因 Go cache 权限阻止启动，授权重跑后通过。未修改 workflowpresentation/controlapi，未额外运行这些包。

真实 ChatGPT Host 验收：`NOT_RUN`。未启动浏览器/Desktop，未操作真实 Review/Decision，未 commit / merge / push / deploy。
