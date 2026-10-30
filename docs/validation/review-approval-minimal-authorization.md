# Review Approval minimal authorization surface

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-40fa4793a2b9d926)。

`REVIEW_APPROVAL_MINIMAL_AUTHORIZATION_SURFACE_IMPLEMENTED`

`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`

仓库 `/Users/phoenix/Documents/devbridge`，分支 `dev_20260908_1913`，Starting HEAD `4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`。开始时工作区已有 Host diagnostics、Review wakeup fallback/context optionality/late claim，以及 current Decision authority 的未提交修改与验证报告。本轮保留这些内容，没有重置或覆盖既有实现。

## 原始渲染入口

`internal/mcpserver/dev_bridge_ui.html` 的两个通用路径直接把 Review 技术内容带进审批面板：

- `renderProjectPendingDecisions`：`decision.summary || item.runtimeRequest?.waitingState || item.semanticState || item.kind`，同时展示 owner/Task/FeatureArea/semanticState 与时间。
- `renderProgress`：`decision.summary || projection.nextAction?.message || '请确认当前操作后继续。'`。

相邻路径还会复制技术内容：`renderProgress` 的通用“状态详情”汇集 Decision summary、nextAction 与 workflow message；`renderAgent` 在没有实际 activity/step 时回退到 nextAction.message。Review Approval 现在不使用这两种通用 fallback；实际 Codex activity、Task 标题、phase、progress 和明确的历史/details 入口继续保留。

## 新展示策略

`decisionPresentationPolicy('REVIEW_APPROVAL')` 明确返回不展示 summary、可见 action 为 APPROVE/REJECT、标题 REVIEW、状态“等待审批”。其他 kind 保持现有 summary 和动作策略。没有按字符串内容或长度判断。

现有两个 Decision renderer 共用该策略与 heading helper。当前 Review-only pending 区域不再显示通用 Pending Decisions 标题、owner 元数据、时间或 capability reason tooltip。审批面板最终内容是：

- REVIEW / 等待审批。
- 下一次运行设置：Model、Reasoning、Codex Fast Mode。沿用既有 authoritative runtimeOptions；Review form 默认展开，模型、reasoning 默认与切换策略不变。
- 确认 → APPROVE；拒绝 → REJECT。

REVISE 仅被 presentation filter 隐藏。Core、schema、projection capability inventory 和既有 dispatcher 均未删除 REVISE，GPT 仍可通过原 workflow 使用它。没有根据 phase 制造 Decision、增加审批接口或自动调用 execution_start。

## 数据、authority 与 transport

没有删除或改写 Review result/proposal、Decision summary、specs、hashes 或历史数据。测试在渲染前后比较整个 fixture，并检查 VM 中的 summary 与三项 capability 仍存在。完整 Review 继续通过原 authoritative read、history/details、diagnostics/audit 路径提供给 GPT 和明确的只读入口。

当前 exact Decision 的过滤、validActionTarget、aggregate authority 和 dispatcher 未修改。测试继续验证 decisionStatus/actionTargetStatus 都是 PENDING、foreign Decision 不可提交，以及 Task 清空/切换、过期/分离表单的拒绝逻辑。

确认仍调用 `decision_respond`，保留原 aiSessionId、featureAreaId、taskId、decisionId、requestId 和 APPROVE。提交所选 `model-b / high / true` 时，测试收到：

```json
{"nextRunRuntimeConfig":{"model":"model-b","reasoningEffort":"high","codexFastMode":true}}
```

拒绝仍发送 REJECT，且不附加下一次运行配置。`subjectId` 保留在 authoritative Decision/ActionTarget 中并沿用现有 owner 校验；现有 `SurfaceDecisionRespondInput` 没有独立 subjectId 入参，因此没有为展示调整扩展 wire schema。下一次运行配置和 Execution immutable 语义保持原状。

## 本轮文件范围

- `internal/mcpserver/dev_bridge_ui.html`：显式 presentation policy、共用 heading、可见动作过滤、Review chooser 展开、中性文案及相邻 fallback 抑制。
- `internal/mcpserver/dev_bridge_ui_test.js`：aggregate/projection 两条路径的技术内容抑制、原数据保留、其他权限/问题信息回归；更新 Review 文案及最小动作断言。
- `internal/mcpserver/dev_bridge_pending_scope_test.js`：保留 exact authority 测试，验证确认及拒绝 transport、三项 backend capability、runtime 配置。
- `internal/mcpserver/dev_bridge_candidate_test.js`、`dev_bridge_collapsible_test.js`、`dev_bridge_review_wakeup_test.js`：仅同步“批准实施”到“确认”的 Review UI 断言；不修改对应生产行为。

本轮没有修改 Go production、domain、schema、工具目录、selector、wakeup 或 session binding。工作区中的 Go diff 属于前序任务。

## 验证

| 检查 | 结果 |
| --- | --- |
| UI + pending scope 定点 Node suites | 229 PASS、0 FAIL；包含共享 harness 重复注册。[日志](archive/review-approval-minimal-authorization.md#file-028d3e7a01c9fee4) |
| `node --test internal/mcpserver/*test.js` | 883 项：879 PASS、4 SKIP、0 FAIL。[日志](archive/review-approval-minimal-authorization.md#file-83f92bc220540373) |
| `go test ./internal/mcpserver` | PASS，包含实际 SDK exact Decision presentation/schema 契约回归。[日志](archive/review-approval-minimal-authorization.md#file-1ad1bd7add355820) |
| `git diff --check` | PASS |

测试覆盖了 summary 中 ROOT_CAUSE_SECRET_MARKER、/Users/、workSpec、acceptanceCriteria 的 DOM 抑制；authoritative summary/capability 不变；最小动作；APPROVE 配置与 REJECT 映射；runtime chooser 默认/模型切换既有策略；Runtime Approval 的 reason/permission、CODEX_REQUEST 的问题与回答控件。原 Task Acceptance、cancellation、recovery 和 lifecycle 测试通过。

全量测试曾发现 collapsible/wakeup 的旧按钮文案断言，更新展示期望后通过；没有修改业务断言来跳过失败。4 项 SKIP 仍为未提供候选环境 snapshot/orphan fixture 的既有跳过。

真实 ChatGPT Card 手动验收：`NOT_RUN`。未启动浏览器/Desktop，未操作真实 Decision、live SQLite 或正式服务，未 commit / merge / push / deploy。不声明 GLOBAL_READY。
