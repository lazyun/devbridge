# Dev Bridge Development Cycle & Feature Area Work Model

> Project-centric update (schema v73–v74): [implemented contracts and remaining workflow boundaries](project-centric-multi-repository-closure.md). Projects may have zero or multiple repositories; Session selection is Project-only. Workspace membership is not authorization. Multi-repository Review, execution and delivery remain explicitly blocked where the complete authority chain is unavailable; the single-repository flows below describe compatibility behavior.

Current Task/Execution authority follows the [Task-owned v0.1 model](task-execution-v0.1.md).
The [Task Review gate contract](review-approval-workflow.md) defines current schema v46
Review depth, disposition, admission and approval. These current contracts take
precedence over historical workflow and identity descriptions retained below.

## Current implementation workflow (schema v46)

```text
FeatureArea
└── Task                         semantic workflow owner
    ├── Review [0:N]
    │   └── Run [1:1]
    ├── TaskDefinitionRevision   immutable approved definition
    └── Execution [0:N]
```

Task exists before Review. `Task.phase` is the durable workflow authority;
`current_review_id` fences the latest Review. Approval acts on that existing
Task's Review gate and creates neither a Task nor an Execution. FeatureArea
continues to own its Workspace and AgentConversation. Same-FeatureArea agent
serialization, runtime epoch fencing, recovery, Execution and Acceptance retain
their existing semantics.

### Review depth is an independent semantic dimension

`ReviewDepth` is exactly `FAST | DEEP`, stored explicitly and immutably on every
new Review. `ReviewPlan.mode` remains `FOCUSED | REPOSITORY`; `reasoningEffort`
uses the selected model’s supported enum (`low | medium | high | xhigh | max`,
plus `ultra` where supported); `model` is the configured coding-agent model.
An omitted reasoning override uses that model’s configured default. Semantic
depth and runtime settings remain independent. FAST + xhigh, FAST +
REPOSITORY, DEEP + medium and DEEP + FOCUSED are valid combinations. Ordinal,
prompt text, plan scope and reasoning effort never imply depth.

FAST / DEEP describes semantic review depth only.

It does not alter:

- Review ownership
- Review-to-Run cardinality
- authorization
- ReviewPlan scope mode
- model
- reasoning effort
- Coding Agent prompt

A FAST Review may satisfy the Review gate directly.

A FAST Review may instead return structured FURTHER_REVIEW_REQUIRED, causing the
Task to require a new DEEP Review. Bridge never auto-starts that Review.
A DEEP Review may also require another DEEP Review; each attempt creates a new
Review with its own single Run. This successful evidence outcome is independent
of infrastructure failure.

`tasks.required_review_depth` is the durable minimum while Review work is
required or in flight. New Tasks require FAST; explicit DEEP admission raises
the minimum to DEEP, including subsequent infrastructure retries. A successful
`APPROVAL_READY` result clears the minimum and opens Approval; a successful
`FURTHER_REVIEW_REQUIRED` result sets it to DEEP and exposes `START_TASK_REVIEW`
without creating a Decision. Approval REVISE begins a new cycle requiring FAST;
APPROVE, REJECT and terminal Task transitions clear the minimum.

Schema v45 adds `reviews.review_depth`, `reviews.review_disposition` and
`tasks.required_review_depth`. v44 historical Reviews become DEEP. Existing
Decision evidence alone backfills historical APPROVAL_READY; historical prompt,
result and receipt bytes/hashes remain intact. Safe quiescent Task phases and
pointers are preserved. Ambiguous active/in-flight v44 Review state rejects the
migration atomically with explicit development database rebuild guidance; the
migration creates no Review, Run or Approval and advances no Task phase.

`task_review_start` requires structured `reviewDepth`; shallow admission returns
`REVIEW_DEPTH_TOO_SHALLOW` without Review/Run/claim creation. Shared product facts
include `requiredReviewDepth`, `allowedReviewDepths`, each Review's `reviewDepth`
and its available `reviewDisposition`. WebUI, Card, Console and JSON consume
these facts; Task remains the semantic title and selected product entity.

Prompt Neutrality remains byte-for-byte: authored `reviewInstruction` equals
persisted `final_codex_prompt` and the actual sent prompt, with equal SHA-256.
Only the machine structured-output schema requires `reviewDisposition`; Bridge
adds no natural-language depth or disposition instructions. See the complete
[transition table and approval fences](review-approval-workflow.md).

Retire and Task commit/FeatureArea submission are unchanged. Production recall
host integration remains `HOST_INTEGRATION_PENDING`.

## Historical branch-selection baseline (schema v33)

This section records the original DevelopmentCycle branch-selection baseline.
Branch creation is now implemented as a separate authoritative action; selection
and Git checkout remain separate actions. The remaining lifecycle sections below
are target design, not claims that those capabilities have shipped.

```text
Project
├── Repository*
├── DevelopmentCycle*  ← current developmentRef authority per Repository
└── FeatureArea*
```

Each cycle records its Project, Repository, canonical `refs/heads/*` developmentRef,
selection-time `selectedHeadSha`, creation timestamp, request identity and optional
AI session provenance. A SQLite partial unique index guarantees at most one current
cycle per Project/Repository. Explicit reselection atomically supersedes the previous
cycle with `BRANCH_RESELECTED` and inserts the new cycle. Historical selection facts
and request receipts cannot be rewritten or deleted. No release meaning is inferred.

`workspace_get(include=["BRANCHES"])` and `development_branch_select` share an application
service and authoritative schemas across GPT Adapter and Local MCP. Console uses
that same service. `requestId` is a durable, globally unique command identity;
Project/Repository/full-ref form the payload. A fresh session can replay the command,
returning its original receipt even after reselection or ref deletion. A changed
payload conflicts. Session identity records provenance, not branch authority.

`project_open` binds the initialized Session without selecting a FeatureArea or
reading branch inventory. The subsequent Project-only `workspace_get` remains
unfocused. Its development context, including BRANCHES reads and Console, reports each
Repository's current cycle, selected head, current head, ref existence, head movement,
and factual checkout relation. An unselected Repository reports
`developmentBranchSelectionRequired = true`; Upper AI must obtain an explicit branch
choice before normal development flow. This is startup guidance, not a new admission
or lifecycle binding on existing engineering operations.

Only real direct commit-valued local branch refs are eligible, in deterministic
full-ref order. Tags, remote-only refs, symbolic branch aliases, detached HEAD and
arbitrary SHAs are not choices. There is no default-branch inference or single-branch
auto-selection. New and migrated Projects start without any selected cycle.

A moved ref sets `refChanged = true` while preserving the selected baseline. A
missing ref reports `MISSING` / `DEVELOPMENT_REF_MISSING` with no current head and no
fallback. Reads never reset, retarget or create cycles. Checkout may differ from the
selected branch, including a detached checkout.

Development branch selection is workflow metadata only. It is not Git checkout,
execution workspace selection, integration target mutation, or release authorization.
Development branch creation is implemented separately and does not select or
checkout the new ref. Git checkout, merge, push and repository-file writes remain
outside branch selection and creation. The explicit Console confirmation states
that Git checkout will not run.

Deferred: FeatureAreaCycleBinding, Execution/Git lifecycle redesign, Integration,
candidate refs and manifests, ReleaseRecord/release authorization, automatic next
cycles, asynchronous delivery and Orchestrated Review.

---

## 1. Product Positioning

Dev Bridge is a **local-first workflow tracker for AI-assisted software development**.

Its primary value is to preserve a durable and auditable engineering history across:

- project understanding;
- release-development cycles;
- stable product capabilities / feature areas;
- durable Tasks and repeated Executions;
- Coding Agent context continuation;
- Git branches, commits, worktrees, integrations, and releases;
- exact delegated Prompts;
- delegated model/reasoning configuration;
- Verification evidence and Results.

Dev Bridge is not:

- a Coding Agent;
- an LLM provider/router;
- an NLP or semantic-similarity engine;
- a Jira/Linear-style project-management system;
- an Agent-specific todo/plan observability platform.

The v0.1 product should remain generic across AI clients and Coding Agents.

---

## 2. Responsibility Boundaries

### 2.1 Upper AI / AI Client

The upper AI owns semantic decisions:

- understand user intent;
- decide which product capability / Feature Area work belongs to;
- decide whether to create, reuse, split, or retire a Feature Area;
- decide whether intended work belongs to the same Task or a new Task;
- author engineering Prompts;
- select model and reasoning;
- interpret natural-language user decisions;
- interpret Project Discovery results and Feature Area candidates.

### 2.2 Dev Bridge

Bridge owns deterministic product and workflow facts:

- durable workflow state;
- Project / Feature Area / Task / Execution history;
- Development Cycle participation;
- authorization boundaries;
- exact Prompt persistence;
- repository/Git provenance;
- worktree lifecycle;
- deterministic Verification evidence;
- recoverable Git operations;
- historical audit projections.

Bridge does **not**:

- infer Feature Area similarity from text;
- decide whether a Review is semantically stale;
- infer Task identity through NLP;
- infer product capabilities directly from code and silently create them;
- rewrite engineering Prompts;
- summarize Coding Agent context as a substitute for real continuation;
- decide how the Coding Agent should reason about the repository.

### 2.3 Coding Agent

The Coding Agent owns engineering intelligence:

- repository exploration;
- implementation planning;
- code modification;
- testing;
- engineering reasoning.

Core principle:

```text
Upper AI owns semantics.
Coding Agent owns engineering intelligence.
Bridge owns durable workflow, authorization, provenance, and deterministic product state.
```

---

## 3. Canonical Vocabulary

The v0.1 target vocabulary is:

```text
Project Discovery
Project Context
Development Cycle
Feature Area
Feature Area Cycle Binding
Feature Area Review
Task
Execution
Delegation
Verification
Result
```

### 3.1 `Topic` Is Replaced by `FeatureArea`

`Topic` is not the canonical product term for v0.1.

Reason:

`Topic` strongly suggests a conversation subject or temporary engineering discussion. The intended product object is longer-lived and more stable.

Canonical definition:

> **Feature Area is a durable product capability or functional area within a Project. It groups Reviews, Tasks, and Executions that change, fix, extend, or analyze that capability across Development Cycles.**

Examples:

```text
Review
Local MCP Adapter
Desktop Runtime
Execution & Context
Development Cycle
Chat
Sync
Resource Access
Schedule
```

A Feature Area is not:

- a chat/session container;
- a Development Cycle;
- one Task;
- one code Module;
- one short-lived implementation workstream.

### 3.2 AI-facing Keys

The target AI/product vocabulary should use Feature Area terminology consistently.

Conceptual naming:

```text
FeatureArea
featureAreaId
FeatureAreaCycleBinding
FeatureAreaRelation

feature_area_create
feature_area_list
feature_area_review_start
...
```

Do not maintain simultaneous AI-facing `Topic` and `FeatureArea` aliases after the migration unless a concrete compatibility requirement exists.

v0.1 has not shipped; avoid carrying obsolete semantic aliases by default.

---

## 4. Product Dimensions

The main product dimensions answer different questions:

| Object | Question |
|---|---|
| Project | Which software project? |
| Project Context | What does Bridge currently know about this project's engineering structure? |
| Development Cycle | Which version/release-development period? |
| Feature Area | Which long-lived product capability? |
| Task | What concrete change is intended? |
| Execution | What actual attempt was made? |
| Module | Where is the code organized? |

Canonical distinction:

```text
Development Cycle
→ version / release dimension

Feature Area
→ product capability dimension

Task
→ engineering-change intent dimension

Execution
→ attempt / evidence dimension

Module
→ code-structure dimension
```

A Feature Area may span many Modules.

A Module may support many Feature Areas.

Module ownership must not substitute for Feature Area semantics.

---

## 5. Navigation View vs Ownership Model

The user-facing navigation may naturally be:

```text
Project
→ Development Cycle
→ Feature Area
→ Task
→ Execution
```

This is a **participation/query view**, not an ownership tree.

Canonical ownership:

```text
Project
├── ProjectContext
├── DevelopmentCycle*
│   └── ReleaseRecord*
│
└── FeatureArea*
    ├── FeatureAreaCycleBinding* ──→ DevelopmentCycle
    ├── Review*
    └── Task*
        ├── TaskDefinition snapshot/version
        └── Execution*
```

Relationship analogy:

```text
Role ← UserRoleBinding → User

DevelopmentCycle ← FeatureAreaCycleBinding → FeatureArea
```

A Development Cycle does not own a Feature Area's lifecycle.

A Feature Area is Project-owned and may participate in multiple Development Cycles over time.

---

# Part I — Project Understanding

## 6. Project Discovery

The first project-wide repository analysis is **Project Discovery**, not Review.

Canonical distinction:

```text
Project Discovery
= shallow project-wide engineering understanding

Feature Area Review
= focused engineering analysis of one product capability
```

Do not model:

```text
Project Review
```

as another Review kind.

Review belongs to a semantic Task within its FeatureArea.

### 6.1 Initial Project Flow

Normal first-open flow:

```text
Project bind/open
        ↓
Project Context unavailable
        ↓
Project Discovery
        ↓
shallow Coding Agent repository analysis
        ↓
Project Context READY
        ↓
upper AI understands:
- repositories
- Modules
- entrypoints
- major relationships
- product-capability candidates
        ↓
normal product workflow
```

### 6.2 Discovery Scope

Initial Project Discovery should remain shallow.

Its purpose is to establish reusable context such as:

```text
Repository topology
Module map
Module responsibilities
Roots / entrypoints
Major internal relationships
Major external dependencies where useful
Product capability / Feature Area candidates
Obvious architectural boundaries
```

It should not automatically perform:

```text
deep analysis of every module
full-call-graph reconstruction
all-project bug audit
deep Feature Area Review
implementation planning for every capability
```

Deep engineering analysis belongs to Feature Area Review.

### 6.3 Feature Area Candidates

Project Discovery may report candidate product capabilities.

Example:

```text
candidate Feature Areas:
- Review
- Task Execution
- Local MCP Adapter
- Desktop Runtime
- Documents
- Authorization / Decisions
```

These are **candidates**, not authoritative Feature Area records.

Flow:

```text
Coding Agent
→ engineering facts + capability candidates

Upper AI
→ product-semantic interpretation

Bridge
→ persists explicitly selected Feature Areas
```

Bridge must not automatically create, delete, rename, merge, or split Feature Areas based solely on repository scans.

### 6.4 Project Discovery Cannot Create Engineering Work

Project Discovery may produce/refresh Project Context.

It must not directly create:

- Tasks;
- Executions;
- execution authorization;
- implementation changes.

Understanding the Project and changing the Project are separate workflows.

---

## 7. Project Context

Project Context is reusable engineering understanding.

It is not Review history.

Conceptual contents may include:

```text
repositories
Modules
roots
entrypoints
relationships
capability candidates
current shallow engineering map
```

Project Context does not own Feature Areas.

Feature Areas are durable product-history objects.

### 7.1 Refresh

Project Context may be refreshed as repositories evolve.

A useful user-visible lifecycle can remain compatible with:

```text
UNAVAILABLE
PREPARING
READY
READY · REFRESHING
NEEDS_ATTENTION
```

A usable snapshot may remain READY while refresh occurs.

Refresh must not automatically rewrite Feature Area history.

It may surface new candidates or changed engineering facts for upper-AI interpretation.

---

# Part II — Development Cycles and Feature Areas

## 8. Development Cycle

A Development Cycle represents a release-development period.

Examples:

```text
v0.1.0
v0.2.0
v1.0.0
```

A Development Cycle is a first-class product object because it has:

- durable identity;
- repository binding;
- a development line;
- Feature Area participation;
- release preflight;
- release authorization;
- release provenance;
- terminal history.

### 8.1 Creation

Choosing a cycle/version is a semantic decision made by the user / upper AI.

Bridge must not infer:

- the next semantic version;
- release readiness;
- a new cycle from the date;
- a new cycle because all work appears complete.

Once the cycle is explicitly selected, creating its Git development branch is deterministic Bridge infrastructure.

Example:

```text
integration branch @ A
        │
        └── dev/v0.1.0 @ A
```

The branch creation is visible but does not require another confirmation when it is the deterministic, non-destructive consequence of already-authorized Development Cycle creation.

### 8.2 Repository Binding

Do not hardcode `main` or `master`.

Conceptually:

```text
DevelopmentCycleRepositoryBinding
- developmentCycleId
- repositoryId
- integrationBranch
- developmentBranch
- baseCommit
- createdAt
- releasedCommit?
```

Branch names aid navigation.

Commit SHAs preserve historical truth.

The model should evolve cleanly to multiple repositories even if v0.1 initially supports one repository binding per Development Cycle.

### 8.3 v0.1 Concurrency

Prefer:

```text
one ACTIVE Development Cycle
per Project/repository development line
```

Do not implement concurrent release trains in v0.1 unless an already-validated product requirement demands them.

---

## 9. Feature Area

A Feature Area is a **long-lived product capability or functional area**.

It exists independently of one Development Cycle.

Example:

```text
Feature Area: Local MCP Adapter

v0.1.0
- Generalize adapter
- Enforce loopback-only
- Add Desktop supervision

v0.3.0
- Add another capability

v0.4.1
- Fix runtime defect
```

The Feature Area remains the same durable object across these cycles.

### 9.1 Long-lived Identity

Feature Area identity does not change because:

- Development Cycle changes;
- branch changes;
- repository baseline changes;
- Task count changes;
- model changes;
- Agent context changes.

A Feature Area is not normally "completed" when one version of its functionality ships.

### 9.2 Feature Area vs Task

```text
Feature Area
= what product capability is this about?

Task
= what are we changing about that capability now?
```

Example:

```text
Feature Area: Local MCP Adapter

Tasks:
- Generalize LibreChat-specific adapter
- Enforce loopback-only access
- Supervise Local MCP in Desktop
- Fix runtime log presentation
```

Avoid creating a new Feature Area for every individual change.

### 9.3 Feature Area vs Module

```text
Feature Area
= product semantics

Module
= code organization
```

Example:

```text
Feature Area: Review

Relevant Modules:
- review
- mcpserver
- consoleapi
- storage
- git
```

A large Module can serve several Feature Areas.

A Feature Area can legitimately span several Modules.

---

## 10. Feature Area Lifecycle

The schema-37 lifecycle has five states:

```text
DISCOVERING ── ACTIVATE ──> ACTIVE
     │                       │
     ├── RETIRE ─────────────┴──> RETIRING ── convergence ──> RETIRED
     └── atomic DECOMPOSE ──> DECOMPOSED
```

### 10.1 DISCOVERING

### 10.2 ACTIVE

### 10.3 RETIRED and DECOMPOSED

### 10.4 Atomic lifecycle actions

ACTIVATE requires DISCOVERING and no active claim. RETIRE accepts DISCOVERING or
ACTIVE even with active/UNKNOWN claims and pending Decisions. It verifies Project
ownership, persists the immutable intent and RETIRING fence, then commits before
child cancellation. Activation preserves the canonical conversation; retirement
ends it at final completion (or earlier under canonical orphan quarantine).

DECOMPOSE receives an explicit ordered list of child titles/problem statements
and a stable requestId. It requires an idle DISCOVERING parent with zero explicit
product Tasks. In one transaction it creates every independent DISCOVERING child,
records every SPLIT_INTO provenance relation, marks the parent DECOMPOSED and ends
its current conversation. Any failed write rolls the entire operation back.
Children inherit no Task, Review, AgentConversation or backend Thread.

Lifecycle receipts bind Project, FeatureArea, action, requestId and a SHA-256 of
the canonical payload. Identical retries return the original result, even after
the parent becomes terminal; a changed payload under the same key conflicts.
RETIRE replay instead returns the current RETIRING/RETIRED result for its durable
intent, including when a response-loss retry uses another request ID.
Task insertion and Agent Run/claim reservation validate lifecycle at their actual
write boundary, so retirement and operation starts have one valid serial order.
Different FeatureAreas retain independent claims; there is no Project scheduler.

### 10.5 Legacy split history

There is no active persisted intermediate split workflow. Schema v28 retains
old SPLITTING rows as DISCOVERING with legacy_read_only=1 and
legacy_status='SPLITTING'. Old storage recorded no reliable completion marker;
the migration therefore does not infer completion or activate those rows.
Unclaimed current conversations are ended. Existing claimed runtime history is
preserved for exact recovery, which ends the old context and releases its claim
without creating a usable successor. Existing relations remain factual history.

An ACTIVE capability with historical Tasks cannot be decomposed. New sibling
FeatureAreas may be created explicitly for future work; old Task ownership is
preserved.

---

## 11. FeatureAreaCycleBinding

Participation in a Development Cycle is represented by a durable binding.

Conceptually:

```text
FeatureAreaCycleBinding
- id
- featureAreaId
- developmentCycleId
- repositoryId

- enteredAt
- exitedAt?

- entryCommit
- exitCommit?

- featureWorkRef?
- latestIntegratedCycleCommit?

- disposition
```

A Feature Area crossing cycles does not move ownership.

Example:

```text
Feature Area A

Binding #1
→ v0.1.0
→ CARRIED_OVER

Binding #2
→ v0.2.0
→ ACTIVE
```

Old bindings are never overwritten by the current binding.

### 11.1 Cycle Participation vs Feature Area Lifecycle

A Feature Area may remain:

```text
ACTIVE
```

for years.

Its participation in one cycle may end.

These are different dimensions.

Possible binding disposition concepts include:

```text
ACTIVE
CONCLUDED_FOR_CYCLE
CARRIED_OVER
DECOMPOSED
CANCELED
```

Exact persisted enum names may be refined during implementation.

Do not use `FeatureArea.ACTIVE` as a proxy for "this cycle still has unfinished work."

### 11.2 Split Participation

If a DISCOVERING Feature Area is decomposed while participating in a cycle:

```text
parent binding
→ DECOMPOSED
```

New split Feature Areas may explicitly receive bindings to the same active Development Cycle.

They do not inherit ownership from the parent.

---

## 12. Separate Feature Area State, Cycle Disposition, and Artifact Inclusion

These facts must remain separate.

### 12.1 Feature Area Lifecycle

Answers:

> Does this long-lived product capability currently exist?

Examples:

```text
DISCOVERING
ACTIVE
RETIRING
DECOMPOSED
RETIRED
```

### 12.2 FeatureAreaCycleBinding Disposition

Answers:

> How did this capability's participation in this Development Cycle end?

Examples:

```text
ACTIVE
CONCLUDED_FOR_CYCLE
CARRIED_OVER
DECOMPOSED
CANCELED
```

### 12.3 Artifact Inclusion

Answers:

> Which exact engineering results actually entered the Development Cycle / Release candidate?

A Feature Area can have unfinished work while older results are already included.

Example:

```text
Feature Area A
lifecycle: ACTIVE

Task B / Execution #1
result commit B
→ already integrated into dev/v0.1.0

Task B / Execution #2
rework result C
→ not integrated

Release candidate
→ contains B
→ does not contain C
```

Therefore:

```text
Feature Area has unfinished work
≠
Feature Area code is not included
```

`CARRIED_OVER` must not be used as proof that nothing from that Feature Area reached the release.

---

# Part III — Review, Task, Execution

## 13. Feature Area Review

Review is **read-only engineering analysis owned by a semantic Task** against a specific repository state. Its Task belongs to the FeatureArea; current depth and gate rules are defined above.

There is no Project Review product concept.

A Task may have multiple Reviews:

```text
FeatureArea
└── Task
    ├── Review R1 @ A · FAST → Run [1:1]
    ├── Review R2 @ B · DEEP → Run [1:1]
    └── Review R3 @ C · DEEP → Run [1:1]
```

Historical Reviews remain immutable.

Later Reviews may supplement, contradict, or replace earlier semantic conclusions.

### 13.1 Review Provenance

Review stores immutable repository provenance.

Factual drift may include:

```text
reviewCommit
currentCommit
repositoryChanged
changedFileCount
focusPathsChanged
```

`focusPathsChanged` means:

> paths explicitly recorded in the Review focus that changed.

It does not claim those were all files the Agent inspected.

### 13.2 Review Baseline

The Review baseline must correspond to the authoritative current Feature Area / Development Cycle Git baseline at Review start.

Do not derive historical Review truth from an arbitrary process checkout HEAD.

If dirty/untracked files are visible to the Review Agent, the system must either freeze/record those inputs or reject the ambiguity. A recorded commit must not falsely imply commit-only analysis when additional mutable state was actually read.

---

## 14. Task

Task is **durable engineering intent**.

It is not one Coding Agent execution.

Example:

```text
Feature Area: Local MCP Adapter

Task B
├── Execution #1 @ v0.1.0
├── Execution #2 @ v0.1.0
└── Execution #3 @ v0.2.0
```

If the intended engineering outcome remains the same:

```text
same Task
→ new Execution
```

If the intended outcome changes:

```text
upper AI
→ new Task intent → Review → Approval of that existing Task
```

Bridge does not infer this by NLP/similarity.

### 14.1 Cycle Context

Task identity remains Feature Area-owned.

An Execution binds the Task to the concrete FeatureAreaCycleBinding in which that attempt occurs.

Do not move or clone Task ownership merely because the Development Cycle changes.

---

## 15. TaskDefinitionRevision

A Task is explicit durable engineering intent under an ACTIVE FeatureArea.
Creation commits intent before Review, with no initial definition revision.
APPROVE of the current Review gate binds its exact receipt and creates the
initial immutable TaskDefinitionRevision on the existing Task. REQUEST_CHANGES
retains the same Task and creates a new immutable revision and successor
Execution within the frozen goal, constraints and scope boundary.
REQUEST_REVERIFICATION retains the same Task and revision. Earlier revisions
are never rewritten; a different engineering outcome requires explicit new
Task intent before its Review.

The canonical v1 payload uses deterministic JSON field order and SHA-256. It
contains Project, FeatureArea, repository, module and Codex configuration,
model/reasoning selection, problem statement, base commit, WorkSpec,
VerificationSpec and ordered Goals, plus ReviewApprovalID for new approved Tasks.
Runtime state, timestamps, WorkKind and
Development Cycle/Git/Release concepts are excluded from its semantic hash.
The v46 next-run model, reasoning effort and Codex Fast Mode selection is an
immutable runtime envelope on Approval/Execution, separate from this definition.

---

## 16. Task Goals

Existing semantics remain:

- optional;
- upper-AI authored;
- ordered;
- durable;
- not generated by Bridge;
- not authorization boundaries;
- not Runs;
- not worktrees;
- no DAG;
- no per-Goal model/reasoning.

Statuses:

```text
PENDING
RUNNING
COMPLETED
FAILED
SKIPPED
```

Goal progress belongs to each Execution.

An Execution freezes the Goal definition snapshot it reports against.

Late Goal events must route by Execution identity and must not mutate another Execution.

---

## 17. Engineering analysis inside Execution

Coding Agents perform their analysis inside the authorized Execution. Material scope
conflict stops the Execution without changing its immutable Task definition.

## 18. Execution

Execution is one authorized attempt to realize a Task against one concrete repository baseline.

Execution is not:

- a network reconnect;
- a UI refresh;
- a retry of a read request;
- one Goal;
- one Coding Agent internal todo.

A completed Execution is immutable historical evidence.

### 18.1 Historical Binding

At creation, freeze:

```text
- TaskDefinition snapshot/version
- FeatureAreaCycleBinding
- repository identity
- base ref/role
- base commit
- Agent context mode/source
- execution authorization
- Goals definition snapshot
```

Historical reads must not substitute mutable current Project / Feature Area / Task / Cycle state.

---

## 19. Canonical Workspace per FeatureArea

Every repository-backed FeatureArea owns one canonical Bridge-managed Workspace.
The first Review or Development operation creates it lazily from the selected
Project Development Branch's exact commit. SQLite persists exact Project,
Repository, FeatureArea and origin baseline. The FeatureArea stores
`currentWorkspaceId`. Task, Execution, Run and Review only associate with it.

Later Tasks reuse that Workspace ID, worktree path, working branch, commits and
uncommitted changes. Review remains read-only; authorization belongs to the exact
Execution/Run and immutable TaskDefinitionRevision. Source checkout state is
independent of both the integration branch and the FeatureArea working branch.

## 20. Continuous Task results

A terminal Task leaves the FeatureArea ACTIVE and idle. The next Task reads the
existing engineering state without copying a diff to another directory. Failed
work also remains available after canonical quiescence. An unknown operation
blocks admission until existing recovery resolves or permanently revokes its
authority and quarantines the exact old workspace/context. Quarantine retains
historical evidence and detaches the current pointer; subsequent canonical work
can create an independent safe workspace.

Agent Context Reset rotates only the conversation. It does not reset or discard
the Workspace. A commit can be an ordinary later maintenance/development Task;
merge, push and release need separate explicit authorization.

---

## 21. Delegation

Every actual engineering delegation from Bridge to a Coding Agent creates immutable historical delegation provenance.

Delegation is an internal audit object, not a new primary user workflow node.

Conceptually:

```text
Delegation
- id

- ownerKind
  REVIEW
  EXECUTION

- ownerId

- exactPrompt
- delegatedModel
- delegatedReasoning

- backend session/thread/turn correlation where available
- adapter/agent identity where useful

- startedAt
- completedAt?
- usage?
```

### 21.1 Prompt Invariant

```text
Upper-AI-authored Prompt bytes
=
persisted Prompt bytes
=
View/Copy bytes
=
bytes delegated to Coding Agent
```

Bridge does not prepend semantic role prose.

Model/reasoning are metadata.

### 21.2 Model / Reasoning Historical Truth

Historical UI reads Delegation provenance.

Never read current Task model/reasoning and present it as historical Execution truth.

If Bridge only proves what it sent:

```text
delegatedModel
delegatedReasoning
```

are requested/delegated facts, not necessarily authoritative backend-actual facts.

Unknown historical values remain unknown / `—`.

Do not fabricate backfills.

---

## 22. FeatureArea AgentConversation

FeatureArea owns Coding Agent engineering context. Task, Execution, and Workspace
are not conversation owners. Each usable FeatureArea has one canonical current
AgentConversation when context is first allocated, plus its retained historical
conversations. Project Discovery uses separate Project-level context and never
acquires a FeatureArea Agent claim or attaches to its conversation.

### 22.1 Durable serialization and ownership

AgentConversation is authoritative for the backend Thread. The same FeatureArea
owns the canonical Workspace/cwd across Tasks and Executions. These are separate
lifecycles: conversation rotation preserves the Workspace; workspace quarantine
is an explicit recovery fact. Same-FeatureArea agent work remains serialized.

### 22.2 Explicit reset and linear rotation

Reset Agent Context is an explicit upper-layer FeatureArea action. It ends the
current conversation with `EXPLICIT_RESET`, preserves it as history, and creates
a new canonical conversation with no backend Thread. The next Agent operation
starts a new Thread. Reset request IDs make duplicate requests idempotent.

Technical replacement uses the same linear rotation mechanism with
`BACKEND_THREAD_UNAVAILABLE`, `HOST_TOOL_CONTRACT_UPGRADE`, or
`UNSAFE_CONTEXT_BOUNDARY`. Ambiguous I/O failure is not proof that a backend
Thread is unavailable. Review changes, repository drift, turn counts, token
counts, and Codex compaction never automatically reset context. Bridge defines
no context token or turn limit; Codex owns compaction.

### 22.3 Exact operation boundary provenance

Each internal Run persists its actual AgentConversation identity and entry
boundary. The entry boundary is the last safely completed canonical context
point before that operation. The first operation may have no entry Turn.

Before sending a Turn request Bridge durably records `UNKNOWN`. A known completed
Turn can establish a `KNOWN_ADVANCE` exit boundary and update the conversation's
safe current boundary. `NO_ADVANCE` means no Turn was dispatched. Unknown outcomes
have no invented exit ID and never optimistically move the safe boundary.

UNKNOWN retains the operation claim until exact known finalization or permanent
authority revocation, workspace/context quarantine and UNKNOWN_ORPHANED disposition.
Local transport Close never proves physical backend death. Canonical owner cancellation
can orphan current owned work; `agent_operation_recover` explicitly abandons the exact
older fenced operation using the same authority-revocation transaction; ordinary context reset remains
idle-only. Startup distinguishes NO_ADVANCE, KNOWN_SAFE, UNKNOWN and CONFLICT without
timeout-based unlocking. Normal terminal Run/boundary/claim facts commit through
the atomic finalizer and its durable external-observation intent. Historical
operation references never change when a later conversation rotates.

The [runtime safety contract](agent-runtime-safety.md) specifies DataRoot single
writer ownership, immutable runtime epochs, callback fencing, recovery authority,
workspace quarantine, and the crash acceptance matrix.

### 22.4 Agent Host Tool Contract v1

Every new FeatureArea backend Thread declares exactly this stable ordered union:

- `repository_search`
- `repository_read`
- `repository_list`
- `report_goal_progress`

### 22.5 FeatureArea lifecycle

Decomposed or retired FeatureAreas end their current conversation into history.
Lifecycle mutation cannot race active Agent work. Split children start independent
conversations without copying or forking the parent's backend context.

---

## 23. Execution, Verification, Integration, and Completion Are Separate

Do not collapse:

```text
Execution success
Verification success
Integration success
Task completion
Feature Area lifecycle
Development Cycle release
```

Example:

```text
Execution
SUCCEEDED

Verification
PASSED @ commit B

Integration
CONFLICT

Task
ACTIVE

Feature Area
ACTIVE
```

This is valid.

### 23.1 Verification

Verification binds to an exact immutable result commit/tree.

A test result against tree X does not automatically validate a later candidate Y.

### 23.2 Task Completion

Task completion means the intended engineering change was semantically accepted as complete.

It does not retire the Feature Area.

### 23.3 Feature Area Lifecycle

Feature Area ACTIVE means the product capability remains valid.

It is not a proxy for whether current-cycle work is complete.

---

# Part IV — Git Lifecycle

## 24. Git Working Model

Normal shape:

```text
Project Development Branch @ exact integration baseline
└── FeatureArea canonical Workspace / working branch
    ├── Review Task (read-only ceiling)
    ├── Development Task A / Execution / Runs
    └── Development Task B / Execution / Runs
```

The first repository-backed Review also creates this workspace. Subsequent
work uses it without merging, replacing or cleaning it merely because a Task
ended. The origin baseline remains immutable as the working branch evolves.

## 25. Execution Git lifecycle

An Execution binds the FeatureArea's existing workspace and records each Run's
start/end Git snapshots. Terminal finalization preserves the workspace and
conversation, releases the exact claim after quiescence, and leaves the area
ACTIVE / idle. New work requires its own definition and authorization. Commits
and prior uncommitted work are ordinary FeatureArea evolution; external
Development Branch advancement is an integration concern.

---

## 26. Feature Area Work → Development Cycle Integration

Feature Area is long-lived and therefore is **not completed** to trigger cycle integration.

The integration unit is work performed within a `FeatureAreaCycleBinding`.

When an authorized Task/result is accepted and the relevant Feature Area work is ready to enter the Development Cycle:

```text
Feature Area work line
→ Development Cycle branch
```

This may occur multiple times across the lifetime of the Feature Area and across different cycles.

Do not RETIRE or DECOMPOSE the Feature Area because current-cycle work was integrated.

Before each advancement:

- verify expected source/target commits;
- verify no conflicting active write state;
- verify required evidence applies to the exact candidate;
- preserve artifact-inclusion provenance.

If integration changes the exact verified tree, produce/require evidence applicable to the actual candidate.

On conflict:

- do not auto-resolve semantic code conflicts;
- preserve recoverable state;
- do not mark integration complete.

---

## 27. GitOperation: Recoverable Git ↔ Durable-State Convergence

Git and SQLite do not share one atomic transaction.

Important Git mutations require durable recoverable operation identity.

Conceptually:

```text
GitOperation
- id
- kind

- repositoryId

- sourceRef?
- sourceExpectedCommit?

- targetRef?
- targetExpectedCommit?

- candidateCommit?
- resultCommit?

- authorizationId?
- phase
- createdAt
- completedAt?
- failure?
```

Possible kinds:

```text
EXECUTION_INTEGRATE
FEATURE_AREA_INTEGRATE
FEATURE_AREA_RECONCILE
RELEASE
```

GitOperation is infrastructure/audit state, not a user engineering object.

### 27.1 Recovery

If Git succeeds and Bridge crashes before durable workflow state converges:

```text
restart
→ recover same GitOperation
→ compare actual Git facts
→ converge product state
```

Do not blindly replay the logical operation.

Repeated requests representing the same already-started side effect should converge on the same logical operation.

---

## 28. Git Historical Evidence Retention

Saving SHA text in SQLite does not guarantee the Git object remains reachable.

Workspace cleanup and evidence retention are independent lifecycles.

If Bridge promises a historical object remains inspectable, retain the necessary Git objects through Bridge-managed refs or another explicit archival mechanism.

Conceptually:

```text
refs/devbridge/reviews/<id>
refs/devbridge/executions/<id>/result
refs/devbridge/releases/<id>
```

Exact ref names are implementation details.

Canonical invariant:

```text
temporary workspace may be deleted
temporary branch may be deleted
promised historical evidence must remain reachable
```

---

## 29. Failure and Cleanup

### Valuable Failed Work

If failed/unintegrated work may be useful:

- checkpoint/preserve it explicitly;
- retain workspace when necessary;
- expose cleanup-needed state;
- do not destroy it.

### Disposable Failed State

If Bridge owns the workspace and no valuable work remains:

- remove exact Bridge-owned worktree;
- delete disposable refs when safe.

### Cleanup Is Separate

If integration succeeds but cleanup fails:

```text
integration = success
cleanup = pending/failed
```

Do not rewrite engineering history as failed.

### No Global Prune

Do not use global `git worktree prune` as normal task cleanup.

Remove exact known Bridge-owned worktrees.

---

# Part V — Cross-cycle Feature Area History

## 30. Carry-over

A Feature Area itself normally remains ACTIVE across versions.

What carries over is **unfinished work / participation in one Development Cycle**.

Example:

```text
Feature Area: Local MCP Adapter
lifecycle = ACTIVE

Binding v0.1.0
→ disposition = CARRIED_OVER

later:
Binding v0.2.0
→ ACTIVE
```

Release does not:

- retire Feature Area;
- complete unfinished Task automatically;
- delete Feature Area;
- clone Feature Area;
- assign it automatically to a guessed next Cycle.

Artifact inclusion remains separate.

---

## 31. Resume in a Later Development Cycle

A carried Feature Area participation is explicitly resumed/bound into a later cycle.

Do not automatically move all carry-over work when a new Development Cycle is created.

Before executable work:

```text
Feature Area resume/bind preflight
→ repository reconciliation
→ establish FeatureAreaCycleBinding
→ establish authoritative current Feature Area baseline
```

Reconciliation targets the **current Development Cycle branch**, not hardcoded main/master.

Reason:

```text
dev/v0.2.0
```

may already contain other accepted work from that cycle.

### 31.1 Strategy

Prefer non-history-rewriting merge/integration semantics for audited persistent work.

Do not automatically rebase already-recorded historical results.

Canonical invariant:

> recorded historical objects are not rewritten; new integration objects are recorded separately.

### 31.2 Conflict

If reconciliation conflicts:

- do not mark binding/resume successful;
- do not start a new Execution;
- expose factual conflict state;
- preserve recoverable Git state;
- do not auto-resolve engineering semantics.

---

# Part VI — Release

## 32. Development Cycle Release

Release is an explicit user-authorized product action.

Bridge does not release because:

- all Tasks appear complete;
- all Feature Area work appears integrated;
- time passed;
- branch looks mergeable.

---

## 33. Release Candidate Authorization

User authorizes an **exact candidate**, not an abstract future branch state.

Conceptual flow:

```text
source development branch @ S
target integration branch @ T
        ↓
compute candidate R
        ↓
deterministic preflight
        ↓
Verification applicable to R
        ↓
collect Feature Area work / artifact inclusion facts
        ↓
create durable Release Decision for R
        ↓
user authorizes exact candidate/disposition
        ↓
short critical section
        ↓
revalidate S / T / product facts
        ↓
advance expected target T → R
        ↓
record ReleaseRecord + GitOperation completion
```

If material facts change while waiting:

- source commit;
- target commit;
- unfinished work;
- artifact inclusion;
- required Verification;
- authorization scope;

old authorization cannot be silently reused.

Create a new candidate / Decision.

---

## 34. Release with Unfinished Work

Release preflight must distinguish:

```text
Feature Area lifecycle
FeatureAreaCycleBinding disposition
unfinished Tasks/Executions
artifact inclusion
```

Do not merely list "unfinished Feature Areas" as though nothing from them is included.

Example:

```text
Feature Area: Local MCP Adapter
status: ACTIVE

Already included:
- Task A result B

Outstanding:
- Task B / Execution #2 result not integrated
```

If unfinished work exists, show a strong explicit Decision:

```text
CANCEL
RELEASE_ANYWAY
```

`RELEASE_ANYWAY` means:

> accept the explicitly displayed unfinished-work disposition for this exact candidate.

It does not authorize overriding:

- candidate changes;
- conflicts;
- unknown outcomes;
- failed required Verification;
- authorization mismatch;
- unknown inclusion facts.

No automatic next Development Cycle is created.

---

## 35. ReleaseRecord

Conceptually:

```text
ReleaseRecord
- id
- developmentCycleId
- repositoryId

- sourceBranch
- sourceCommit

- targetBranch
- targetCommitBefore

- candidateCommit
- releasedCommit

- featureAreaParticipationSnapshot
- unfinishedWorkSnapshot
- artifactInclusionSnapshot

- authorizationId
- verification references

- createdAt
- releasedAt
```

A local Git release means the Development Cycle reached the configured integration branch.

It does not imply:

- push;
- deployment;
- package publishing;
- App Store release;
- user distribution.

---

# Part VII — Authorization, Concurrency, Recovery

## 36. Authorization Model

Separate:

```text
Proposer
→ upper AI

Authorizer
→ user / trusted decision channel

Executor
→ Bridge
```

Upper AI may propose:

```text
"this Task is complete"
"this work should enter v0.1.0"
```

That is semantic judgment, not automatically high-impact user authorization.

### 36.1 Deterministic Consequences

Within an already-authorized scope, Bridge may perform deterministic consequences automatically.

Examples:

| Authorized semantic action | Bridge consequence |
|---|---|
| Create Development Cycle | create its development branch |
| Start authorized Execution | lookup or create the canonical FeatureArea workspace |
| Verified authorized Execution result | integrate into Feature Area line when preconditions remain valid |
| Authorized Task/work promotion | advance Feature Area work into current Development Cycle when exact preconditions remain valid |

### 36.2 High-impact Authorization

Development Cycle release requires explicit authorization of the exact candidate.

Discarding valuable work, expanding permissions, overwriting unknown state, or substituting a changed candidate requires new authorization.

### 36.3 Local MCP

`decision_respond` is a trusted-client transport for resolving an existing durable Decision.

Bridge does not NLP-parse the user's natural language.

A client model cannot create a non-existent release authorization merely because it can call a tool.

---

## 37. Concurrency Model

Keep v0.1 intentionally small:

```text
one ACTIVE Development Cycle
per Project/repository development line

one modifying Execution
per Feature Area at a time

different Feature Areas
may execute concurrently

one mutable Agent Thread
must not be concurrently used by multiple Executions
```

Repository safety must use real repository identity, not only Project ID.

Bridge application locks cannot prevent external user Git operations.

Important ref updates must validate expected old commits.

---

## 38. Idempotency and Unknown Outcomes

Side-effecting operations need stable durable operation identity.

Network retry/reconnect must not silently create another:

- Execution;
- integration;
- reconciliation;
- release.

If Bridge cannot determine whether an external side effect occurred:

```text
unknown / recovery required
```

is valid.

Do not convert uncertainty into:

```text
failed → safe to retry
```

without evidence.

Late events route using stable Execution / Delegation / backend correlation identity, not "latest Task Execution."

---

## 39. Legacy / Unknown Historical Data

Migrations are non-destructive.

Do not fabricate:

- Development Cycles;
- Feature Area bindings;
- model/reasoning provenance;
- branch provenance;
- artifact inclusion;
- exact historical definitions.

Old records missing new facts remain readable with explicit unknown/unavailable fields.

Unknown is a valid historical fact.

---

# Part VIII — Product Queries and v0.1 Scope

## 40. Product Query Model

### Development Cycle View

```text
v0.1.0

Feature Areas
├── Review
├── Local MCP Adapter
├── Desktop Runtime
└── Execution & Context
```

This is a participation query.

### Feature Area View

```text
Local MCP Adapter

Development history
├── v0.1.0
├── v0.3.0
└── v0.4.1

Reviews
Tasks
Executions
Relevant Modules
```

### Task View

```text
Task B
├── definition snapshots
└── Executions
```

### Execution View

```text
Execution #2
├── Development Cycle
├── FeatureAreaCycleBinding
├── base commit
├── workspace/result provenance
├── Delegation
│   ├── exact Prompt
│   ├── delegated model
│   └── delegated reasoning
├── Goal progress
├── Verification
└── Integration / Result
```

Navigation hierarchy must never be confused with ownership.

---

## 41. v0.1 Scope Boundaries

Explicitly defer:

- concurrent release trains;
- automatic next-version creation;
- automated backport/cherry-pick planning;
- Task DAG;
- parallel Goal scheduling;
- per-Goal worktrees/Runs/models;
- generic Feature Area relation graph beyond split provenance;
- automatic semantic similarity;
- Bridge-authored Agent summaries;
- arbitrary historical Thread forks when backend cannot support them;
- OpenCode todo integration;
- ZCode checklist integration;
- Coding-Agent-specific runtime observability;
- LAN/public MCP;
- automatic merge-conflict resolution;
- arbitrary branch-topology editor;
- deployment/distribution orchestration.

Do **not** defer:

- Project Discovery / Project Context distinction;
- Feature Area canonical vocabulary;
- FeatureAreaCycleBinding;
- Feature Area actionability/splitting gate;
- Task definition snapshots;
- Delegation provenance;
- Execution/Verification/Integration separation;
- recoverable GitOperation identity;
- historical Git evidence retention;
- exact Release candidate authorization.

These are required for truthful v0.1 history.

---

## 42. Canonical v0.1 Invariants

---

## 43. Normal v0.1 Flow

```text
Project selected
        ↓
Project Discovery
        ↓
Project Context READY
        ↓
Development Cycle selected/created
        ↓
Upper AI selects or creates Feature Area
        ↓
Feature Area DISCOVERING
        ↓
Feature Area Review if useful
        ↓
upper AI decides product boundary
        │
        │      ↓
        │   independent Feature Areas
        │      ↓
        │   parent DECOMPOSED
        │
        └── ACTIVE
               ↓
        FeatureAreaCycleBinding
               ↓
             Task
               ↓
               ↓
          user authorization
               ↓
            Execution
         canonical FeatureArea workspace
               ↓
          Delegation(s)
               ↓
       result artifact/commit
               ↓
          Verification
               ↓
   integrate into Feature Area line
               ↓
     accepted work enters Cycle
               ↓
      more Tasks may occur later
               ↓
        release candidate R
               ↓
      release Decision for R
               ↓
        user authorization
               ↓
 Development Cycle → integration branch
```

Cross-cycle path:

```text
Feature Area remains ACTIVE
        ↓
v0.1.0 release contains some accepted results
        ↓
unfinished work remains
        ↓
v0.1.0 binding = CARRIED_OVER
        ↓
later v0.2.0
        ↓
explicit resume/bind
        ↓
reconcile against dev/v0.2.0
        ↓
new FeatureAreaCycleBinding
        ↓
        ↓
same durable Task may create another Execution
```

---

## 44. Design Principle

The v0.1 implementation should preserve a simple user experience while retaining enough internal structure that Bridge never needs to invent history later.

Core distinction:

```text
simple UI
≠
lossy historical model
```

Users primarily navigate:

```text
Project
→ Development Cycle
→ Feature Area
→ Task
→ Execution
```

Internally Bridge retains:

- Project Context;
- FeatureAreaCycleBinding;
- definition snapshots;
- Delegations;
- Agent/code lineage;
- GitOperation records;
- retained Git evidence;
- release candidate authorizations;
- Verification evidence.

The durable record must be able to answer:

> What product capability was involved, what change was intended, against which repository state, with which exact Prompt/model/reasoning, what the Coding Agent actually attempted, what was verified, what entered which development line, what the user authorized, and what remains unfinished?

Bridge should never compensate for missing historical facts with semantic inference.

The [schema-36 unified work/cancellation contract](unified-agent-work-and-cancellation.md) governs Task Type/scope, Inspection admission, canonical owner cancellation and UNKNOWN_ORPHANED claim release. The [schema-37 retirement contract](feature-area-retirement.md) extends lifecycle stop orchestration while reusing that cancellation protocol.

## Repository unlink

Safe Repository unlink closes the current Project/Repository DevelopmentCycle in
the same SQLite transaction with end reason `REPOSITORY_UNLINKED`. The cycle,
selected HEAD, development ref and receipt remain historical. No branch, ref,
checkout or worktree is changed. A new selection requires an ACTIVE Repository,
so a confirmation prepared before the topology change cannot recreate the cycle.
See [the schema-v84 closure](safe-repository-unlink-closure.md).
