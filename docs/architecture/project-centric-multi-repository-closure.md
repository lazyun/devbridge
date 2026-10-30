# Project-centric repository sets: implementation boundary

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

Current user-decision increment: **PARTIAL_IMPLEMENTATION**, not
`CODEX_REQUEST_USER_DECISION_CLOSED`. Live request/answer/cancel and successor
fixtures pass; the pinned protocol cannot restore an old connection's pending
request after Core restart. See the [increment execution report](codex-request-user-decision-execution-report.md).
Earlier Coding v1 closure and the historical evidence below remain unchanged.

## Trusted Executor Model v1

Dev Bridge v1 trusts the configured Upper LLM and Coding Agent as engineering
executors. Core owns Project, FeatureArea, Task, Review, Execution, immutable
definitions, managed-worktree provenance, exact Run/Thread/Turn identity,
confirmation, receipts, cancellation, recovery, stale fencing, observed results,
Verification, Acceptance and Git delivery. The approved Repository/member/path
set is an **engineering execution scope**, not a cryptographic or hostile-OS
security boundary.

Dev Bridge v1 does not promise to stop a malicious Coding Agent from reading an
arbitrary host path. Only effects observed inside the exact canonical managed
worktrees, attributable to the immutable definition and validated against the
approved member/write scopes, may reach Acceptance or delivery. Backend-native
sandbox, approval and permission settings remain enabled and are never silently
changed to danger/full access. Workspace roots and permission profiles express
intended scope; they are runtime provenance rather than a Dev Bridge hostile-
sandbox proof.

Accordingly, Codex isolation research is retained as
**CODEX_SANDBOX_HARDENING_DEFERRED** and is not a v1 Coding admission condition.
The 72-case matrix, same-Thread narrowing proof and modified Codex build are not
required for release. Production still fails closed when the actual Client cannot
serialize the full workset, worktree/root identity is incomplete, backend/config
binding is unavailable, or a historical definition lacks exact targets/primary
member/runtime provenance. No path may fall back to Project inventory, current
Workspace inventory, `repositories[0]`, a recently selected repository or prompt
text.

For a multi-member Coding definition, `primaryMemberId` is explicitly authored
and confirmed with the exact target set. It chooses only the runtime cwd. It does
not add, remove or widen a member. The durable RuntimeWorkset is the only bridge
from an exact approved definition or Review grant to a Run: it freezes ordered
member bindings, managed roots and identities, start HEAD/snapshot, intended
read/write scopes, backend/model/runtime identity and a content hash. Start,
resume and subsequent turns reuse that frozen value. Implementation and required
Verification have distinct Runs in one Execution and one canonical conversation,
while the workset content hash and member set remain identical.

Latest bounded diagnostic: **CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED**; the original
72-case matrix still has 26 unexpected accesses. See the
[causal controls and independent-terminal next step](codex-offline-isolation-diagnosis.md).
This does not change any production permission boundary.

Project status: **PARTIAL_IMPLEMENTATION**. The narrower production milestone is
**MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED**.

The production Client and Coordinator carry a durable v77 RuntimeWorkset through
explicit start, managed member-worktree preparation, primary-member cwd,
start/resume/turn bindings, and per-member end observations. NO_START remains
definition-only. Missing layout/backend evidence fails closed with a specific
code. A production-facade fixture proves the full dual-repository Implementation
→ Verification → Acceptance → v76 delivery path, including response loss and
restart recovery. A separate production-facade fixture proves two-member,
read-only CODING_AGENT Review with a formal v2 result and mutation rejection.

Last completed closure: **MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED**.
The prior multi-member Upper Review → v2 NO_START approval and Direct File
delivery closures are preserved.
Explicitly started DIRECT_FILE Tasks now pass isolated dual-repository mutation,
formal result/acceptance, Task commit and separately confirmed FeatureArea merge,
including partial effects, lost responses and restart recovery. See the [current execution report](project-centric-multi-repository-execution-report.md).

The accepted model is Project-centric. An AISession binds only a Project;
repositories are resources owned by that Project. A Project may be empty.
DevelopmentCycle remains the sole per-Project/per-Repository branch authority.
FeatureArea continues to own its canonical Workspace and AgentConversation;
Task owns phase. Workspace membership never authorizes an operation.

This document records the current implementation boundary. The existing
single-repository workflow descriptions in the linked architecture documents
remain compatibility descriptions; they are not authority to infer a primary
repository or expand a grant to all Project/Workspace resources.

## Implemented resource and intent paths

- Desktop creates a logical Project with zero, one, or several repository rows,
  and can append repositories to an existing Project. Each path is independently
  resolved to a canonical Git root and must have an available HEAD commit.
- Core validates the entire collection before writing. The production SQLite
  registration store rechecks duplicate paths, aliases and ownership under its
  writer transaction; Project, repositories, events and durable receipt commit
  together. No repository is transferred between Projects.
- A stable `requestId` identifies a registration payload. Exact replay reads the
  durable original result before revalidating paths. Different payloads conflict.
  Default aliases combine the basename with a canonical-path digest, so same-name
  directories do not collide. A registered resource is not file authorization.
- `SELECT_PROJECT` confirms binding of the existing AISession to a Project
  without a repository or branch. `CONFIGURE_DEVELOPMENT_CONTEXT` separately
  validates the whole submitted collection of repository/ref rows and creates
  repository-qualified command receipts. Historical `SELECT_PROJECT_BRANCH`
  retains its original command interpretation.
- Project development context reports all rows, including unavailable sources.
  Configuration of available selected rows is independent of an unavailable
  unrelated repository. Reading options/context has no binding or worktree effect.
- A planning Task may omit both repository and baseline. It creates no Review,
  grant, executable definition, Run or worktree. Existing explicit single-repo
  planning remains a compatibility path.

Repository unlink uses the schema-v84 ACTIVE → UNLINKED lifecycle. It retains
the immutable Repository identity and original Project ownership while removing
the row from current inventories and fencing every new engineering writer. The
transaction does not access or delete the local Git directory or managed
worktrees. See [the safe unlink closure](safe-repository-unlink-closure.md).

## Contracts and migration

`internal/workset` describes an exact frozen set: Workspace identity, encoding
version, repository/member identities, member version, immutable origin commit,
optional origin ref / DevelopmentCycle provenance, separately observed commit,
explicit read permission and `NONE`, `PATHS` or `REPOSITORY` write scope.
Every path belongs to one repository target. Absent targets have no permission.
The fingerprint covers the whole set and permission boundary. Instructions are
not included or rewritten; Prompt Neutrality remains unchanged.

Task Definition v2 uses `repositoryTargets` and the `REPOSITORY_TARGETS` scope
marker exclusively; legacy top-level repository/baseline fields must be empty.
Verification checks name their repository, relative working directory and
applicable repository set; required checks must cover the complete target set.
Formal Upper Review reports can propose these targets through schema version 2.
Canonical APPROVE with NO_START writes an immutable Version=2 definition and
advances the Task to IMPLEMENTATION_READY without creating an execution or write
grant. The approved targets, work scope and verification requirements must match
the exact formal proposal. V1 definitions remain verified against their original
raw bytes/hash and are not reencoded by migration; v1 approval stays compatible.

SQLite v73 adds `project_registration_receipts` and `workspace_members`.
Workspace members preserve repository, immutable origin, alias, worktree identity
and preparation facts. Historical Workspaces are projected to one member without
changing IDs, definition payloads, approvals or receipts. Missing/removed legacy
worktrees become FAILED; quarantined/unavailable Workspace facts become UNKNOWN.
Membership cannot be deleted or rebound. A ready member requires an exact
worktree, with ownership checked on both INSERT and UPDATE.

SQLite v74 permits only unapproved pure planning intent to have a NULL repository:
empty baseline/specs and no current Review, Execution, approval or definition.
It replaces the known Task-insert guard fragment and fails on an unexpected guard
shape. Historical schema files are unchanged.

SQLite v75 removes only the worktree creator Run's UNIQUE constraint, preserves
historical worktree/workspace/grant records, and adds `run_repository_bindings`.
Each binding freezes Review/Run/Project/FeatureArea/Workspace, repository/member
version, origin and preparation state, then pins the worktree root identity and
start snapshot. `worktrees.run_id` remains creation provenance. A later Run uses
explicit bindings to reuse members; it does not overwrite their creator Run.
Review targets and grant protocol version are durable and immutable. Nullable
legacy Workspace/grant columns support v2; scalar Run roots are v1-only authority.

## Workspace preparation and actual runtime boundary

Workspace service can add and find exact repository members in the existing
canonical Workspace. Reuse preserves prior origins and member worktrees.
Member IDs determine distinct worktree IDs, branches and sibling paths. New member
paths never sit beneath a legacy authorized worktree root. Physical creation
followed by metadata failure is recovered by verifying the original branch/root;
existing dirty work is retained.

One UPPER_LLM Review uses one logical Run with multiple member bindings. Its grant
stays PREPARING until every required member is ready and observed. Failure before
physical creation or after creation but before metadata persistence can resume the
original admission; the existing workspace, member identities, dirty files and
generation are retained during same-epoch retry. After process restart, formal recovery
rotates epoch/generation and the caller explicitly uses `review_access_prepare`
with that exact target to continue partial preparation. Old admission replay cannot
activate the recovered generation. Multi-member cancellation and recovery use the existing
claim/epoch protocol. Explicit recovery rotates generation/epoch while retaining
the frozen set, original snapshot and pinned root identity.

The production Codex adapter now binds the exact primary cwd, runtime workspace
roots, writable roots, model/reasoning/service tier, authorization ceiling, and
structured schema on start, resume, and turn. Runtime admission uses the durable
v77 RuntimeWorkset and fails with specific layout, backend, or evidence codes.
Offline excluded-temporary-directory checks remain failed and same-Thread hostile
narrowing remains unverified; those facts are retained as deferred hardening
diagnostics and do not disable the trusted-executor runtime adapter.

Direct File inputs now carry repository/member identity and destination identity;
legacy grants remain tied to their original root. Supplied identities are checked
against that durable grant. Cross-repository/member atomic move is rejected.
V2 Task Direct File authorization uses an exact consumed START_DIRECT_FILE confirmation;
all frozen members must be ready before ACTIVE. Per-member write scopes and
request/epoch/generation fences apply. Explicit recovery is required after restart.
Generic enable/disable cannot start or close this Task-owned authority.
Upper Review reuses the file transport only for list/read: every v2 request
must supply exact repository/member/version, even for a singleton target set.
The parent grant, current Task/Review/Run/claim, epoch/generation, member binding,
root identity and path boundary are rechecked. Reads expose managed-worktree source,
origin commit and actual start snapshot separately. Evidence is repository/member
qualified; generic repository reads cannot bypass the active Review grant.
Adding a Workspace member does not expand an existing grant.

## Formal Review and approval boundary

A repository-free Task can prepare an explicit readonly UPPER_LLM target set.
Preparation/options are read-only; confirmation admits the exact set through the
existing Review admission and receipt transaction. Unselected unavailable sources
do not participate. No latest-branch lookup silently replaces admitted origins.

The production facade and MCP schemas accept reports with repository-qualified
evidence and v2 implementation proposals. Approval-ready reports revalidate every
member source/snapshot and evidence before finalization. Findings-only FURTHER and
BLOCKED reports may omit implementation targets/specs. Without cited evidence they
can report an unavailable source and close the read grant without creating an
approval. REVISE/REJECT, current pointers and stale-decision fences remain canonical.

Admission, report and approval each replay their original durable receipt after
response loss. The one approved v2 revision is publicly readable with its original
payload hash after database reopen. `APPROVE` is explicitly distinct from
`APPROVE_AND_START`; the latter cannot silently become NO_START. Upper Review and
v2 approval require no unrelated backend/model selection. Actual Coding start
continues to require its own supported adapter and explicit runtime configuration.

## Direct File results and per-repository delivery

SQLite v76 adds immutable multi-member Task file grants, explicit start receipts,
durable mutation receipts, and per-repository Task/FeatureArea delivery steps. Legacy
scalar grant and delivery records remain protocol 1; historical definition bytes,
hashes, IDs, result and receipt payloads are preserved. Migration starts from v75,
is safely repeatable, and passes foreign_key_check. Historical migration files are
not rewritten and old NO_START/closed grants acquire no new authority.

Each mutation has exact member identity and request/payload fencing. Overwrite,
delete and move use content-digest preconditions. Durable intent precedes filesystem
effect; a synced effect journal proves response-loss recovery. Unproved effects
remain pending/unknown and block conflicting finalization. There is no promise of
cross-file or cross-repository atomicity, nor rollback of completed work.

Formal result submission derives the complete member set from the approved revision,
validates unchanged HEAD and final receipt-backed bytes, closes writes and advances
canonical acceptance atomically. RUN checks are unsupported before start. Explicit
user-waived NONE verification is permitted only through the existing confirmed
contract; a fixture waiver is not authorization for any real project.

Business acceptance and all Task member delivery intents persist together. Git is
separate: selective private-index commits retain unrelated dirty bytes and the real
index; readonly/unchanged members record NO_CHANGE without a fake SHA. Recovery
checks original parents, provenance and exact resulting tree. Complete aggregate
status requires all frozen members, not just the first successful repository.

FeatureArea merge requires its own confirmation and complete frozen plan over actual
accepted, committed, undelivered Task members. Source/target/expected HEAD/cycle and
Task provenance stay fixed across restart and mapping changes. Actual Git common-dir
and ref locks serialize shared targets; Git does not rely on a long SQLite write
transaction. A second FeatureArea's stale plan is refused after target drift.
Partial/unknown delivery retains its original receipts and fences conflicting work.

Legacy single-repository Direct File result, acceptance, commit and merge remain
compatible. Ambiguous historical scalar delivery spanning distinct repositories
continues to fail closed; it is not fabricated into a v2 aggregate receipt.

Remaining project work includes manual Desktop/Card acceptance. Hostile sandbox
hardening remains deferred and real-model E2E remains opt-in and not run; neither
changes the completed trusted-executor Coding v1 or safe unlink gates.

## Validation interpretation

Tests use temporary SQLite databases and independent temporary Git repositories.
No real Coding Agent, user runtime, live DataRoot, desktop UI, device or simulator
is used. Test fixtures were brought up to the already-required Session
initialization, backend/runtime and explicit implementation-mode protocols where
needed to exercise the relevant existing Git/acceptance tests.

The execution report records exact commands, results and remaining failed tests.
Mock/fixture results must not be represented as production backend verification.

- `MANUAL_DESKTOP_ACCEPTANCE_PENDING`
- `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`
- Real Coding Agent exact multi-root smoke: `NOT_RUN` (explicit opt-in)
- Direct File cross-repository implementation and partial Git delivery: `CLOSED`
- Coding Agent cross-repository implementation, Verification, Acceptance and delivery: `CLOSED` with the production Client fake App Server

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.


## 跨 Core CODEX_REQUEST 后续状态（2026-09-17）

固定 0.149.0 的同实例 loaded Thread resume 已实际证明 pending request replay，新的独立 App Server transport 与 v79 receipt 可恢复原操作。回答已消费而 resolved 丢失的窗口仍无可重放的协议确认；UserDecision continuity 保留 partial。不得将恢复原请求与创建 successor 混为一谈。证据见 [continuity 记录](codex-request-cross-core-continuity.md)。
