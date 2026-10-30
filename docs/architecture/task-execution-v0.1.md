# Task-owned workflow — schema v46（历史记录）

当前运行合同请先阅读 [当前任务流程合同](current-workflow-contract.md)。下方 v46/v76/G0 能力边界为对应时期记录，不能作为当前版本的准入说明。

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

> Project-centric update (schema v76): [current contracts and remaining boundaries](project-centric-multi-repository-closure.md). Multi-member Upper Review and v2 NO_START approval are complete. Explicitly started v2 DIRECT_FILE Tasks support exact member permissions, formal results/acceptance and recoverable per-repository Task commits and FeatureArea merges. Coding Agent multi-root execution remains unsupported. Workspace membership never grants access; the legacy single-repository descriptions below remain compatibility behavior.

> Coding runtime G0 update: [actual binary/schema and offline isolation evidence](coding-runtime-capability-evidence.md). Experimental permission DTOs and evidence identities are preparatory only. Multi-repository CODING_AGENT start, required Verification.RUN and Coding Review remain blocked; the installed helper did not prove complete temporary-directory denial. Confirmation reads/options do not create Execution, claim or handoff, and no waiver or alternate Thread bypass is used.

Task is the durable engineering intent and the sole semantic phase authority.

```text
Project → FeatureArea → Task
                       ├─ Review [0:N] → Run [1:1]
                       └─ Execution [0:N]
                            ├─ Implementation Run [0..1]
                            └─ Verification Run [0..1]
```

FeatureArea owns the canonical Workspace and AgentConversation. Its lifecycle is
DISCOVERING, ACTIVE, RETIRING, RETIRED or DECOMPOSED. Tasks own neither resource.
One active agent operation per FeatureArea is enforced by the existing durable
claim; independent FeatureAreas can run concurrently. Project-scope Inspection
keeps its internal operation representation and is not a FeatureArea Task.

## DIRECT_FILE v2 implementation and delivery

An immutable approved multi-repository DIRECT_FILE definition starts explicitly via
purpose-scoped confirmation. It creates no Coding Execution or implementation Run.
All frozen members must be prepared before writes become ACTIVE. Restart requires
formal recovery with the old observed epoch/generation; canceled or result-closed
grants cannot be revived by replay. Required RUN verification remains unsupported
for this path and must fail before activation.

Core-observed member results freeze snapshots and receipt-backed changed paths while
closing authorization and moving the canonical Task to ACCEPTANCE_PENDING. Business
acceptance moves it to SUCCEEDED and stores every member's Task delivery intent in
one database transaction. Git runs afterwards; an accepted Task may still have
partial or unknown delivery. Member outcomes COMMITTED/NO_CHANGE determine completion,
not a scalar SHA. READ-only and unchanged members do not create commits.

FeatureArea delivery is separately confirmed from all accepted, committed, undelivered
Task members. Its immutable per-repository plan pins exact source and expected target
state. Commit/merge recovery proves the original parents and resulting tree before
recording a lost response. It retains completed members and never rolls them back,
changes request identity or follows a newer development mapping. Pending effects
continue to fence conflicting writes; ordinary reads never advance delivery.

## Durable lifecycle and phase

Task.status is ACTIVE, SUCCEEDED, CANCELED or FAILED. Task.phase is a named string,
independent of status. Counts of Reviews or Executions are history, never phases.

| Authorized fact / gate | Required phase | Result |
| --- | --- | --- |
| task_create with stable requestId | New intent | ACTIVE / REVIEW_REQUIRED; required_review_depth=FAST; no Review, revision or Execution |
| task_review_start for exact Task and explicit reviewDepth | REVIEW_REQUIRED, or REVIEWING with a terminal unsatisfied Review; requested depth satisfies minimum | New Review and linked Run, current_review_id replaced, REVIEWING; minimum becomes requested depth |
| Current FAST or DEEP Review completes with APPROVAL_READY and valid proposal | REVIEWING, exact current Review | REVIEW_APPROVAL_PENDING and pending gate Approval; required_review_depth=null |
| Current FAST or DEEP Review completes with FURTHER_REVIEW_REQUIRED | REVIEWING, exact current Review | REVIEW_REQUIRED; required_review_depth=DEEP; no Approval Decision or automatic Review start |
| Review infrastructure failure or terminal without valid structured result | REVIEWING | Phase and admitted minimum retained; no disposition or Approval; another Review may be started once safe |
| APPROVE current Task review gate | REVIEW_APPROVAL_PENDING | Initial immutable revision; IMPLEMENTATION_READY; required_review_depth=null; no Execution |
| REVISE current Task review gate | REVIEW_APPROVAL_PENDING | REVIEW_REQUIRED; required_review_depth=FAST; new Review cycle |
| REJECT current Task review gate | REVIEW_APPROVAL_PENDING | Task CANCELED; required_review_depth=null |
| execution_start | IMPLEMENTATION_READY | Current Execution, Implementation Run and claim reserved; IMPLEMENTING |
| Successful Implementation | IMPLEMENTING | VERIFICATION_READY; explicit user verification waiver can enter ACCEPTANCE_PENDING |
| Start/continue Verification | VERIFICATION_READY | Same Execution's Verification Run; VERIFYING |
| Verification transport/report completion | VERIFYING | ACCEPTANCE_PENDING; report verdict is evidence, not acceptance |
| ACCEPT | Acceptance gate | Task SUCCEEDED |
| REJECT | Acceptance gate | Task CANCELED |
| REQUEST_CHANGES | Acceptance gate | Same Task, new immutable revision and Execution, IMPLEMENTATION_READY |
| REQUEST_REVERIFICATION | Acceptance gate | Same Task and revision, verification-only Execution, VERIFICATION_READY |

ReviewDepth is exactly FAST or DEEP and is independent of ReviewPlan.mode
(FOCUSED/REPOSITORY), model and reasoningEffort. Both depths use the same read-only
authorization and exact caller-authored prompt. A FAST Review may satisfy the gate
directly; FURTHER_REVIEW_REQUIRED requires a caller-started DEEP Review, with
repeated DEEP attempts permitted. Each Review still owns one Run. Task terminal
transitions clear required_review_depth. The complete
[Review depth, Approval and migration contract](review-approval-workflow.md)
freezes structured fields, minimum-depth admission and latest-only Approval.

Infrastructure failure, cancellation of an operation, response loss and reconnect
do not mark the Task terminal or advance its semantic phase. Existing recovery,
quarantine and physical backend disposition fences remain authoritative safety
facts. After an interrupted terminal Execution is safely finalized, explicit
Task Acceptance can request changes/reverification or close the Task. This also
covers a successor canceled before its first Run. Successful transport alone
never accepts the Task. Task-level cancellation and FeatureArea retirement may
explicitly close Tasks through canonical cancellation.

## Creation, review succession and approval

`task_create` records title/intent, problem statement, constraints and exact
FeatureArea/repository ownership. Its durable receipt is checked before mutable
runtime prerequisites on replay. Equal request/payload returns the existing Task;
changed intent conflicts. The next action is START_TASK_REVIEW.

Reviews have NOT NULL task_id and exact Project, FeatureArea and repository
ownership, UNIQUE(task_id, ordinal), immutable linked Run and runs.review_id
uniqueness. Admission atomically creates Review, Run, claim and request receipt,
sets Task.current_review_id and REVIEWING. The pointer is fenced by ordinal and
retained after terminal completion. No created_at heuristic determines latest.

Gate completion checks exact current Review, terminal successful Run, available
structured proposal and provenance in the same progression transaction. Stale
callbacks cannot move the Task. Approval's business subject is TASK_REVIEW_GATE
with subjectId = Task.ID; source Review ID remains receipt/provenance data. The
external decision input requires taskId, decisionId, option and requestId; it has
no selectable reviewId. Only the current/latest Review can authorize a new
transition. An old unresolved approval returns STALE_REVIEW_APPROVAL. Exact replay
of a committed receipt is observational and does not re-approve history.

APPROVE updates the existing Task and freezes its initial TaskDefinitionRevision
from original intent/constraints and the current approved proposal. It writes
receipt, revision pointer, phase and first FeatureArea activation atomically.
It contains no INSERT INTO tasks and creates no Execution. REVISE and REJECT
retain the original Task and Review history. Initial approval requires explicit
execution_start; reads, polling and restart do not start it.

## Execution cycles and atomicity

Task.current_execution_id is an authoritative fence. Each Execution binds the
exact Task and immutable revision. UNIQUE(execution_id, purpose) enforces at most
one IMPLEMENTATION and one VERIFICATION Run. The first start atomically creates
internal authorization records, Execution, current pointer, Run and FeatureArea
claim and moves Task to IMPLEMENTING. Existing Acceptance-created successors use
the same reservation path; their previously authorized continuation may still be
picked up by write-side reconciliation. Verification-only successors have no
Implementation Run. Backend retries reuse the same durable Run and Execution.

Task progression is written by canonical SQLite transactions, not presentation
or a child status reducer. Completion updates its operation facts and eligible
Task gate together; mismatched phase/current pointer aborts advancement. Internal
TaskPlan and execution_proposals records support immutable authorization and
history; they expose no independent Task confirmation or Execution authorization
workflow. The old app-only ui_decision_get, ui_task_decide and ui_execution_decide
tools are removed. Runtime permission approval remains separate.

## Presentation and guidance

Card, Console, JSON and Project inventory consume Task title, status and phase.
selectedTaskId always identifies the semantic Task. Review/Run status is activity
or diagnostic detail; reviewInstruction is not the Task title. The five rail
stages are Review → Approved → Implementation → Verification → Done.
REVIEW_APPROVAL_PENDING shows Review complete / Approved pending;
ACCEPTANCE_PENDING shows Verification complete / Done pending. Only Task
SUCCEEDED completes Done. Recovery and runtime permission facts may block an
available action without changing durable Task.phase.

Guidance maps REVIEW_REQUIRED to START_TASK_REVIEW, REVIEWING to
WAIT_FOR_REVIEW_COMPLETION, REVIEW_APPROVAL_PENDING to RESOLVE_REVIEW_APPROVAL,
IMPLEMENTATION_READY to START_EXECUTION, IMPLEMENTING to WAIT_FOR_IMPLEMENTATION,
VERIFICATION_READY to START_VERIFICATION, VERIFYING to WAIT_FOR_VERIFICATION and
ACCEPTANCE_PENDING to RESOLVE_TASK_ACCEPTANCE. Terminal Tasks report
WORKFLOW_COMPLETE. A safely terminal, unsatisfied Review allows START_TASK_REVIEW.

## Schema and validation boundary

Current schema is v46. Fresh databases install the target structure through the
versioned migrations. The v45 → v46 migration adds immutable next-run runtime
configuration to Approval materializations and Execution proposals, retaining
NULL for historical selections that were never recorded. It also widens the
persisted reasoning-effort enum without changing Task ownership or ReviewDepth.
Model, reasoning effort and Codex Fast Mode for the next Run remain separate
from TaskDefinitionRevision, semantic ReviewDepth and caller-authored prompts.

The v44 → v45 Review-depth migration retains its existing semantics:
v44 historical Reviews backfill as DEEP; existing Approval
Decision evidence alone backfills APPROVAL_READY. Safe quiescent Task phases,
current pointers and historical prompt/result/receipt bytes and hashes remain
unchanged. REVIEW_REQUIRED retains a FAST minimum; terminal failed/canceled
Review attempts in REVIEWING retain a DEEP minimum. Approval, implementation
and terminal Tasks have no authoritative Review-depth requirement.

Ambiguous active/in-flight v44 Review state, unprocessed successful REVIEWING
results and pending Approval without a successful Review/Decision fail closed
with explicit development database rebuild guidance. Rollback preserves schema
version and history. Migration creates no Review, Run or Approval and advances
no Task phase. v43's existing empty-database upgrade and populated-Task rejection
rules remain unchanged; historical schema files retain their original semantics.
No live database is reset by this implementation. There is no historical phase
inference, old/new dual write or REVIEW pseudo-Task compatibility.

Tests use temporary SQLite databases, temporary Git repositories and fake
backends. No real Codex, external network, live database or device is required.
UNKNOWN/UNKNOWN_ORPHANED recovery fences and Retire remain unchanged. Permanent
orphan recovery preserves historical UNKNOWN evidence while removing only that
exact revoked authority from current progression blockers. Acceptance, Git
delivery and guidance share this durable determination; unresolved authority,
active writers and conflicting Direct File grants remain blocking. Production
Card recall host integration remains `HOST_INTEGRATION_PENDING`.

## Prompt Neutrality Principle (frozen)

Dev Bridge MUST NOT author, prepend, append, rewrite, summarize, substitute, or
semantically augment natural-language instructions sent to a Coding Agent.
The complete instruction MUST come from the configured Upper LLM / caller.
Leading/trailing whitespace, newlines, Unicode and symbols are transported
verbatim, with no trimming, normalization, extra newline or suffix.

| Operation | Authoritative instruction |
| --- | --- |
| Task Review, including another Review or scope expansion | Explicit `task_review_start.reviewInstruction`, durably stored in `Review.Instruction`; linked Run bytes must match |
| Implementation | `WorkSpec.CodexInstruction` in the Execution's immutable TaskDefinitionRevision |
| Verification | `VerificationSpec.CodexInstruction` in the same authoritative immutable revision |
| Context Probe / Inspection | Explicit caller `CodexInstruction` stored with the Probe |
| Recovery, continuation, resume and follow-up turns | The same operation's frozen authored instruction; resume restores conversation identity without composing instructions |

Missing or blank exact instructions are INVALID_ARGUMENT / CONTRACT_INCOMPLETE;
admission must not reserve a new Run, and a corrupt already-reserved handoff must
fail before backend dispatch. Goal, WorkSpec, Task metadata, constraints, phase,
Review ordinal, plan, selected modules and FeatureArea state are never prompt
fallbacks. Reconciliation may continue an authorized operation, but cannot author
its next instruction. A changed implementation requires an authorized immutable
revision, not a replacement prompt for an existing revision.

Structured schemas, authorization metadata and runtime capabilities are machine
protocol, not natural-language prompt augmentation. Bridge owns durable
coordination, authorization, repository/workspace/runtime boundaries, tool and
capability enforcement, structured I/O, model/reasoning configuration,
lifecycle/fencing/replay/recovery and auditing. These fields remain separate from
the prompt. Review plans and module selections are authorization/relevance
metadata. Verification checks, acceptance criteria, scope and mode remain
structured contracts/evidence inputs. Any semantic request to execute them must
be written into the Upper LLM's instruction.

Review uses `outputSchema` with version, summary and nullable workSpec /
verificationSpec, so a findings-only Review need not invent a final proposal.
The existing latest-only Approval gate still requires a complete valid proposal.
Verification retains its version 2 report schema. Implementation uses a minimal
machine result envelope: `resultDisposition` (COMPLETED or SCOPE_CONFLICT) and
`summary`. Bridge handles the explicit disposition, never a natural-language
prefix or keyword; a summary mentioning `SCOPE_CONFLICT:` has no control meaning.
This envelope changes no Task-owned ownership, phase, revision or acceptance relation.
Backends without the required structured-output capability fail closed, without
a prose fallback or starting a turn.

Review and Verification are mechanically read-only: Codex operation startup,
per-turn sandbox policy (including resumed threads), permission ceiling and
capability handlers enforce authority independently of prompt wording. Removing
prose does not increase filesystem, Git, tool or runtime authority. Bridge does
not infer semantic architecture boundaries from command text or model prose.

The local audit invariant is:

```text
authorized authored instruction bytes
  == final_codex_prompt bytes
  == actual StartTurn input[0].text bytes
SHA256(authored) == prompt_sha256 == SHA256(sent)
prompt_bytes == byte length(authored)
```

`final_codex_prompt`, `prompt_sha256`, `prompt_bytes` and `prompt_sent_at` remain
durable, immutable dispatch evidence. As before, `prompt_sent_at` records the
pre-dispatch audit time; transport acceptance and response loss are tracked by
the separate durable turn/conversation protocol. Operational logs expose
fingerprints, not instruction text. Tests cover the builders, stored Review,
frozen revision, runtime dispatch and actual JSON-RPC serialization separately.

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.


## 原 App Server continuity（v79）

开启独立 App Server transport 的操作将 instance binding 与冻结 executor checkpoint 在 Thread/Turn effects 前持久化。Core reopen 只 attach 已验证的原实例、原 loaded active Turn；不重新发送 prompt，不重新调用 turn/start，不推进 Task.phase。exact request replay 后恢复原等待或发送已记录的不可变决定。只由原 Turn facts 与既有 workflow transaction 推进后续 Verification。无 binding 的旧 operation 保留旧恢复语义；idle 不证明原答案已消费。详见 [continuity 执行记录](codex-request-cross-core-continuity.md)。
