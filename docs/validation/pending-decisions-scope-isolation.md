# Pending Decisions 作用域隔离

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-e0e645ad69e2c216)。

`PENDING_DECISIONS_SCOPE_ISOLATION_IMPLEMENTED`

`MANUAL_CHATGPT_ACCEPTANCE_PENDING`

仓库：`/Users/phoenix/Documents/devbridge`；分支：`dev_20260908_1913`。Starting HEAD：`1a61382db29fd0725b84da94d697e8ce0d2af230`，开始时工作树干净。实现留在该分支未提交工作树；未 commit / merge / push / deploy，未操作真实 Pending Decision、正式配置或服务。

## 根因与数据定义

`devBridgeCardProjection` 调用 `DevelopmentWorkspaceForSession`。后者精确读取选中的 Presentation，但 `attachProjectPendingDecisions` 无论当前选择如何，都会附加整个 Project 的待处理清单。原 UI 按 interaction kind 和可选 Decision ID 过滤，且 `currentProjectDecisionAuthority` 只验证 Project、快照及 generation，导致其他 FeatureArea/Task 的清单项同时进入当前主内容与点击授权路径。

| 数据 | 范围与用途 |
| --- | --- |
| `workspace_get.workflow.pendingDecisions` | Compact reader 的当前选择，加准确 Run 的运行时交互 |
| `workspace_get.result.navigation.pendingDecisions` | 项目级导航清单；不代表当前 Task 的权限 |
| Card `_meta.workspace.pendingDecisions` / `dev_bridge_card_get.pendingDecisions` | 同一项目级清单；可包含其他 owner |
| UI `projectPendingInteractions()` | 显式命名旧 wire 字段的项目清单语义 |
| UI `currentPendingInteractions()` | 从项目清单通过共享 predicate 派生的当前范围交互；不建立第二份 store |

为兼容现有 SDK 和 Text/JSON 消费者，本轮未新增或改名 wire DTO 字段，也未改变 Core 数据源。以组件 presentation helper 明确区分两种 scope，DTO 注释与接口文档同步定义。

## 当前范围契约

- 必须有准确 FeatureArea；Task 非空时同时匹配 FeatureArea 和 Task。Task 为空只表示 FeatureArea overview，不是 Project wildcard；该领域自己的交互仍可显示。
- Decision entry 在原导航位置时仅匹配其准确 Decision，且 status 必须是 PENDING；已处理、替代、未知状态不回退到其他 Decision。
- Operation entry 继续限定 operation identity / owner Task / Run；ActionTarget entry 限定原 target，且 target 必须与实际请求 payload 的 owner、Run、connection、item 一致。缺少必要匹配证据时不开放操作。
- Runtime Approval、Runtime Request、Codex Request 同样经过 scope 过滤。请求的 Project/FeatureArea/Task 必须与清单 owner 一致。同范围 INTENDED/SENT 保留只读投递状态，只有 PENDING 能响应。
- 相关分区读取失败返回空的当前交互集，保留错误/刷新提示；不从项目清单猜测当前审批，不恢复 stale authority。
- 渲染、折叠状态、`aggregateTargets`、`currentProjectDecisionAuthority` 共用 `currentPendingInteractions()` → `pendingInteractionBelongsToCurrentScope()`。Capability target 额外经过 `pendingTargetMatchesOwner()` 与 `pendingOptionBelongsToCurrentScope()`；dispatcher 拒绝隐藏的 foreign item、foreign target 和不属于该 item 的 capability。
- 旧 projection decision / runtime fallback 路径也使用相同 owner predicate。既有 Session、Project、generation、readback、Loading、historical read-only fencing 保留。

本轮不保留 Project Pending Inbox：其他 owner 的项目事项暂不显示，不提供跨范围审批。未修改 presentation lease、entry identity、8 秒 timeout、project_open 生命周期、Session 模型、durable workflow/receipt、30-tool 注册。

## 验证

| 检查 | 结果 |
| --- | --- |
| 新增隔离矩阵 | 无 Task 的领域、同领域另一 Task、准确 Decision、不同 Run/request、Runtime Approval、owner 冲突、旧 option/新 payload、dispatcher 拒绝、迟到响应、分区失败、只读投递状态与静态调用链检查 |
| 全部 MCP Node tests | 707 项：703 PASS、4 SKIP、0 FAIL；包含 UI、collapsible、refresh modes、wakeup 和新 scope suite。共享 harness 会重复注册 UI tests，计数为 runner 结果。[日志](archive/pending-decisions-scope-isolation.md#file-fce98112c4cb139c) |
| 实际 SDK scope/schema | PASS：当前 workflow/show 摘要只有当前 Task，NAVIGATION 与同次 Card meta 保留项目清单；实际 OutputSchema 校验通过。[日志](archive/pending-decisions-scope-isolation.md#file-50f46799aa8ab83e) |
| 相关 Go 包 | MCP、workflowpresentation、controlapi 均 PASS |
| 全仓库 Go test | 59 个包 PASS（其中 2 个 cached），14 个包无测试。[日志](archive/pending-decisions-scope-isolation.md#file-289780c88c904e99) |
| 最终组件集成 | 最终 HTML 的 MCP 全包测试 PASS；build / vet exit 0。[日志](archive/pending-decisions-scope-isolation.md#file-6e6a7f30cc6e181e) |
| diff hygiene | `git diff --check` PASS；新增文件也检查尾部空白 |

Node 的 4 项跳过由未配置 `DEVBRIDGE_ORPHAN_CARD_FIXTURE` 和 `DEVBRIDGE_CARD_SNAPSHOTS` 引起，不把它们计为候选环境验收。真实 Coding Agent smoke 使用空环境变量禁用。

发现的既有失败：HEAD 中 collapsible 测试期待“原决策已解决或被替代”，生产已有文案为“原决策已处理或被替代”；仅修正断言，保留其历史入口不泄漏其他审批的检查。原先明确允许 foreign Project item 在当前 Card 内执行的测试按本轮冻结语义改为拒绝；runtime configuration 功能测试改用当前 owner fixture，仍验证实际提交、参数与重试。

复现：

```sh
node --test internal/mcpserver/*test.js
DEVBRIDGE_REAL_CODEX= DEVBRIDGE_REAL_REVIEW_SMOKE= go test ./internal/mcpserver/... ./internal/workflowpresentation/... ./internal/controlapi/...
DEVBRIDGE_REAL_CODEX= DEVBRIDGE_REAL_REVIEW_SMOKE= go test ./...
go build ./...
go vet ./...
git diff --check
```

## 修改与工作树

- `internal/mcpserver/dev_bridge_ui.html`：单一当前 scope 投影及渲染/authority/dispatcher 接线。
- `internal/mcpserver/dev_bridge_ui_test.js`：修正跨 scope 旧预期、当前 owner fixtures；仅测试 harness 注入 closure 访问，验证隐藏 dispatcher；生产组件不暴露测试 API。
- `internal/mcpserver/dev_bridge_pending_scope_test.js`：隔离矩阵与静态安全检查。
- `internal/mcpserver/dev_bridge_collapsible_test.js`：修正既有历史状态文案断言。
- `internal/mcpserver/pending_decisions_scope_test.go`：真实 SDK 当前 workflow 与 Project navigation/meta 区别及 schema 契约。
- `internal/workflowpresentation/workspace.go`：只增加 scope 注释，无 DTO/schema 变化。
- `docs/interfaces.md`、本报告及证据日志：最终契约和验收记录。

最终工作树包含上述修改与新增文件，均未暂存、未提交；HEAD 保持 Starting HEAD。

## 真实 ChatGPT 宿主待验收

以下全部为 **NOT_RUN**：无 Task 且无交互的领域实际不显示其他领域 Pending Decisions；真实当前 Task 审批仅显示自身；历史 Decision/运行时请求不回退；快速导航与回读失败在真实宿主中保持隔离。未操作浏览器或设备 UI，未处理任何真实请求。自动 fixture/SDK 验证不代表真实宿主已验收。
