# FeatureArea ownership (schema v46)

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

> Project-centric update (schema v76): [current boundary](project-centric-multi-repository-closure.md). Project/Workspace membership is not authorization. Multi-member Upper Review, v2 NO_START approval and explicitly started Direct File/delivery are complete. Coding runtime remains blocked pending exact isolation and same-canonical-Thread narrowing evidence; see [G0 findings](coding-runtime-capability-evidence.md). The single-repository descriptions below remain compatibility behavior.

Current Task/Execution authority follows the [Task-owned v0.1 model](task-execution-v0.1.md).
Task exists before Review and owns semantic workflow. Each Task has Review [0:N]
and Execution [0:N]; each Review has exactly one linked Run. Approval advances
the existing Task and freezes its initial immutable TaskDefinitionRevision.
Explicit Execution start authorizes implementation of that revision.
The [agent identity envelope](agent-execution-ownership.md) supplies runtime
identity, diagnostics and exact cancellation without another semantic Task.
Configured Upper LLMs convey user intent and Core enforces the exact state.

The independent Project-owned DevelopmentCycle supplies branch selection; see the
[implemented DevelopmentCycle boundary](development-cycle-work-model.md#implemented-boundary-development-branch-selection-schema-v33).
Task automatic commit and FeatureArea submission use the existing Git delivery
contract. FeatureAreaCycleBinding, release, deployment, scheduler and Delegation
aggregates remain future work. The v27 migration section below is historical.

```text
Project
├── Repository*
├── DevelopmentCycle* (current developmentRef authority per Repository)
└── FeatureArea
    ├── Task* (semantic DEVELOPMENT intent and workflow owner)
    │   ├── Review [0:N] → Run [1:1] (REVIEW, reviewId)
    │   ├── immutable TaskDefinitionRevision* (initial revision on APPROVE)
    │   └── Execution [0:N] → implementation / verification Run* (EXECUTION, executionId)
    └── AgentConversation* (at most one canonical current)
```

A FeatureAreaAgentClaim authorizes one current Agent operation. A Run records its
immutable Project, FeatureArea and real operation owner after the claim is released.
AgentConversation owns context lineage and its backend Thread; Run Thread/Turn
records interaction provenance. FeatureArea owns its canonical Workspace through currentWorkspaceId. Workspace
records exact Project/Repository/FeatureArea/origin baseline and worktree facts.
Tasks and Runs retain associations, never canonical workspace ownership.
Project ContextProbe discovery has a visible Project-scope INSPECTION Task and
Run. It creates no FeatureArea and acquires no engineering conversation claim.

Review resolves repository, configuration, base commit, focus, model and instruction
from its own durable definition. Host tools require its exact active Run, claim,
conversation, Thread/Turn and epoch. Repository-wide authorization remains separate
from PRIMARY/SUPPORTING relevance focus. Historical ScopeExpansion records retain
their exact Review grant bindings as read-only history. New proposals and decisions
are unregistered; ordinary traversal inside an authorized repository needs no expansion.

## Lifecycle and write ordering

DECOMPOSE creates every supplied child and SPLIT_INTO relation, changes the parent
and ends its canonical conversation in one SQLite transaction. Children start
independent in DISCOVERING. Lifecycle receipts bind action and ordered canonical
payload to requestId. Identical replay returns the original response, except RETIRE returns the current
RETIRING/RETIRED projection of the same intent; changed payload conflicts. All failure points roll back children, relations, events,
conversation state and receipt together.

Development Task inserts validate ACTIVE ownership in database triggers. Operation reservation
validates the real owner and lifecycle in the same write transaction as its durable
claim. ACTIVATE/DECOMPOSE check that no claim exists. RETIRE is accepted with a claim.
Admission first commits work that retirement then cancels; retirement first causes
admission to fail closed. No service-level precheck is the final authority.
UNKNOWN_ORPHANED is a valid retirement completion without rewriting UNKNOWN.
See [FeatureArea retirement](feature-area-retirement.md). Different FeatureAreas use
separate claims; existing repository/worktree resource constraints remain intact.

The shared tools are `feature_area_create`, closed
`feature_area_lifecycle(action=ACTIVATE|DECOMPOSE|RETIRE)`, `agent_context_reset`
and `agent_operation_recover`. Lifecycle branches delegate to the existing Core
methods; RETIRE requires explicit confirmation. Reset is idle-only;
recovery fences an unsafe exact operation. Neither action authorizes implementation.

## Immutable engineering intent

Task creation records ACTIVE / REVIEW_REQUIRED intent with no definition revision.
APPROVE of the current Task Review gate creates the initial immutable
TaskDefinitionRevision and advances that same Task. REQUEST_CHANGES preserves
the exact Task identity while atomically creating a new immutable revision and
successor Execution from the complete successor specs explicitly confirmed by
the user. Goal wording, natural-language constraints and legal scope may change
with that new approval; technical repository and access boundaries remain. REQUEST_REVERIFICATION
keeps the same Task and revision and creates a verification-only Execution.
IDs and per-Task ordinals identify immutable
history. Canonical v1 JSON and SHA-256 cover ownership, repository/base commit,
module/configuration/model/reasoning, problem statement, authored WorkSpec,
VerificationSpec and ordered Goals. Runtime status, WorkKind and timestamps are
excluded from the semantic payload.

The v46 next-run runtime selection is frozen separately on Approval/Execution.
Model, reasoning effort and Codex Fast Mode selected for the next Run do not
rewrite a TaskDefinitionRevision or the caller-authored prompt.

## Conservative migration from v27

- Review gains direct repository/configuration/baseline ownership. Original Task
  links become legacy_task_id; current Review insertions cannot supply them.
  Execution or exact persisted claim links. Unknown past ownership stays unknown.
- Tasks without FeatureArea, and ambiguous former Review carriers with no durable
  Task lists/counts. No new FeatureArea is invented and no historical Task is deleted.
- Old Task agent_context values are copied to immutable legacy_task_audit. They
  cannot be new input, continuation source or authorization. The old SQL column is
  retained only as immutable compatibility data; current domain/read models omit it.
- Workspace thread_id becomes immutable legacy_thread_id. It cannot be written by
  current workspace creation or used for canonical context. Run interaction facts
  and AgentConversation Thread ownership remain intact.
  cannot be proved. Read models label this LEGACY_UNKNOWN rather than inventing
  an exact snapshot from today's Task. Current revision rows start with current
  authored changes; old unknown history remains unknown.
- SPLITTING had no durable completion marker. Every old row is conservatively
  retained as DISCOVERING plus legacy_read_only and legacy_status='SPLITTING'.
  No current operation can use it. Unclaimed conversations end during migration;
  claimed history remains intact for recovery. Recovery releases the exact claim
  and ends old context without manufacturing a usable successor.
- Old Task-based scope expansions retain legacy_task_id with no guessed Review.
  They are readable but cannot authorize current work. Old Review grants remain
  immutable audit history; exact current grants are Review-keyed.
- The unused pre-TaskPlan TaskProposal mutation/consume API is retired. Historical
  rows remain readable; new writes cannot bypass current Task ownership.

The migration rebuilds required tables deterministically, restores runtime safety
triggers and runs foreign-key validation. Runtime epochs, claims, approvals, evidence,
artifacts, prompt hashes, result bodies and token accounting survive the upgrade.

## Validation boundaries

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.


## App Server reattachment 对 owner 的约束

v79 使用独占 DataRoot writer、当前 runtime epoch 与不可变 reattachment receipt 收养原 active Run/feature-area claim；不释放后重新竞争 owner，不创建新 operation。process、socket、backend 或 exact Thread/Turn 不匹配时不得收养。旧 epoch callback、已取消操作和未经 exact pending replay 的回答均受 fence 限制。进程 supervisor 仅管理进程与 endpoint，不是 feature-area owner 或第二套 workflow。见 [continuity 记录](codex-request-cross-core-continuity.md)。
