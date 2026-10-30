# WEBUI_TASK_OWNED_CONTRACT_AND_IA_CLOSURE

已实施，保留未提交工作区。审计先于页面修改；共享 DTO、动作和路由 ownership 经串行 Gate 冻结后实施。

| # | 交付项 | 最终结果 |
|---|---|---|
| 1 | Starting HEAD | `dev_20260908_1913` / `e17f1b4401adc323f6fd40c20655b1e72fe8576d`；开始时 working tree clean；schema target v44；Prompt Neutrality closure 在此 HEAD。未 reset。 |
| 2 | Contract audit matrix | [完整 endpoint / loader / route / DTO / mutation / MCP action 矩阵](webui-contract-audit.md)，另有 [Feature / Retire 审计](webui-feature-retire-audit.md)。 |
| 3 | 删除 / 替换接口 | 删除可访问但未使用的 `ui/app.js` / `ui/app.css`；移除 Feature Review approval HTTP 调用、Review pseudo Task 分支、Review 类型筛选、Execution 主入口及 Task child-status reducer。Task create 改为 semantic Task command，Task action 统一分派 starts / approval / acceptance / recovery。旧资产返回 404。 |
| 4 | 主导航 | 概览 → 功能领域 → 任务 → 文档 → 模块 → 工作区；更多 / 诊断内保留运行诊断。 |
| 5 | Task list | `Task / Phase / Status / Code / Dev / Updated`；仅 durable DEVELOPMENT FeatureArea Tasks；排除 Inspection、Review、Execution、legacy read-only 和不可读对象。Code/Dev 及 currentAction/recovery 与详情复制同一 shared TaskView。 |
| 6 | Task detail | Semantic title、phase/status、Code/Dev、updatedAt、currentAction；Current Work；默认折叠 Reviews、Executions、Activity/Usage、Goal/Result、Diagnostics。一次 product detail 获取 exact operation graph。 |
| 7 | Task.phase | 直接返回持久 Task.phase；`TaskPhaseLabel` / `TaskPhaseProgress` 在 workflowpresentation 中共享；浏览器只渲染。终态依实际 v44 的 Task.status 展示 Done/Canceled/Failed，不伪造新的持久 phase。 |
| 8 | Review history IA | `TaskView.currentReview/reviewHistory`，Task/Project/FeatureArea 精确过滤；含 ordinal、status、所属 Run、result availability、instruction、failure、created/updated；Details 从 Task 进入，instruction 不成为 Task title。 |
| 9 | Execution history IA | `TaskView.currentExecution/executionHistory`；含 ordinal/current、TaskDefinitionRevision、Implementation/Verification Run、acceptance receipt/status、failure；Run 展示在操作内。Task execution inventory 不混入 Review runtime envelope。 |
| 10 | 全局 Execution | 只读 Diagnostics inventory；可查看 domain/backend disposition、失败及历史，无第二条 workflow mutation 按钮路径。Task 当前操作中的正式 runtime actions 保留。 |
| 11 | FeatureArea ownership | Feature header 展示 lifecycle、commit/dev facts、exact Current Task 和菜单，下方以 Tasks 为主；不再出现 Feature-owned Review approval / Review history 主区。 |
| 12 | Retire | 只在 Feature `⋯` 菜单提供；capability 不可用或 RETIRING/RETIRED/DECOMPOSED 时不显示。首击只打开自有 dialog；Confirm 才生成 ID 并调用 authoritative retire adapter；双击防重、失败同 ID 重试、刷新失败只重读；Cancel 零 mutation；成功刷新同 Feature/Task 页面。 |
| 13 | NOT_FOUND | 列表与详情统一 semantic entity domain，并以 exact shared detail read 过滤不可读项目；HTTP 回归遍历每个 clickable ID、错误 Project/FeatureArea 以及 operation/stale IDs。另实际复现并修复 Feature presentation 仅传 project_id 时的 404：从 exact FeatureArea 解析既有绑定 session。未读取历史 live DB，因此不把旧现场 Task 404 归因于未验证的单一数据库记录。 |
| 14 | Shared product projection | 扩展既有 `ProductTaskSummary` / `TaskView`，未建立第二 family。加入 shared labels、currentWorkKind、Review history、nested Execution Runs、当前 immutable specs、currentAction/recovery 和 Code/Dev facts。 |
| 15 | Web/Card/Console parity | Facade / shared presentation 为同一事实来源；HTTP list/detail 14 字段逐一比较；Console JSON/text parity、MCP suite 通过；selectedTaskId 继续是 semantic Task，selectedWork 是 operation。未修改 Card host。 |
| 16 | Modified files | [完整最终文件清单](../validation/archive/webui-task-owned.md#file-f7cff5c408202118) 和 [git status](../validation/archive/webui-task-owned.md#file-f1c7b0f4cf1251e4)，含 UI、adapter/shared projection、回归和验证文档。 |
| 17 | Go tests | `go test ./internal/bridgehttp ./internal/consoleapi ./internal/controlapi ./internal/workflowpresentation ./internal/mcpserver -count=1` 全部通过；最后 HTTP transport 补测后再次运行整个 bridgehttp 包通过。[结果](../validation/archive/webui-task-owned.md#file-072f9accf2aa8629)。 |
| 18 | Node tests | `node --test internal/bridgehttp/*test.js`：74 passed，0 failed / skipped，涵盖 product_ui、product_work_refresh、git commit、development、retire。[结果](../validation/archive/webui-task-owned.md#file-927355dc2acc84d9)。 |
| 19 | Responsive / theme | 320 / 560 / 850 / 1440 × light/dark：8/8；24 次 Task 详情打开；验证 nav、列表/详情无溢出、历史折叠、modal Tab/Shift+Tab、Escape、focus return、Cancel/Confirm、原页刷新。仅 loopback fixture + 独立 headless Chrome profile。[结果](../validation/archive/webui-task-owned.md#file-f8c74b1b846c096f)。 |
| 20 | Static obsolete audit | production JS/HTML 旧接口/authority 与 FAST/DEEP 字段命中 0；另有结构性 Node/Go 断言防止旧主导航、pseudo Task 和 Feature approval 回流。[记录](../validation/archive/webui-task-owned.md#file-66ccce19356e941d)。 |
| 21 | go vet | 上述 5 包通过，无诊断。[记录](../validation/archive/webui-task-owned.md#file-d19550e2db34099b)。 |
| 22 | git diff --check | 通过。[记录](../validation/archive/webui-task-owned.md#file-b5355cab84d7a84c)。 |
| 23 | Final git status | 仍在原 branch/HEAD，改动未暂存、未提交；完整状态见第 16 项。 |
| 24 | SCOPE_CONFLICT | 无。未修改 storage/schema、Task/review/execution write-side Core、decisionresolution 或 retirement Core。 |
| 25 | FAST / DEEP | 未实现，未增加 ReviewDepth / 快速审查 / 深度审查。 |
| 26 | Prompt Neutrality | 保持。prompt construction/transport、Probe、scope-conflict disposition、byte identity、final_codex_prompt audit 未修改；UI 读取既有字段，Review input 原文交给既有 owner。 |
| 27 | Production recall host | 仍为 `HOST_INTEGRATION_PENDING`，未实现 visibility/recall。 |
| 28 | 发布边界 | **未 commit、未 push、未 tag、未 release，未修改其他 worktree。** |

## Frozen read and action contracts

```text
ProductTaskSummary (list and detail)
  id, title, phase, phaseLabel, phaseDescription, status, updated_at
  commitStatus, commitSha, commitActivity, commitErrorSummary, commitSummary
  devIntegrationStatus, mergedCommit, devSummary
  currentAction, recovery

ProductTaskSummary.presentation.taskView (detail)
  currentWorkKind: REVIEW | EXECUTION | TASK | NONE
  currentReview, currentExecution
  reviewHistory[], executionHistory[]
  actions[], currentAction, recovery
  currentWorkSpec, currentVerificationSpec
```

`currentWorkKind` 也在 shared presentation 中机械映射，避免已批准 Review 的 durable pointer 在 Implementation 阶段覆盖 Current Execution。Task.phase 不从这些子对象反向计算。

Task action 请求携带 observed capability 的 exact target 和 requestId，服务端重新验证。REQUEST_CHANGES 提交显式修改后的现有 immutable specs。Task Review start 需要独立 instruction，Task create 只提交 semantic title/problem/repository/constraints，不隐式创建 Review 或 Run。

v44 有两项必须保留的实际语义：终态保持最后的 durable phase、由 Task.status 提供终态展示；运行中断后 Core 可能在 phase 尚未改变时返回 `RESOLVE_TASK_ACCEPTANCE`。此时只展示 Core 正式授权的处置动作，保持原 phase，且绝不在 ACCEPTANCE_PENDING 以外提供 ACCEPT。相应回归验证无 pending authority 时，child failure 不能自行产生动作。

## Visual evidence

- [宽屏 Task list](../validation/webui-task-owned/1440-light-tasks.png)
- [850px dark Task detail](../validation/webui-task-owned/850-dark-task-detail.png)
- [320px Retire confirmation](../validation/webui-task-owned/320-light-retire.png)

本轮发现并修复了 320px Feature rows overflow、dialog Tab 逸出和 850px compact sidebar 空白按钮；最终 headless 复测通过。未使用 live DB、真实 Codex、外网服务或用户浏览器 profile。
