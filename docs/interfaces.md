# Dev Bridge interface reference

独立工程状态恢复的合同与验收见 [Recovery Status 记录](architecture/codex-recovery-status-reconciliation-v1.md)；Project Inspection owner 变体见 [G10 closure](architecture/codex-recovery-status-inspection-g10-closure.md)。Project Inspection 使用同一组 `recovery_status_prepare/start/read/resolve` 命令；已解决的 partial 另提供 `project_inspection_recovery_continue`，HTTP 对应 `POST /v1/recovery-status/{id}/continue-inspection` 与 operator 路由。该 continuation 只接受 stable request ID、caller instruction、payload hash 与 expected epoch，不接受 outcome、findings、targets 或 force。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](architecture/codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

> Project-centric update (schema v73–v74): [implemented contracts and remaining workflow boundaries](architecture/project-centric-multi-repository-closure.md). Projects may have zero or multiple repositories; Session selection is Project-only. Workspace membership is not authorization. Multi-repository Review, execution and delivery remain explicitly blocked where the complete authority chain is unavailable; the single-repository flows below describe compatibility behavior.

The [unified Review executor contract](architecture/unified-review-executors.md) extends this document with
Task-owned UPPER_LLM Reviews, strict read grants, formal report submission and
atomic result/permission/claim closure. Generic Direct File writes and Coding Agent
configuration retain their existing meanings.

Schema v46 uses a Task-owned engineering workflow. Task is the durable engineering
intent and the sole owner of semantic phase. Review and Execution record operations
under that Task; Run records backend execution activity.

```text
Project → FeatureArea → Task
                       ├─ Review [0:N] → Run [1:1]
                       └─ Execution [0:N]
                            ├─ Implementation Run [0..1]
                            └─ Verification Run [0..1]
```

FeatureArea owns the canonical Workspace and AgentConversation and serializes agent
operations across all its Tasks. Project-scope Inspection keeps its internal operation
envelope outside FeatureArea semantic Tasks.


## Direct File Capability v0.1

Direct File Capability is disabled by default and may only be enabled after explicit user authorization represented by an Upper LLM enable call.

Direct File and writable Coding Agent operations are mutually exclusive for the same canonical worktree.

Direct File mutation supports whole-file creation/replacement only. Missing parent directories for create/move destinations are created automatically inside the canonical worktree.

Both Upper LLM MCP adapters expose `direct_file_enable`, `direct_file_disable`,
`file_list`, `file_read`, and `file_write(action=CREATE|REPLACE|DELETE|MOVE)`.
All five use explicit `aiSessionId`, `featureAreaId`, and `workflowId`; file paths
are worktree-relative. File reads and writes require explicit enable. The closed
write branches preserve create-without-overwrite, existing-regular-file replacement,
nonrecursive deletion, and no-overwrite/no-cross-filesystem-fallback move.
A final file call with `workflowComplete: true` closes the workflow automatically;
otherwise its Upper LLM owner must call disable on completion/cancellation. Every
unrelated user request uses a fresh workflow ID. No transport-session lifetime or
natural-language inference substitutes for this explicit authorization contract.

Whole-file decoded content is bounded to 256 KiB (inclusive). Base64 accepts up
to 349528 encoded bytes including padding before decode, then applies the same
256 KiB decoded-byte bound as plain UTF-8. Invalid base64 is INVALID_FILE_TARGET;
oversized content is FILE_TOO_LARGE. File reads are bounded to 256 KiB, lists to 256 immediate
entries, and relative paths to 4096 bytes. Patch/edit/append/line mutation and
caller-supplied filesystem roots are prohibited. See the [canonical Direct File
architecture contract](architecture/direct-file-capability.md) for ownership,
atomic publication, byte encoding, lifecycle, error and Git boundaries.


## Product workflow

`task_create` requires a stable requestId and persists ACTIVE / REVIEW_REQUIRED intent
through canonical CreateIntent and its durable creation receipt before Review.
Same request/payload returns the exact same Task; changed payload conflicts.
The legacy POST /v1/tasks write route is retired; GET /v1/tasks remains a read. It creates no
Review, Execution or implementation-authoritative revision. `task_review_start` starts
a Review for exact `taskId`. The current Review gate approval freezes the initial
immutable TaskDefinitionRevision on the same Task. `execution_start(taskId)` reserves
the current candidate and its Run atomically; there is no second authorization card.

### Feature Area creation and first Review composition

`feature_area_create` has two typed modes. Omitted `mode` remains `CREATE_ONLY` for
compatibility: it creates only the FeatureArea, and an optional supplied `requestId`
uses the FeatureArea creation receipt. It must not silently launch a Task, Review or
Run. `CREATE_ONLY` rejects `initialTask` and `initialReview`; callers that omit a
request ID receive no invented replay identity.

`CREATE_AND_REVIEW` is an explicit, state-changing composition. It requires a stable
root `requestId`, a complete `initialTask` (title, problem statement and Repository,
with explicit constraints when applicable) and a complete `initialReview` (depth,
plan, verbatim instruction and model; an omitted reasoning effort uses the enabled
dynamic default fixed at admission). It validates the selected Session/Project, exact Repository,
development branch context, focus and read-only runtime ceiling before writing the
FeatureArea. It then reuses the canonical `task_create` and `task_review_start`
application paths for the exact newly-created Task; it does not make one FeatureArea
own a Review or create a hidden Task.

The root receipt freezes its accepted input and configuration. Stable namespaced child
request IDs let retries resume only unfinished Task/Review admission steps. A same
root request and payload returns the original FeatureArea, Task, Review and Run
identities; a changed payload conflicts. A response-loss or dispatch-unknown result
reads/reconciles that original Run and never opens a second Review. Later Review
completion, failure or cancellation does not cause replay to start another Review.
Partial failure keeps every committed identity and returns the completed steps, failed
step, structured failure and exact recovery path; a post-write readback failure is a
successful mutation with `refreshRequired`, not permission to recreate it.

The result keeps `result.id` as the FeatureArea ID and reports composition state in
dedicated startup fields: mode, root request ID, completed steps, exact Task/Review/Run
identities, Review admission, observed Run status, failed step, retryability and
refresh requirement. It remains SILENT. After the authorized chain finishes or reports
a blocker, GPT keeps the existing `panel_show` panel; it must not show a Card for each
child step. The composition admits only the first technically read-only Review. It
does not approve a Review, start Implementation or Verification, resolve Task
Acceptance, choose a branch or grant runtime permission.

A Project is the durable development boundary. `session_initialize({requestId})`
first creates a real AISession with NULL Project and a durable initialization
receipt. The model receives its actual ID before displaying Project choices.
`session_initialize({aiSessionId})` instead reads that Session's current binding;
exactly one input is required. It returns PROJECT_SELECTION_REQUIRED with a
separate bounded candidate inventory, or PROJECT_BOUND with the actual Project.
Inventory failure retains the committed Session and reports its own read error.

Only an explicit choice permits `project_open({aiSessionId,projectId,requestId})`
to bind that same Session once. Same-Project repeats return its original binding;
another Project conflicts. No-Session opens fail with an initialization
prerequisite, and invalid Sessions never cause replacement. Request keys are
caller-scoped idempotency data, not proof of a ChatGPT conversation. GPT MCP and
Local MCP share this contract; Console Project browsing remains independent.
See [the Session-first contract](architecture/first-use-project-selection.md)
for receipts, scope, migration and historical compatibility.

A Feature Area is the durable, Project-owned engineering problem. It has `ID`,
`ProjectID`, `Title`, `ProblemStatement`, `Status`, `CreatedAt`, and
`UpdatedAt`. Its active lifecycle is `DISCOVERING`, `ACTIVE`, `RETIRING`, `DECOMPOSED`, and `RETIRED`. A Feature Area
may be created before repository, module, configuration, scope, verification,
or implementation details are known. Natural-language steering changes the
Feature Area direction.

A DEVELOPMENT Task persists title, problem statement and constraints independently of
Review instructions. Its status is ACTIVE, SUCCEEDED, CANCELED or FAILED. Its durable
phase is one of the eight stages below. Review/Run infrastructure failures preserve
Task phase and overall status; exact recovery and Task Acceptance govern continuation.

## Development branch context

`project_open` reports only the committed Session/Project binding, receipt and
preparation status. Read `workspace_get` for repository-scoped development context
and branch inventory. Unselected repositories explicitly require branch selection;
a Project-only workspace has no selected FeatureArea. Upper AI obtains an explicit
choice before entering normal development work.

- `workspace_get(include=[BRANCHES])(aiSessionId)` returns each registered Repository's
  identity, selectable full local refs, head SHAs and checkout facts.
- `development_branch_create(aiSessionId, repositoryId, developmentRef, baseCommit)`
  creates one validated local branch ref at an exact immutable base. It neither
  selects the branch nor runs Git checkout. It has no request receipt; retries
  reconcile the exact ref and base commit through authoritative inventory.
- `development_branch_select(aiSessionId, repositoryId, developmentRef, requestId)`
  selects an exact `refs/heads/*` identity without executing Git checkout.
- Console reads `GET /v1/development-context?project_id=...` and explicitly writes
  `POST /v1/development-branch-selection` with `project_id`, `repository_id`,
  `development_ref` and `request_id` through the same application service.

Each Project/Repository has at most one current DevelopmentCycle. Reselection
preserves history. Durable request replay returns the original selection result;
changed payload yields `REQUEST_CONFLICT`. `DEVELOPMENT_BRANCH_NOT_SELECTED`,
`DEVELOPMENT_BRANCH_NOT_FOUND`, `DEVELOPMENT_REF_MISSING`, and
`REPOSITORY_PROJECT_MISMATCH` remain distinct domain facts/errors.
Selected and current heads are separate. Missing refs never fall back; moved refs
never reset the baseline. FeatureAreaCycleBinding and execution baseline changes
are deferred. Existing workflow admission remains unchanged.

`project_selection_show(aiSessionId,presentationRequestId)` displays the existing
Session's Project picker; it creates neither a Session nor a selectionHandle.
The user explicitly chooses before `project_open` binds the original Session.
Next-turn reads use `session_initialize(aiSessionId)` without relying on context
push or manual synchronization. `workspace_show` requires a real bound Session
even when its Project has no development branch selected.
Tool input/result establish binding; widgetState is presentation cache only.
Missing/out-of-order identity waits for initialization, then fails closed on a
bounded timeout. Conflicting Session/Project identities never select a new Project.

## External and internal model

The product names do not require a repository-wide internal rename.

| Product concept | Durable/internal implementation | Rule |
|---|---|---|
| Feature Area | Project-owned engineering domain | Owns Tasks, Workspace, AgentConversation and operation serialization. |
| Task | Semantic engineering intent | Durable status, phase, current Review, current Execution and current definition revision. |
| Review | Task-owned read-only operation | Ordinal unique within Task; one immutable linked Run. |
| Execution | Task-owned delivery candidate | Exact immutable revision; at most one Run for each purpose. |
| AgentExecution | Runtime identity of Review, Development or Inspection | Runtime state is activity metadata, never Task phase. |
| Run | Exact backend operation phase | Immutable owner and purpose; reconnect does not create another business Run. |

Fresh databases install schema v47 through the versioned migration chain.
The v46 → v47 migration adds only the FeatureArea creation command receipt table,
so root request identity and FeatureArea creation commit atomically. Existing
FeatureArea/Task/Review/Run rows and workflow phases are unchanged.
The v45 → v46 migration freezes next-run runtime configuration separately on
Approval and Execution, preserving historical unknown selections as NULL.
Versioned v44 → v45 migration
preserves quiescent Task-owned history and backfills historical Review depth as
DEEP without fabricating approvals. Ambiguous active Review state fails closed.
Pre-Task-owned databases with populated semantic history remain unsupported;
see the [migration policy](architecture/task-execution-v0.1.md#schema-and-validation-boundary).

## Workflow state and readiness

`workspace_get(aiSessionId, featureAreaId?, taskId?, include?)` returns a compact
Project, development summary, exact selected Task phase, real pending decisions,
capabilities, blockers and nextAction. Includes are closed: MODULES, BRANCHES, TASK,
RUNTIME_CONFIG and NAVIGATION. Explicit Task reads that Task; mismatched ownership
fails. Read partitions carry their own generation/sourceSequence, read time,
staleness and errors, and `atomic:false`. A failed partition disables actions and
sets refreshRequired; it does not become a complete fresh snapshot.

Pending interaction scopes are distinct:

- `workflow.pendingDecisions` belongs to the selected workflow (plus its exact Run requests).
- `result.navigation.pendingDecisions` and Card `_meta.workspace.pendingDecisions`
  are Project-wide navigation inventories; their presence grants no current Card authority.
- The Card adapter names these `projectPendingInteractions()` and derives
  `currentPendingInteractions()` through `pendingInteractionBelongsToCurrentScope`.
  Rendering, collapsed status, aggregate targets and dispatch share this predicate.
  FeatureArea selection requires exact Area ownership; Task selection additionally
  requires exact Task ownership. An empty Task never removes the Area restriction.
  Explicit Decision/operation/action targets narrow the scope further. Missing or
  conflicting owner evidence and failed read partitions fail closed. Capability
  targets are also checked against their item's owner before dispatch.
- Other owners are currently omitted from Card interaction content. No Project
  Pending Inbox or direct cross-owner approval controls are provided.

Task state is read from `tasks.phase`, not reduced from child status or Decision presence.

| Durable Task phase | Normal next action |
|---|---|
| REVIEW_REQUIRED | START_TASK_REVIEW |
| REVIEWING | WAIT_FOR_REVIEW_COMPLETION while active; WAIT_FOR_REVIEW_MATERIALIZATION after a successful raw result; RESOLVE_REVIEW_RESULT for INVALID/SCOPE_CONFLICT; RETRY_REVIEW_OR_ABANDON after terminal failure or an empty result |
| REVIEW_APPROVAL_PENDING | REVIEW_CONFIRMATION_REQUIRED |
| IMPLEMENTATION_READY | START_EXECUTION |
| IMPLEMENTING | WAIT_FOR_IMPLEMENTATION |
| VERIFICATION_READY | START_VERIFICATION |
| VERIFYING | WAIT_FOR_VERIFICATION |
| ACCEPTANCE_PENDING | RESOLVE_TASK_ACCEPTANCE |

Terminal Task status yields WORKFLOW_COMPLETE. Lifecycle and exact recovery facts may
block actions while retaining the persisted phase. An interrupted terminal Execution
permits Task Acceptance once its operation claim is safely released.

The product action APPROVE_AND_START resolves the existing APPROVE decision with
START_AUTHORIZED in one writer transaction. It creates the initial revision, one
Execution and its durable start effect; the existing worker starts it after commit.
REVISE requires `reviewFeedback`, records `CHANGES_REQUESTED`, and atomically
creates and authorizes the same Task's next Review. The Task enters REVIEWING and
the Review continuation worker starts the exact reserved Run; no second
START_TASK_REVIEW command is exposed. REJECT cancels Task. Approval always fences `task.current_review_id`; source Review IDs
are provenance, not user-selectable historical approvals. Exact receipt replay preserves
the original result even after later transitions.

Task Acceptance ACCEPT succeeds Task; REJECT cancels it. REQUEST_CHANGES creates a new
immutable revision and Execution under the same Task. REQUEST_REVERIFICATION creates a
verification-only Execution under the same revision. Backend retries reuse business Run
identity and do not create extra candidates.

Durable ProjectContext is not renamed. Both `READY` and `NO_GUIDANCE` are usable
states with a validated Project Module Map;
`NO_GUIDANCE` means lightweight bounded production discovery succeeded without
project-level guidance, not that no Project understanding exists. A usable
snapshot remains authoritative while refresh is `RUNNING`; if refresh fails,
the prior snapshot remains usable and the failure appears as recovery information.
Only first bootstrap without a usable snapshot blocks Review. Feature Area creation is
independent of context readiness.

Before a review, GPT receives the bounded Project Module Map and supplies either
a focused PRIMARY/SUPPORTING module plan or explicit repository-wide intent.
Bridge resolves module keys into one Repository's exact production roots,
persists that initial grant, and resolves a sole Repository plus the default or
stable enabled Codex configuration mechanically. Multiple Repositories remain a
semantic choice and are surfaced with canonical handles. Internal Module IDs
remain classification detail, not relevance-selection handles. The Review base commit is the Repository HEAD recorded when the Review is created.

A Task Review may run in DISCOVERING. The first successful current Review approval
retains FeatureArea activation in the same transaction. Task title stays the engineering
intent during Review. Card rail, Console and JSON consume Task phase directly; child
Run status and results appear in Activity/Details.

## Model interaction rules

The authoritative instruction text is `ExternalServerInstructions` in
`internal/mcpserver/external_workflow.go`; Local MCP shares the business contract.

- Initialize a real Session before Project selection. Reuse that same aiSessionId
  for binding, `session_initialize` readback, workspace and authorized operations.
- For record-only intent, use feature_area_create CREATE_ONLY; CREATE_TASK guidance
  does not itself authorize a Review. For an explicitly authorized investigation with
  complete Task/Repository/Review input, use CREATE_AND_REVIEW and complete its
  mechanical first Review admission. For existing objects, use task_create then
  task_review_start, or task_review_start alone for an existing exact Task. ReviewDepth
  and model reasoning effort are distinct controls.
- The combination tool boundary is not a human confirmation boundary. Approval and
  Acceptance wait for an exact user decision; absent authorization or required input
  is a specific blocker, never a guessed value. Queries, displays, reconnects and
  completion notifications never create or review work. `nextAction.required` guides
  the client; it is not host auto-execution. These upper-layer rules never alter the
  verbatim Coding Agent instruction.
- After Review completes, read operation_get RESULT and current workspace_get
  at the exact Task/Decision. Explain scope, risks and recommendation, then wait
  for the user's explicit decision. Completion notification is not approval.
- Initial APPROVE freezes the definition and enters IMPLEMENTATION_READY.
  execution_start performs actual launch. START_VERIFICATION uses that same
  command's existing current-candidate verification reservation path.
- Verification success is not Task Acceptance. WAIT performs no progression
  write and does not cause continuous GPT polling.
- A first user-facing progress query calls `panel_show` once; later queries use silent reads, normally
  `COLLAPSED`; an explicit open/show calls it `EXPANDED`. It does not require a
  mechanical `workspace_get` first. `workspace_get`, `work_list`, and
  `operation_get` are internal SILENT reads. A business flow performs its
  authoritative actions and then presents the exact final state once when UI is
  appropriate; intermediate actions do not each insert a Card.
- `presentationHint.required` is true only for an authoritative pending user
  decision or runtime request in the exact returned scope. It is not a host
  render guarantee and is not inferred from `REVIEWING`, cancellation, recovery,
  retirement, or an unrelated Task. False does not prevent an ordinary or
  explicitly requested presentation.
- Card actions and refresh use their canonical business interface plus in-place
  readback. They never call `workspace_show`, ask GPT to rediscover the target,
  or turn display into approval. Text-only requests and non-UI hosts retain the
  SILENT/text workflow.
- Display, refresh, expand and system completion are never user approval.
  These are upper-layer rules; Coding Agent instructions remain verbatim.

## AI surface

Model-facing GPT MCP discovery registers the shared business tools below. The current model-facing set uses purpose confirmation. Local MCP retains its separately tested canonical command catalog. App-only tools are not model aliases. Actual SDK discovery is contract-tested.

| Tool | Contract |
|---|---|
| `panel_show` | Open the single Session panel once; later operations refresh inside it. |
| `panel_focus` | Silently switch the existing panel to an exact Project/FeatureArea/Task display within the same bound Session; does not change business state or authorize execution. |
| `agent_context_reset` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `agent_operation_recover` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `bridge_diagnostics` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `confirmation_commit` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `confirmation_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `confirmation_options_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `confirmation_reprepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `development_branch_create` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `direct_file_disable` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `direct_file_reconcile_task_authorization` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `document_create` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `execution_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `feature_area_lifecycle` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `feature_area_prepare` | Prepare area creation or `MERGE_FEATURE_AREA`. For a released Task completion merge, pass the exact `featureArea.completionTaskId`; Core freezes a successor plan for that original Task and current development targets. Preparation never repeats acceptance or dispatches Git. The successor still requires its formal user confirmation. |
| `feature_area_merge_retry_candidates` | Read exact rejected merge operations, including the original merge request and repository; never dispatches Git. |
| `feature_area_merge_reprepare` | Explicitly authorize one successor to an exact pre-effect merge refusal using a new request ID. Revalidates the unchanged workset and development context; UNKNOWN cannot authorize retry. |
| `feature_area_submit` | User-initiated submission of completed Task deliveries. Multi-repository Tasks share the frozen per-repository planner and dispatcher used by `MERGE_FEATURE_AREA`; exact request replay reads the saved plan and outcomes. Every Task commit must be reachable from a frozen merge input. UNKNOWN cannot authorize another dispatch. Console `COMMIT_FEATURE_AREA` uses the same command. |
| `file_implementation_accept` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `file_worker_claim` | One durable pull-delivery intent for an exact already-approved and started v2 DIRECT_FILE workflow, before any mutation. Returns unchanged instructions once; binds all member authority and preserves separately required verification. Enrolled writes/results require the exact worker claim. No model, cloud task or synthetic Run is created. |
| `file_worker_get` | Authenticated historical receipt/result readback only; no executable payload, redispatch, authority refresh or same-workflow worker recovery. See [Direct File pull workers](architecture/direct-file-pull-worker.md). |
| `file_implementation_result_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `file_implementation_result_submit` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `file_list` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `file_read` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `file_write` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `operation_cancel` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `successor_execution_prepare` | Prepare the unique successor for a canceled admitted Execution with no Run through the Core command; replay and conflicts use durable receipts. |
| `operation_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `project_inspection_recovery_continue` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `project_list` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `project_setup_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `recovery_purpose_repair` | Explicit legacy orphan repair with a complete typed proposal; Core revalidates the source and writes only a canonical intent/link and repair receipt. |
| `recovery_status_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `recovery_status_read` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `recovery_status_resolve` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `recovery_status_start` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `repository_file_read` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `repository_search` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `repository_unlink_commit` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `repository_unlink_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `repository_unlink_read` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `review_access_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `review_access_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `review_evidence_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `review_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `run_analysis_get` | Read natural Codex output, captured commands and check evidence, frozen report schema and exact sourceHash for GPT analysis. |
| `run_analysis_submit` | Record GPT-authored analysis of a completed Run with exact sourceHash and immutable requestId. Never dispatches Codex or approves/accepts work. |
| `review_result_get` | Exact immutable Review result or request-specific receipt. The pending read-compatibility update dispatches by executor/result protocol: Upper returns `report`; natural Coding Review returns recorded `analysis` and canonical `proposal`, with explicit state/source. It never parses native prose as a formal report. See `docs/validation/review-result-read-2026-10-24.md`; deployment status is recorded there. |
| `review_result_submit` | Structured Upper Review evidence, not user acceptance. An enrolled pull-worker Run requires its exact `workerClaim`; legacy unenrolled payloads are unchanged. |
| `review_worker_claim` | One durable pull-delivery intent for an exact already-admitted UPPER_LLM Review/Run under its original authenticated Session. Only the first committed response returns unchanged instructions and bounded access; retries never redispatch. No cloud/model launch or worker identity proof. |
| `review_worker_get` | Exact immutable pull-delivery receipt and known result; no executable payload, lease refresh or grant effects. UNKNOWN does not authorize retry, reassignment or same-Run recovery. See [Upper Review pull workers](architecture/upper-review-pull-worker.md). |
| `runtime_request_recovery` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `runtime_request_respond` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `runtime_request_user_decision_prepare_hash` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `runtime_request_user_decision_submit` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `session_binding_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `session_initialize` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `task_commit_reprepare` | Authorize one exact rejected Task commit successor; request replay returns its immutable receipt. |
| `task_commit_retry_candidates` | Read rejected Task commit operation identities without authorizing Git. |
| `task_commit_retry` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `task_prepare` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `task_stop_and_revise` | Return an exact settled no-Run Task to Review with its current predecessor and definition revision. Changes only phase; preserves history and creates no runtime or successor. A new Review and approval are separate commands. |
| `verification_continue` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `work_list` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |
| `workspace_get` | Current production interface; its closed input schema and Core authority are defined by the registered handler. |

Direct creation, direct Review start, direct execution start, and Project binding are retired from the model-facing catalog. Their purpose-specific `*_prepare` interface creates only an immutable pending confirmation intent. `confirmation_get` and `confirmation_options_get` are exact reads; `confirmation_show` is an app-only legacy presentation endpoint; `confirmation_commit` is the only submission entry and is model-discoverable so an explicit user instruction and the Card reach the same handler.

Confirmation failures in public output always carry a non-empty safe message
when the message field is present. Historical empty internal messages are
normalized only on read; stored receipt bytes and hashes remain immutable.
Typed business rejection and Host transport failure are distinct. In
particular, `OUTCOME_UNKNOWN` does not imply non-consumption and never permits
automatic redispatch of the original request. Review confirmation eligibility
includes typed stale reasons, and the commit transaction revalidates its
topology, development context, Task, Review and Decision fences.

Project-scope and multi-repository Review confirmations omit `repositoryId`
when no single Repository is authoritative. Their display metadata is resolved
from Project, Task, Review and Decision identities; presentation enrichment
failure cannot block exact intent or receipt readback.

`confirmation_show` publishes `devbridge/presentationInstanceId`, derived from
`aiSessionId`, `intentId`, `version` and `contentHash` only. A Host should use
it as the Card instance key so that a replacement intent mounts as its own
Card instead of being pushed into a still-mounted one; the value is a render
request, never evidence that the Host mounted or displayed anything. Two
intents over one component version therefore share the `ui://devbridge/confirmation/v5`
resource but never share an instance. A second intent shown on a handle that
already carries another intent is rejected with
`CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED` and no Card resource, and the
already-shown Card keeps its own frozen identity, state and options.
`sourceIntentId` is lineage data only: options, read, show and commit always
use the new intent's own identity, and a receipt read names only a request the
Card itself submitted or the intent actually recorded.

The public Confirmation read identity is the five fields
`aiSessionId + intentId + version + purpose + contentHash` taken together.
`contentHash` is required on every read surface
(`confirmation_get`, `confirmation_options_get`, `confirmation_show`) and on
commit; absent, empty and malformed digests are refused by the closed input
schema before Core resolves anything, and Core re-verifies the supplied digest
against the durable frozen intent before answering. A supplied identity whose
digest, `version` or `purpose` differs from the durable frozen intent is refused with
`CONFIRMATION_READ_IDENTITY_MISMATCH` at stage `INPUT_IDENTITY`. That refusal is
a pure read miss: it never changes intent state, a receipt or the request
ledger, never consumes a Decision and never mounts a Card. `intentId` and
`version` alone cannot address a read, `sourceIntentId` is never current read
identity, and a Task, Review, Decision identity or resource URI is not a
substitute. There is no hash-free public read that later gains commit
authority; internal id-only reads are diagnostic and emit no Card metadata,
capability or submission authority. A read after a lost response uses the
original `intentId`, `version` and `contentHash` exactly, optionally with the
`requestId` the Card itself submitted.

A `REJECTED_NOT_CONSUMED` outcome whose durable receipt matches the current
intent and request exactly is the only state that returns a Card to user
editing: `businessOutcome = REJECTED_NOT_CONSUMED`, `consumptionDisposition = NOT_CONSUMED`,
`consumed = false` and `nextAction = EDIT_SELECTION` together yield
`retryDisposition = EDIT_SELECTION_AND_RESUBMIT`, provided the intent itself can still
accept a command — `state = PENDING`, no frozen `commit` and an `expiresAt` in the future;
once any of those three fails, the same rejection re-derives to `REPREPARE_REQUIRED` and
nothing unlocks. On the admitted conjunction the pending commit
selection is released so the user may change the choice and submit again. The Card
names the state this single predicate reaches `EDITABLE_RETRY_READY`, and its frozen
feedback copy is `本次确认未被消费。请修改选择或重新确认。`. The
retry is a new user command with a new `requestId` and a new payload digest; the
original request, payload digest, receipt and rejection stay in the history as
recorded facts, and re-sending the original `requestId` only returns the
original rejection receipt. `redispatchSameIntent` remains `false`, and nothing
resends automatically. `OUTCOME_UNKNOWN` never enters this path
(`retryDisposition = READ_ORIGINAL_RECEIPT`), `STALE` requires
`REPREPARE_REQUIRED`, and `REQUEST_CONFLICT` unlocks nothing. A Card refresh, a
Host transport error or timeout, an unchanged Task or the absence of a successor
Review is never evidence of non-consumption.

Every consumption and retry fact above is derived by Core from durable state at the
read boundary. A caller may never supply or override `consumed`,
`consumptionDisposition`, `retryDisposition`, `manualRetryAllowed`,
`redispatchSameIntent`, `reprepareAllowed`, a receipt outcome or receipt request ID,
the durable intent's own `contentHash` (a supplied digest is verified against stored
content, never trusted as an identity the caller defines), `sourceIntentId` as current
read identity, a replacement version or a lineage
ordinal, and the public input schemas accept none of them. There is deliberately no
`force`, `retryAnyway` or `assumeNotConsumed` option anywhere in this contract.

A Confirmation intent is immutable after prepare: its content is never
overwritten in place. Replacing a still-pending intent creates a new `intentId`
at `version: 1` through `sourceIntentId`, and `version` is therefore not a
replacement ordinal, a lineage generation, a Decision version or a Task Review
ordinal. Replacement order is expressed only by the `sourceIntentId` chain and
never inferred from `version`. The exact immutable identity of one proposal is
`intentId + version + contentHash`, and a `version: 1` intent has no in-place
revision.

`confirmation_reprepare` is the formal `PREPARE_NEW_VERSION` action and the only
replacement constructor; it is an adapter over the existing Review prepare builder, so
there is no second intent path. The client supplies only `aiSessionId`, the rejected
source intent's exact `intentId`, `version` and `contentHash`, a fresh `requestId`, and
optional `reviewFeedback`. Core proves the source's recorded
`REJECTED_NOT_CONSUMED`, `consumed=false`, `REPREPARE_REQUIRED` and
`reprepareAllowed=true` receipt, reads the current Decision snapshot, and refreezes the
Decision version token, the Project topology revision, the development context hash and
the current formal Project members. The result is a new `intentId` at `version: 1` with
`sourceIntentId` lineage, and the rejected intent becomes `SUPERSEDED` while its receipt
stays readable. Reprepare never creates a successor Review, Run or start authority;
only committing the replacement does, and `PREPARE_NEW_VERSION` is a product action name
rather than an in-place upgrade of the rejected intent to version 2. Replaying the exact
`requestId` returns the same replacement, the same `requestId` with a changed payload
returns `REQUEST_CONFLICT`, and an attempt on the now superseded source is refused, so a
lost response cannot fork duplicates.

`confirmation_commit` accepts only `aiSessionId`, `intentId`, `version`, `contentHash`,
`requestId`, `variantId` and the structured selection, and never a caller-supplied
outcome, `consumed` flag, Decision status or version, successor identity,
`retryDisposition`, `reprepareAllowed`, `force`, `assumeNotConsumed` or `retryAnyway`.
Explicit text continuation is routed through the same public pair: `Continue
confirmation <intentId> version <version> contentHash <contentHash>` performs an exact
`confirmation_get` first and commits only a `PENDING` intent with no recorded submission
and available options; a recorded `REPREPARE_REQUIRED` rejection returns that typed
rejection and goes to `confirmation_reprepare` without any commit call, and an unknown
or unrecorded result is a receipt read rather than a resend. A typed rejection is never
reported as `recorded=false`, `OUTCOME_UNKNOWN` or `READ_ORIGINAL_RECEIPT`. A surface
with no commit entry reports `CONFIRMATION_COMMIT_TOOL_UNAVAILABLE` and states no
outcome instead of inventing one.

Project Inspection also exposes loopback HTTP `POST /v1/project-inspections/prepare`,
`POST /v1/project-inspections/confirm`, and
`POST /v1/project-inspections/{id}/start`. The old ContextProbe create/start HTTP
routes reject new writes and legacy resume. Read routes remain available for
historical results. See [Project Inspection canonical runtime v1](architecture/project-inspection-canonical-runtime-v1.md).


`feature_area_create` requires an explicit Project-bound session and engineering intent.
Its omitted mode is CREATE_ONLY. CREATE_AND_REVIEW requires explicit stable request
identity, complete Task and Review branches, and may create engineering objects and
start read-only Agent work; it is not a read-only tool.
Approval response includes the exact existing Task and immutable revision; no Execution is created.
Implementation preserves authored instruction bytes and reports a structured scope disposition through the existing result
contract. Prompt Neutrality requires exact instruction transport: authored bytes
== stored authoritative instruction == sent Coding Agent prompt. Bridge does not
append, prepend, rewrite, summarize, generate or fall back to a Goal.

GPT `task_review_start` publishes the currently enabled reasoning-effort
subset from the runtime `ExecutionAllowlist`, matching Review admission. Thus
`xhigh` appears in fresh input discovery only when enabled. Desktop capability
changes take effect through Core restart; historical output values remain
representable even when disabled for new Reviews.

Execution `tokenUsage` carries `status` (`AVAILABLE`, `PENDING`, or `UNAVAILABLE`)
and includes a nested `usage` count object only when every contributing Run has
exact available accounting. Unknown and partial usage do not project as zero;
observed zero is `AVAILABLE` with zero counts. Counts are now read from
`tokenUsage.usage`, replacing the former flat Execution token-count object.
Accounting epochs are independent of canonical conversation identity; see
[epoch-aware token accounting](architecture/token-accounting-epoch-aware-closure.md).

The product Console uses the same envelope for each displayed operation:
`usage.status` is always `AVAILABLE`, `PENDING`, or `UNAVAILABLE`; its nested
`usage.usage` counts exist only for `AVAILABLE`. An exact zero total is therefore
displayed as `AVAILABLE` with `total_tokens: 0`; `PENDING` and `UNAVAILABLE`
display no synthetic zero or partial total. Cached-input and reasoning-output
counts remain optional, so an absent breakdown is unknown rather than an
authoritative zero.

Historical free-form Task creation accepted optional `goals`: an ordered list of 1–16 GPT-authored
Task Goals (each title at most 512 bytes). Goals are defined once and create
per-Execution progress rows. `operation_get` exposes ordered `goalProgress` with
`PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, or `SKIPPED` status; Tasks without
goals project an empty list. Same-status reports are idempotent and terminal
Goal transitions remain closed.

Tasks inherit the canonical AgentConversation of their FeatureArea. Caller-selected
Task context sources are retired from the active schema; stored legacy selections
remain readable historical data and are never used to guess FeatureArea lineage.
The shared `agent_context_reset` operation accepts `aiSessionId`, an existing
`featureAreaId`, and a stable `requestId`. It is distinct from both
`feature_area_create` and `agent_operation_recover`. Unknown create fields such as
the former reset action are rejected by the closed schema.
Reset rotates the current conversation into history and creates a successor with
no backend Thread. Replaying a request ID returns the same reset result. Active
Agent work returns `FEATURE_AREA_BUSY`; reset never deletes workflow objects or
injects an old-context summary. Local MCP `decision_respond` remains a separate
Decision transport and now requires its own stable `requestId` for retry-safe
Task confirmation resolution.

`operation_get` requires `aiSessionId` and an exact typed target copied from a
projection: kind REVIEW, EXECUTION or INSPECTION plus operationId, with optional
ownerTaskId/runId assertions. RESULT, EVIDENCE, ACTIVITY and RUNTIME_REQUESTS are
closed optional includes. The bounded response keeps correlated business identity,
Review proposal, Core Run facts, AgentClaim and evidence in separate source fields.
Complete runtime request identities are supplied only through the explicit runtime
include; telemetry never overrides durable Task phase or decision authority.

The Review evidence boundary is explicit. `authorizationStatus` reports the
durable Review grant; `repositoryInspectionObserved` and the three
`*HostToolOperations` counters cover Bridge Host Tool events only and carry
`observationBoundary=BRIDGE_HOST_TOOLS_ONLY`. They do not count every allowed native
backend inspection mechanism. A successful Run may therefore have a result with zero
observed Host Tool operations; summaries state that the Review completed and never
manufacture observed inspection evidence.

Admission-time repository provenance keeps `reviewBaseline`,
`repositoryHEADAtAdmission`, `branchKind`, full `branchRef` when available,
`workingTreeDirtyAtAdmission`, capture time, and
`inspectionMode=FEATURE_AREA_WORKSPACE` as distinct facts. Current drift is reported
separately and never guesses that a SHA belongs to `main` or `master`.

The internal Agent Host Tool Contract v1 declares exactly four tools together on
the first FeatureArea `thread/start`: `repository_search(query, paths?, limit?)`,
`repository_read(path, startLine?, endLine?)`, `repository_list(path?, limit?)`, and
`report_goal_progress(goalId, status)`. These tools are not GPT/Local MCP discovery
entries and accept no model-supplied operation routing identities.

PRIMARY/SUPPORTING selection is `ReviewFocus`, not authorization. Product Review
read authority is the selected Repository. ScopeExpansion is read-only legacy
history in v0.1. New proposals and decisions are unregistered, the production
facade rejects mutation with `SCOPE_EXPANSION_LEGACY_ONLY`, and active workflow
projections ignore historical pending rows. Neither `scope_expansion_propose`
nor `ui_scope_expansion_decide` is a production MCP tool. Historical rows remain
unchanged; they never gate same-Repository traversal or change Task `ExecutionScope`.

Bridge hard-enforces structural authorization: Project/Repository ownership,
repository-relative containment, traversal and symlink escape prevention, and
the separation between Review and implementation authorization. Review focus and
role/profile are routing metadata only. The complete GPT-authored instruction is
persisted and sent verbatim; Bridge neither adds Reviewer prose nor classifies
command or path text.

The app-private actions are not model-visible choices. The complete registered
private catalog is:

| Private tool | Responsibility |
|---|---|
| `dev_bridge_card_get` | Pure authoritative Card projection read. |
| `dev_bridge_panel_get` | Caller-owned Session operation references, including unbound Project and pre-creation proposals. |
| `ui_approval_get` | Exact runtime Approval readback for the current Session. |
| `ui_approval_decide` | Explicit panel decision through the original Core Approval contract; retries read the existing entity without redelivery. |
| `confirmation_show` | Legacy exact confirmation resource; formal children now mount inside the single panel. |
| `confirmation_discard` | Explicit user action to retire an exact unconsumed proposal atomically. Consumed work is only read back; late submissions of the retired proposal cannot execute. |
| `task_action_conflict_resolve` | Explicit user selection of one original Task chain at an exact generation. Retires unconsumed siblings only; never drops a different consumed or unknown effect. |
| `confirmation_submission_withdraw` | Explicit card action to atomically fence an uncertain original submission, or return its already recorded receipt. It never confirms, dispatches or rolls back business work. |
| `details_show` | Legacy exact read-only resource; new details stay inside the panel. |
| `workspace_show` | Legacy workspace resource; the model uses panel_show once. |

Panel RPC discovery explicitly exposes `agent_operation_recover`, `runtime_request_user_decision_prepare_hash`, `runtime_request_user_decision_submit`, `runtime_request_recovery` and `run_analysis_get` to both model and app. App visibility is transport discovery, not authorization; the existing exact Session/target/version and user decision checks remain authoritative. File access, report submission and generic preparation tools remain model-only.

The schema133 candidate adds a shared `taskView.actionNode` containing the Task
node identity, generation and safe occupancy summary. Task actions carry
`target.taskActionGeneration`; new task-bound prepares preserve the observed
`taskActionGeneration`. Legacy prepares that omit it capture Core's current
generation before validation and compare it again in the writer transaction.
Historical payloads keep the omitted zero field and their original hashes.

A consumed unknown parent continues to occupy the Task across Sessions and
restarts. Only exact canonical completion or a proven original-chain exit can
release it; release reasons are retained in immutable `task_action_releases`.
The existing no-Run `STOP_AND_REVISE` contract is exposed on both panel and WebUI
when Core proves that exact canceled admission is the current recovery node.
It preserves the Task and prior steps and never dispatches a Run. See the
[shared Task-chain contract](architecture/current-workflow-contract.md#task-当前待操作链路未部署候选).

One card click maps to one user decision; no user is asked to confirm then apply,
or confirm then consume then start. The former split TaskPlan/ExecutionProposal
confirm/apply/reject/start HTTP routes remain removed. Web Task actions use
`POST /ui/api/tasks/{id}/action?project_id=...`: the adapter re-reads exact Task
capabilities and delegates to canonical Core Decision/Cancel/Recover owners.
`POST /ui/api/featureAreas/{id}/action?project_id=...` accepts CREATE_TASK and
RESET_AGENT_CONTEXT only after a fresh capability check; Task creation persists
the same Core semantic intent and does not start Review. Existing explicit FeatureArea retirement remains
available. These adapters do not own workflow transitions.

The default production component is `ui://devbridge/card`. A workspace entry
requires valid aiSessionId, presentationRequestId, optional Project/FeatureArea/Task/
Decision assertions and optional exact Operation target. workspaceIdentity scopes
Session/Project; entryIdentity scopes one caller presentation intent. Same request
and parameters replay complete identity plus private initialization metadata;
changed parameters conflict. Different replies may create different entries. The
process-local receipt cache does not claim durable business idempotency, host
replacement, conversation-turn detection or delivery. Host visibility is UNCONFIRMED.

COLLAPSED renders one compact 40px component row, with keyboard-accessible expand.
Expand validates and freshly reads the exact target in place, without a GPT message,
show call or business mutation. Collapse reports actual content height. The host's
outer padding is outside component control. The component's selected FeatureArea/
Task is local, including explicitly empty selection; another card cannot change it.
Historical decisions remain exact and resolved/superseded targets are read-only.

The visible expanded component refreshes with bounded exact reads. Hidden/collapsed
entries do not run independent full-workspace polling loops. Existing mounted Review
completion notification retains its exact durable identity and process-local claim;
refresh/mount is not a reason to resend. There is no new background GPT wakeup service
and no promise of notifications while unmounted. Completion notifications instruct
operation_get plus workspace_get, followed by explanation and an exact workspace_show
entry while awaiting explicit approval.

Review wakeup reserves its one process-local attempt only after optional model-context
update, a fresh authoritative read, exact identity revalidation and host transport
selection. Missing transport or failed preflight does not consume a reservation. After CLAIMED,
only final local fencing, the widget receipt and one selected message call remain.
ALREADY_ATTEMPTED and uncertain claim RPC results stay UNKNOWN, not delivered; no
release, reset or TTL is provided. If the Card becomes stale after reservation, it
sacrifices notification liveness and does not force a stale send. Local proof of a
legacy claim without messageAttempted retains the narrow explicit retry compatibility;
a new Card cannot use ALREADY_ATTEMPTED alone as permission to send again.

All reads and writes are SILENT and carry no UI resource/template except the three
explicit show tools. Model response envelopes distinguish result, context, workflow
and presentationHint. Full UI workspace data lives in tool-result metadata rather
than being repeated in the model-visible show result. Successful writes remain
successful if readback fails and report refreshRequired separately. A show result is
not followed by recursive show; host visibility remains UNCONFIRMED until the host
reports it.

Pending decisions contain real business decisions and runtime requests only.
RETIRE remains FeatureArea management; CANCEL_EXECUTION and RECOVER remain exact
operation capabilities. The current Task action area contains one cancellation
button. Settings are in the expanded Header. One operation popup can be open at a
time; outside-click and Escape close it without submission or click-through and
restore focus. All requests use component-wide Loading/inert handling, generation
fencing and stale action disablement. Drafts survive refresh. Loading does not last
for the Coding Agent's entire operation.

See [current consolidation contract](architecture/gpt-surface-consolidation-v1.md)
and [manual preview and validation](validation/gpt-surface-consolidation-v1.md).
Earlier Card closure documents retain historical evidence only; their old catalog,
lease, and no-session fallback rules are superseded here.

## Technical internal audit reference

This section intentionally retains implementation terminology for audit,
operations, and durable-history inspection. It is not the normal model-facing
teaching surface.

- SQLite has ordered migrations; v14 adds Feature Areas and deterministic historical
  Task linkage as described above. State changes and Event Ledger append remain
  transactional; the Ledger is append-only operational history, not event
  sourcing. v31 adds immutable, request-idempotent Task confirmation resolution receipts.
- `TaskPlan` audit states remain `PENDING_CONFIRMATION`, `CONFIRMED`,
  `REJECTED`, and `APPLIED`. `ExecutionProposal` retains
  `PENDING_CONFIRMATION`, `CONFIRMED`, `CONSUMED`, and `REJECTED`.
- Run remains the authoritative execution checkpoint. Its lifecycle remains
  `CREATED → PREPARING → RUNNING ↔ WAITING_APPROVAL → COLLECTING → SUCCEEDED`,
  with `FAILED`, `INFRASTRUCTURE_FAILED`, and `CANCELED` terminal outcomes.
- Evidence records BaseCommit, actual HeadCommit, dirty state, deterministic
  changed files, and bounded diff. AgentClaim is stored separately and never
  proves Git or verification facts. Token accounting remains Run-scoped.
- Project selection, immutable AISession project binding, Workspace/Worktree/
  Thread continuity, RuntimeSupervisor ownership, ScopeExpansion, and Approval
  delivery auditing remain durable internal safety mechanisms.
- `DataRoot` is the local operational storage boundary, not a model-facing
  product fact. `-data-root <path>` derives `state/`, `worktrees/`, `evidence/`,
  `logs/`, `runtime/`, and `artifacts/`; project repositories remain external.
  Switching roots opens an independent environment and performs no migration or
  copy.
- `cmd/devbridge` is the sole DataRoot writer and serves the loopback control
  API, bounded Console, administrative test surfaces, and private GPT/Local MCP
  handlers. `cmd/devbridge-mcp` and `cmd/devbridge-local-mcp` are loopback
  proxies configured with `-core-url`; each verifies the Core DataRoot identity
  before serving model-facing MCP plus health/readiness endpoints. Internal HTTP operations remain available to
  the Console and test harness, but are not additional model tools.

Obsolete primitive names—such as `session_open`, `module_*`,
`task_proposal_*`, `execution_proposal_*`, `run_*`, `approval_*`, and raw
ContextProbe operations—are not model-visible aliases. They remain internal or
app-private only where needed for audit and implementation.

## Repository unlink

Core exposes read-only prepare/read/list and one commit command:

- HTTP: `POST /v1/repository-unlink/prepare`,
  `GET /v1/repository-unlink`, `GET /v1/repository-unlink/list`, and
  `POST /v1/repository-unlink/commit`.
- MCP: `repository_unlink_prepare`, `repository_unlink_read`, and
  `repository_unlink_commit`.

List defaults to ACTIVE resources; historical rows require
`include_historical=true`. Commit accepts only request, Project/Repository,
expected registration generation, expected topology revision and reason. Unknown
HTTP fields and additional MCP properties are rejected. The returned projection
contains Core eligibility, blockers and retained historical identity. Full
semantics are in [the schema-v84 closure](architecture/safe-repository-unlink-closure.md).

## Lifecycle, history and retries

feature_area_lifecycle has disjoint ACTIVATE, DECOMPOSE(children) and
RETIRE(confirmed=true) schemas and delegates to the original lifecycle Core.
Retirement retains durable stop intent and canonical cancellation/recovery;
no generic status update or approval is introduced. ScopeExpansion is legacy
read-only history and remains non-actionable.

work_list kind=TASK lists semantic development Tasks only. kind=OPERATION lists
Review, Development and Inspection with exact existing operation targets. Its
response is bounded by limit/offset and preserves scope assertions. operation_get
uses targets returned by these projections, without a new registry. Review proposal,
Execution outcome, Verification evidence, AgentClaim and runtime telemetry remain
separate typed sources; the requested includes control the optional bodies.

Business request receipts, execution reservation identity, runtime response identity,
file workflow IDs and presentationRequestId retain separate retry contracts.
Cancel accepts exact owner or Execution identity copied from capabilities, validates
the Run when present, and preserves legitimate pre-work cancellation without a Run.
Inspection cancellation is not broadened. ACK is not termination and UNKNOWN_ORPHANED
is not evidence of a physically dead process.

### Task-centric WebUI workbench v1 (2026-09-12)

Project navigation is Overview, FeatureAreas, Tasks, Resources and Diagnostics.
The production Console and Desktop iframe continue to share Core `/ui/`.
Task status/phase, current Review and current Execution remain shared Core facts.
No tool aliases, workflow registries, database migrations or mutation authorities
were added by this navigation change.

New/additive local read contracts:

- `GET /ui/api/work-tabs/tasks?project_id=…&limit=25&offset=0&search=…&feature_area_id=…&status=…&phase=…&branch=…&kind=…`
  returns `{items,total,limit,offset}`. Filtering precedes paging; item IDs are
  semantic Tasks. `status=__terminal__` is a read filter for the area history.
- `GET /ui/api/tasks/{taskId}/runs?project_id=…&limit=20&offset=0&purpose=…`
  returns `{items,total,limit,offset}`, with limit at most 100. Optional purpose
  is exactly SCOPE_REVIEW, IMPLEMENTATION or VERIFICATION. Ordering is immutable
  createdAt then Run ID, ascending; elapsed/status never determine identity.
- `GET /v1/runs/{runId}?project_id=…&task_id=…` and the existing `/prompt` route
  validate Project → Task → durable Review/Execution → Run in the server.
  Omitting task_id retains the project diagnostic context, including Inspection.
- `GET /ui/api/tasks/{taskId}/definition-revisions/{revisionId}?project_id=…`
  reads an immutable definition with exact project, task and area fences.

`TaskView.details.runs` uses the same row builder, defaults to 20 entries and
adds `runsTotal`/`runsTruncated`. Its legacy `executionId` **continues to mean
AgentExecution ID**; it is deprecated, not silently redefined. New callers use
`agentExecutionId`, `taskExecutionId`, `reviewId`, `operationKind`, `operationId`
and the exact `runPurpose`. `purpose` retains its historical presentation value.
Frozen model/reasoning/config source, legal nullable times and completion status
are additive. Run detail exposes `frozen_config`, `runtime`, `elapsed_ms`,
`duration_complete`, exact epoch and explicit review evidence/result availability.
Runtime RawMessage payloads remain objects under the existing strict schema.

The typed local detail route contains project, rootTask, TASK/RUN, exact target,
source, activeTab and generation. Shareable fragments use
`#project=…&task=…&run=…&tab=…&source=tasks`; diagnostic links may omit task.
First restoration always re-reads the server. Navigation, expansion and polling
are read-only. No local storage field or fragment can select the Core's current
Review/Execution or authorize an action.

See [implementation, verification and manual acceptance](validation/webui-task-centric-workbench.md).

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](architecture/codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.


## v79 App Server continuity observation

`operation_get(include=["RUNTIME_REQUESTS"])` 新增 `runtimeObservations`；runtime Snapshot 新增 `runtimeObservation`。只读字段包括 appServerInstanceId、instanceEpoch、instanceStatus、connectionState、threadId、turnId、threadStatus、requestReattachment、observedAt、sourceMethod、failureReason、summary。这些是已缓存的协议事实，不是业务 Decision。GET 不启动进程、不 resume、不提交回答。

绑定实例的 RuntimeRequest / UserDecision identity 增加 `appServerInstanceId` 与 `appServerInstanceEpoch`，并参与决定 payload SHA-256；需连同 current runtimeEpoch/connectionId、原 Run/Thread/Turn、runtimeRequestId、expectedState、稳定 submissionRequestId 原样提交。legacy unbound tuple 为省略/零；部分填写或错 instance epoch 拒绝。originalIdentity 记录 reattachment 前身份，pendingReplayVerified 仅表示当前连接实际匹配过原 pending replay，不授权模型生成答案。

Core reopen 只恢复已持久绑定的同一 0.149.0 实例。新连接收到同 request ID、method、payload 才可使用原不可变答案；idle/terminal 不代替 resolved。实例丢失展示 session lost，保留原始请求与答案，不向替代实例重发。详见 [固定协议与验收](architecture/codex-request-cross-core-continuity.md)。
### Invalid Coding Review result resolution

When a successful Coding Review Run has an INVALID or SCOPE_CONFLICT
materialization, workflow reads expose the durable
`INVALID_REVIEW_RESULT_RESOLUTION:<reviewId>` Decision and the actions
`REQUEST_RE_REVIEW`, `OPEN_PROJECT_REPOSITORIES`, and `ABANDON_TASK`.
`REQUEST_RE_REVIEW` carries only `reviewFeedback`; Core derives the successor
Review, Run, targets, backend and start authority from the exact current Review.
Coding Agent structured output identifies frozen targets only through closed
`target-N` aliases. Canonical repository/member/version/origin identities are
injected during Core materialization and never accepted from model output.

### FeatureArea merge recovery (v119)

A confirmed multi-repository merge reserves immutable per-member operations.
Each operation has at most one dispatch and one exact Core outcome. Receipt
readback precedes live Git/workspace checks. Lost receipt persistence leaves
UNKNOWN: neither restoring HEAD nor another confirmation may repeat Git.
Only a durable `REJECTED_NOT_CONSUMED` outcome permits
`feature_area_merge_reprepare`; it retains the original plan, member, accepted
Task commits and development context, with a new operation/request and exactly
one predecessor-linked successor. Exact reprepare replay does not dispatch.
Legacy incomplete merges without operation receipts remain fenced; migration
does not infer applied or rejected outcomes from Git. Canceled or abandoned
partial Task delivery never qualifies a FeatureArea for merge.

### Console multi-repository delivery recovery

Delivery summaries use per-repository receipts in both Task and FeatureArea views, including partial completion and `NO_CHANGE`; a scalar commit SHA is not synthesized. Task integration summaries include all completed merge receipts for that Task, rather than only the most recent FeatureArea merge.

The Console FeatureArea action endpoint accepts `RECOVER_FEATURE_AREA_MERGE` with the exact original `mergeRequestId`, and `REPREPARE_FEATURE_AREA_MERGE` with `mergeRequestId`, `repositoryId`, `predecessorOperationId` and a new `requestId`. Recovery requires an existing parent and never prepares a replacement plan. Reprepare delegates to the same immutable retry authority as MCP. Replaying either command keeps the original identity; stale or foreign identities are rejected by Core.

`commit.mergeRecoveryState` distinguishes `RESUMABLE`, `REPREPARE_REQUIRED` and `UNKNOWN`. Only the first two expose write actions. UNKNOWN permits receipt observation only. An interrupted operation is not presented as running merely because its parent has no completion timestamp.

### Task review selection and explicit implementation confirmation

New `task_prepare` proposals with `CREATE_TASK_AND_REVIEW` prepare both `UPPER_LLM`
and `CODING_AGENT` instructions and an explicit Repository. The user chooses
`selection.reviewMode` on the Card: `UPPER_LLM`, `CODING_AGENT`, or `NONE`. The first
two require `startReview: true` and the matching frozen variant. `NONE` requires
`startReview: false`; it persists a Review-skip receipt and creates no Review or Run.
Legacy omission of `reviewMode` preserves existing `startReview` semantics.

For a skipped, single-repository Task, `execution_prepare` action
`CONFIRM_AND_START_IMPLEMENTATION` takes the exact Task revision plus complete
Coding `workSpec` and executable `verificationSpec`. One user confirmation selects
the backend and runtime, freezes the definition, and starts that exact definition.
The durable definition and start steps share one consumed confirmation; recovery
reuses their original identities. Legacy `CONFIRM_IMPLEMENTATION` remains
definition-only; its later `START_APPROVED` still requires explicit runtime and target. Skipping Review alone
cannot pass the implementation admission gate. This path currently supports a
single explicit Repository and Coding implementation; other implementation layouts
retain their existing reviewed approval contracts.

For `APPROVE` and all implementation actions, each `variants[i]` must include
`implementationMode`, complete `workSpec`, and complete `verificationSpec`.
`variants[i].implementationMode` and `variants[i].workSpec.implementationMode`
must match (`CODING_AGENT` or `DIRECT_FILE`). Copy the complete reviewed or frozen
plan; do not replace it with a label-only decision. `implementationMode` is not
an `execution` field. Both root `action` and `execution.action` are required;
`execution.executionId` is an empty string before an Execution exists. Only
`ACCEPT`, `REJECT` and `REQUEST_REVERIFICATION` use one decision-only variant
(`id`, `label`, `summary`, optional `displaySummary`).

`START_DIRECT_FILE`, `RECOVER_DIRECT_FILE` and `CANCEL_DIRECT_FILE` each carry one
complete `DIRECT_FILE` variant matching the frozen plan. Initial start omits
`execution.workflowId`, `runtimeEpoch` and `grantGeneration`; recovery and cancel
require those exact current workflow fences and a positive generation. Preparing
a confirmation persists only a pending candidate. It neither consumes approval
nor grants file access or starts implementation.

Executable admission rejects required `CUSTOM` checks with the typed failure
`UNOBSERVABLE_REQUIRED_CHECK`, rather than `CONFIRMATION_FACTS_UNAVAILABLE`.
An instruction to read files is valid descriptive report content but does not
provide a deterministic required-check observation path in the current verifier.
Keep the reviewed constraints intact and obtain a revised verification proposal;
never silently make a required check optional, add command execution, or infer a
`USER_WAIVED` verification waiver to pass admission.

Review read projections include `activity.executionMethod`, `stage`, `summary`,
optional `lastActivityAt`, `inactive`, and optional `notice`. Upper Review access
also exposes `reviewCreatedAt` as a read-only clock anchor. An AI file Review with
no observable activity for five minutes receives an inactivity notice, without
changing lifecycle, activity timestamps, authorization, or dispatch state.

When a create-only Task has not selected a Repository yet, `NONE` may still create
it. The later `CONFIRM_AND_START_IMPLEMENTATION` proposal must explicitly supply
`execution.repositoryId` and its full immutable `baseCommit`. The Card displays
that target before the user confirms; an already selected Task baseline is retained.

### Codex verification after Direct File implementation

For a frozen DIRECT_FILE + RUN definition, START_DIRECT_FILE explicitly selects
Codex backend/runtime for verification. The consumed selection is immutable and
is separate from the earlier Review runtime. Multi-repository verification needs
an approved primaryMemberId and exact repository-qualified executable checks.

file_implementation_result_submit atomically persists the file result, closes its
write grant, binds a verification-only Execution to that exact result/revision,
and advances to VERIFICATION_READY. The Facade reserves and dispatches its one
VERIFICATION Run through the existing durable handoff. Response fields include
executionId, verificationRunId, verificationStatus and optional
verificationStartError. Failure to dispatch does not roll back the file result;
read the original Execution and use verification_continue for its exact target.
Pure reads never dispatch. No IMPLEMENTATION Run is created for GPT file work.

Both file_implementation_accept and the normal execution ACCEPT decision enforce
complete required Codex evidence plus unchanged file-result/verification
snapshots, and retain Direct File delivery ownership. Missing command observations
remain blocking even if the model reports PASSED. Existing explicit NONE waivers
retain NOT_RUN history. Historical immutable reports are revised through Review,
never silently rewritten by this migration (SQLite v128).


## Same-turn Codex steering

`GET /ui/api/runs/{id}/session` includes `steering.capability` and the exact Run's
`steering.receipts`; `session.runtime.nativeThreadStatus` is a native observation.
It has no Task transition or approval authority.

`POST /ui/api/runs/{id}/steering` is a local-operator command using the existing
WebUI authentication boundary. Its body contains the capability's exact runtime
Identity, `submissionRequestId` and literal `text` (nonblank UTF-8, at most 16 KiB).
Clients cannot supply `submittedBy`; Core derives it from the authenticated actor.
Core rechecks the active operation claim, epoch, native binding, frozen identity,
current connection, exact Thread/Turn, pending questions/approvals, cancellation,
finalization and prior unresolved sends in the dispatch transaction.

The immutable input is stored before a single native `turn/steer` call. The
receipt states are `DISPATCHING`, `ACCEPTED`, `REJECTED`, `UNKNOWN`. An unfinished
dispatch after a crash is displayed as uncertain and is never retried. Exact
request replay is read-only even after turn completion; actor, target or payload
substitution conflicts. Neither `turn/start` nor configuration overrides are
available through this command. At most 100 submissions are retained per Run;
a Run at this limit accepts no further steering.

Schema v129 adds only `runtime_steering_receipts`; existing workflow records,
required checks and approval transitions are unchanged. Native history recovery
uses metadata-only `thread/read`, bounded `thread/turns/list` pages with
`itemsView=full`, and `thread/resume(excludeTurns=true)` on modern backends. The
explicitly qualified 0.149.0 backend retains its legacy full-turn read.


### Native execution and GPT analysis (schema 130)

New Codex Turns use `NATURAL_V1` without `outputSchema`. Core records transport
completion, original text, exact Thread/Turn identity, immutable command evidence
and observed verification checks independently of engineering analysis. A native
`SUCCEEDED` does not assert engineering correctness or user acceptance.

For Review, Verification, Inspection and Recovery diagnostics, use
`run_analysis_get(aiSessionId, runId)` after completion. `REQUIRED` means GPT must
analyze the original output and captured evidence, then submit `run_analysis_submit`
with that exact `sourceHash`, a stable `requestId`, and a report conforming to the
returned `reportSchema`. Implementation summaries are optional. Core never parses
native natural text as this report. Invalid submissions can be corrected without
rerunning Codex; exact receipt replay is idempotent. Observed command outcomes
cannot be replaced by GPT claims. Current owner, Project/caller, lifecycle and
frozen workset checks still apply. Submission does not approve implementation,
accept delivery, settle UNKNOWN answers or start new work. Existing dispatched
legacy Turns retain their original output contract and historical read semantics.


### Single GPT panel (2026-10-23)

After session_initialize returns a real Session, the model calls panel_show once.
The component requests the host's PiP presentation and retains one panel for Project
binding, FeatureArea/Task creation, Review, implementation, verification, results and
history. Formal confirmation children retain separate frozen identities and submit
only their own explicit user click. Session feed reads cannot select a Task or grant
permission. Unsupported PiP and existing transcript entries remain host concerns;
no read or notification is allowed to hide another conversation or approve a command.
Ordinary ChatGPT conversations retain the same panel flow; Work-specific notifications
are not required to create, approve or read a Task. Local/Desktop command admission
and all Core authority remain unchanged.

### Existing panel navigation

After the user names an existing Task to view or continue, resolve its accurate identity with `work_list` / `workspace_get`, then use `panel_focus(aiSessionId, projectId, featureAreaId?, taskId?)`. The explicit Project must match the existing Session binding. Task selection derives its actual FeatureArea; a supplied Area is an assertion and mismatches are rejected. Ambiguous names require user selection. Omit Task for Area overview, or both work IDs for Project overview.

The same model/app tool serves manual navigation. It returns a versioned, ephemeral UI focus through the existing `dev_bridge_panel_get` feed. The open component reads the exact new projection before changing its view, retains the prior view on failure, ignores older focus versions, and never remounts a Card. An explicitly empty Task remains empty in an EXACT Area read. Read-only historical entries cannot follow navigation into a different target. Focus survives MCP client reconnects in the same server process; the component's existing exact display snapshot remains the fallback after a server restart. No Core business table, confirmation, authorization or execution is changed by navigation.

### Native runtime approvals

`runtime_request_respond(kind=RUNTIME_APPROVAL)` supports two disjoint contracts: a legacy `approvalId` plus typed decision for a real Approval entity, or a native `resolution` bound to the original runtimeRequestId, complete runtime identity, requestId (including integer zero), itemId, expectedState=PENDING and original request payload SHA256. The native response is an explicitly selected `availableResponses` value; the original Core/runtime validates it. Native permission is never submitted as a semantic UserDecision. Raw semantic CODEX_REQUEST responses remain prohibited.

The unified ChatGPT panel, WebUI Task detail and runtime-session workbench display native command/file/permission requests, original command/cwd and each scope separately. A final user confirmation is required. Unknown responses are never automatically resubmitted. Current exact runtime approvals take presentation priority over generic implementation waiting, without changing the Task lifecycle.

### Implementation scope-conflict routing

An exact current Implementation failure_code=SCOPE_CONFLICT enters
ACTIVE / RECOVERY_REQUIRED with nextAction=RESOLVE_TASK_RECOVERY. The read projection
explains preservation of changed files, evidence inspection and conditional routing;
it grants no new action. A missing native result remains UNKNOWN. Historical
scope failures and Review/Verification failures cannot substitute for this identity.

Resolved CONSISTENT / RECOVERED_PARTIAL can use eligible same-revision
RETRY_IMPLEMENTATION with recoveryStatusId and recoveryContinuationInstruction.
REVISION_REQUIRED instead requires the exact typed RECOVERY_PURPOSE confirmation
and its new frozen successor definition; ordinary REQUEST_CHANGES cannot bypass
the recovery fence. Formal recovery revision does not automatically create a Review.
Use REQUEST_TASK_REVIEW / REQUEST_SUPPLEMENTAL_REVIEW only when Core exposes exact
same-Task eligibility and additional engineering review is needed. Conflicting,
insufficient or unavailable original runtime facts remain blocked.

See the [scope-conflict handling contract](architecture/engineering-scope-responsibility.md#implementation-scope_conflict-handling).
