# WebUI Task-owned contract audit

Task: `WEBUI_TASK_OWNED_CONTRACT_AND_IA_CLOSURE`.

The initial read-only inventory below records the baseline contract before edits. Classifications describe the **initial** implementation; the resolution column freezes the replacement contract. `VALID` keeps existing semantics; `RENAMED` changes the public name; `REMOVED` removes a nonexistent endpoint; `SEMANTICS_CHANGED` changes how an existing contract is consumed; `WRONG_OWNER` moves authority to its exact owner; `OBSOLETE` deletes the compatibility path. No compatibility authority is retained for the unreleased v0.1 UI.

## Mechanical fetch inventory

All production browser HTTP calls initially came through `product-app.js`'s `api(path, options)` wrapper, which calls same-origin `fetch`. The other product JavaScript files do not fetch or invoke MCP directly. Repeated call sites are listed explicitly. Query arguments are project scoped unless noted.

| Initial caller / call sites | HTTP contract | Classification | Frozen use / resolution |
|---|---|---|---|
| `readHealth` | GET `/readyz` | VALID | Service readiness only |
| `loadAll` | GET `/v1/project-selection` | VALID | Initial local selection hint; no workflow mutation |
| `loadAll` | GET `/v1/projects?limit=100` | VALID | Registered project selector |
| `loadProjectData` | GET `/v1/project-overview` | VALID | Project facts and diagnostics |
| `loadProjectData` | GET `/v1/repositories` | VALID | Repository diagnostics |
| `loadProjectData` | GET `/ui/api/featureAreas` | SEMANTICS_CHANGED | FeatureArea lifecycle and exact current Task; no Review-owned development surface |
| `loadProjectData` | GET `/ui/api/modules` | VALID | Module map |
| `loadProjectData` | GET `/v1/tasks` | SEMANTICS_CHANGED | Diagnostic records only; semantic Task product list comes from product projection |
| `loadProjectData` | GET `/v1/runs` | VALID | Diagnostic Run inventory only |
| `loadProjectData` | GET `/v1/ai-sessions` | VALID | Diagnostic session inventory |
| `loadProjectData` | GET `/v1/context-probes` | VALID | Diagnostic inspection inventory, never Tasks |
| `loadProjectData` | GET `/v1/approvals` | VALID | Runtime approval diagnostics, never phase authority |
| `loadProjectData` | GET `/ui/api/project-workspace` | SEMANTICS_CHANGED | Stable product read; Tasks must contain semantic Task IDs only |
| `workRefresh.read` | GET `/ui/api/tasks/{taskId}` | SEMANTICS_CHANGED | Exact Task detail with shared phase, histories, current operation, actions, commit/integration |
| `cancelExecution` refresh | GET `/ui/api/work-tabs/executions` | VALID | Global diagnostic inventory; not Task history authority |
| `cancelExecution` refresh | GET `/ui/api/tasks/{taskId}` | VALID | Refresh exact owning Task after runtime action |
| `openParentTask` | GET `/ui/api/tasks/{taskId}` | VALID | Exact parent navigation, not a prerequisite for understanding Execution |
| `refreshGitArea` related Tasks | GET `/ui/api/tasks/{taskId}` | VALID | Refresh semantic Tasks affected by FeatureArea integration |
| `loadLogs` | GET `/v1/logs` | VALID | Diagnostic history; explicit object filters |
| `loadDevelopmentContext` | GET `/v1/development-context` | VALID | Authoritative branch context |
| `selectDevelopmentBranch` | POST `/v1/development-branch-selection` | VALID | Explicit branch command; persisted retry identity |
| `cancelExecution` | POST `/ui/api/executions/{executionId}/cancel` | VALID | Capability-bound runtime cancellation; no primary workflow or global diagnostic action path |
| `resolveReviewApproval` | POST `/ui/api/featureAreas/{areaId}/review-approval` | REMOVED | Stale route; replace with exact Task capability action |
| `refreshGitArea` | GET `/ui/api/featureAreas/{areaId}/presentation` | SEMANTICS_CHANGED | Browser supplies project only; resolve the FeatureArea's existing durable session, then return shared lifecycle/commit projection. Never create a new session |
| `areaAction` | POST `/ui/api/featureAreas/{areaId}/action` | VALID | Feature lifecycle/context/create commands from authoritative capability; cannot decide Task phase |
| `openReviewDetail` | GET `/v1/reviews/{reviewId}/repository-drift` | VALID | Lazy operation diagnostics, read-only instruction/provenance |
| `openDocumentDetail` | GET `/ui/api/documents/{documentId}` | VALID | Exact project-fenced document |
| `answerAgentRequest` | POST `/ui/api/runs/{runId}/agent-requests/resolve` | VALID | Exact pending runtime request; not a workflow phase reducer |
| `taskAction` | POST `/ui/api/tasks/{taskId}/action` | VALID | Task phase plus authoritative capability/currentAction; exact target |
| `openReviewPrompts` | GET `/v1/tasks/{taskId}` | VALID | Diagnostic prompt inventory only; never product detail fallback |
| `viewReviewPrompt` | GET `/v1/reviews/{reviewId}/prompt` | VALID | Persisted prompt byte identity read; transport unchanged |
| `viewRunPrompt` | GET `/v1/runs/{runId}/prompt` | VALID | Persisted prompt byte identity read; transport unchanged |
| `viewProbePrompt` | GET `/v1/context-probes/{probeId}/prompt` | VALID | Persisted prompt byte identity read; transport unchanged |
| `openFullReviewResult` | GET `/ui/api/reviews/{reviewId}/result` | VALID | Exact full operation result, lazy modal |
| `closeFeatureArea` | POST `/ui/api/featureAreas/{areaId}/close` | SEMANTICS_CHANGED | Only confirmation may mint durable requestId and invoke authoritative retire; retry reuses it |

## Routes, loaders, actions and ownership

| Surface | Initial contract | Classification | Resolution |
|---|---|---|---|
| Product project tabs | Overview, FeatureAreas, Documents, Tasks, Executions, Modules, Workspaces | WRONG_OWNER | Overview, FeatureAreas, Tasks, Documents, Modules, Workspaces |
| Global overview | Recent Executions promoted beside Tasks | WRONG_OWNER | Remove primary workflow promotion |
| Diagnostics | Project/session/probe/task/approval/raw inventories | VALID | Add read-only global Executions and Runs |
| Execution drawer | Lookup global inventory; parent Task must be followed to understand work | SEMANTICS_CHANGED | Task owns Execution history; each entry names revision and nested Implementation/Verification Runs |
| Task row loader | Every work row calls semantic Task detail | WRONG_OWNER | List excludes Review operation, Execution ID, Inspection, stale pseudo Task |
| Task renderer | Shared `presentation.taskView` plus low-level fallbacks | SEMANTICS_CHANGED | TaskListItem/TaskDetail shared facts are the sole product authority |
| FeatureArea drawer | Review history and approval as independent main sections | WRONG_OWNER | FeatureArea owns Tasks; current operation links through exact Task |
| Review drawer | Loaded from FeatureArea Review array | WRONG_OWNER | Enter via owning Task's Review history/current Review |
| FeatureArea retire | Persistent row/drawer action + native confirm | SEMANTICS_CHANGED | Feature menu + accessible WebUI confirmation; cancel causes zero mutation |
| Task actions | Capability dispatch with exact target | VALID | Retain shared capability action; remove independent FeatureArea approval dispatcher |
| Global Execution controls | Cancellation in inventory rows and drawer | WRONG_OWNER | Global diagnostics read-only; Task-owned runtime controls use exact capability |
| `ui/app.js`, `ui/app.css` | Unreferenced but publicly served legacy assets | OBSOLETE | Delete assets and stale HTML fixture markers; assert 404 |
| Saved `executions` project tab | Accepted product workflow navigation | OBSOLETE | Invalid saved tab normalizes to overview; no compatibility route |
| Saved Review work type filter | Pseudo Task filter | OBSOLETE | No Review-as-Task navigation or filter |

## DTO / shared projection matrix

| Owner and fields | Classification | Resolution |
|---|---|---|
| `consoleapi.ProductProjectWorkspace.tasks` | SEMANTICS_CHANGED | Source semantic task store only; owner existence/project checks before clickable exposure |
| `ProductTaskSummary.id/title` | VALID | Task ID and semantic Task title; Review instruction never replaces title |
| `ProductTaskSummary.state`, `work.phase`, latest child status | SEMANTICS_CHANGED | Expose explicit Task `status` and `phase`; never infer phase from child state |
| `ProductTaskSummary.presentation.taskView` | VALID | Reuse shared projection family for Web/Card/Console/JSON parity |
| Task phase/progress rail | VALID | Mechanical shared mapping: Review → Approved → Implementation → Verification → Done |
| Task currentReview/reviewHistory | SEMANTICS_CHANGED | Exact Task owner and current Review ID; ordinal, status, Run and result availability |
| Task currentExecution/executionHistory | SEMANTICS_CHANGED | Exact Task owner, current marker, definition revision, nested Runs and acceptance |
| Task commitStatus/commitSha/devIntegrationStatus/mergedCommit | VALID | Persisted auto-commit/integration facts, distinct from phase/status |
| Task availableActions/currentAction/recovery | VALID | Shared read capabilities with exact targets; phase is only progression authority |
| FeatureArea currentTask/latestTask/capabilities | VALID | Exact semantic Task references; lifecycle display does not invent capabilities |
| FeatureArea active_review/latest_review/reviews | WRONG_OWNER | Historical diagnostic data must not define FeatureArea's primary workflow or Task list |
| `workflowpresentation.Workspace.selectedTaskId` | VALID | Semantic Task ID, including during Review |
| `selectedWork` | SEMANTICS_CHANGED | Current operation/activity; never copied into selectedTaskId as an operation ID |
| Global `ProductExecutionSummary` / universal agent inventory | VALID | Runtime inventory may include Review/Inspection; cannot serve as Task execution-history authority |
| Legacy `task_plan_status`, `confirmed_task_plan_id` renderer | OBSOLETE | Delete old browser reducer/asset; internal historical data does not create UI actions |

## Tool/action audit

The WebUI makes no direct MCP calls. Its HTTP adapters ultimately call shared control/product commands. These are internal Core/HTTP action names, not the GPT tool catalog. Existing authoritative actions include `task_create`, `task_review_start`, `review_approval_resolve`, `task_acceptance_resolve`, `task_commit_retry`, `featureArea_commit`, `featureArea_retire`, `execution_cancel`, runtime request resolution and recovery. These remain capability driven and exact-owner fenced.

| Name / assumption | Classification | Resolution |
|---|---|---|
| `featureArea_review_start` | OBSOLETE | No production browser call; Task owns Review start |
| Review Approval creates Task | OBSOLETE | Approval resolves existing Task's review |
| `task_type REVIEW`, `work_review_*`, Review pseudo Task | OBSOLETE | Reject as product Task IDs |
| Preparation / TaskPlan start action | OBSOLETE | Delete browser legacy reducer and action fallback |
| `ui_decision_get`, `ui_task_decide`, `ui_execution_decide` | OBSOLETE | No production browser call; shared Task capability action only |
| Legacy Start Execution fallback | OBSOLETE | Phase/capability dispatcher only |
| Child-status frontend phase reducer | OBSOLETE | Shared Task.phase mapping only |
| `featureArea_retire` | VALID | Existing Core lifecycle command; modal confirmation is adapter-only |

## Regression closure criteria

- Every listed clickable Task resolves exact detail; wrong project/FeatureArea and operation IDs are fenced.
- Contradictory historical child statuses cannot overwrite Task.phase or create actions.
- Review and Execution histories remain under Task; Runs stay under operations/diagnostics.
- Global Execution navigation is diagnostic and read-only.
- Retire is menu-only, keyboard accessible, confirm-only, idempotent on retry, and refreshes the exact page.
- Existing prompt reads remain unchanged; FAST/DEEP and production Card recall host are out of scope.

The original Node fixtures that asserted Review/Inspection clickable Tasks, Execution primary navigation and native retirement confirmation must be replaced, not retained as compatibility requirements. A final static regression scans actual production assets, excluding historical documentation and test fixtures.

For current GPT/Local names, use the [V1 consolidated catalog](gpt-surface-consolidation-v1.md); HTTP action compatibility does not expose model-tool aliases.

## Task-centric workbench v1 closure (2026-09-12)

The earlier audit above is historical. Current navigation has five primary
entries: Overview, FeatureAreas, Tasks, Resources, Diagnostics. Documents/modules
saved tabs migrate to Resources; workspaces, runs and executions migrate to
Diagnostics; old logs/diagnostics views migrate to the corresponding diagnostic
subtab. Unknown saved navigation becomes Overview. There are no compatibility
write endpoints or duplicate Task/Run interfaces.

Task/Run contents share one detail container. Task owns Overview, Review/plan,
Execution history and Run history. Run owns Overview, Activity/output,
Instructions, Evidence and Diagnostics. Current Work is compact; it does not
embed an owned Review/Execution card. Exact Core capabilities still drive the
single Task operation region. History and global diagnostics are read-only.
FeatureArea retirement stays in its lifecycle menu; integration is in Delivery.
Project settings stay project scoped; Desktop/Core runtime settings remain in
the Desktop host. Refresh/settings are topbar entries.

The additive DTO/read boundaries and compatibility policy are recorded in
[interfaces](../interfaces.md#task-centric-webui-workbench-v1-2026-09-12), with
[verification evidence and pending manual acceptance](../validation/webui-task-centric-workbench.md).
