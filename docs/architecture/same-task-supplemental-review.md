# Same-Task supplemental Review

`task_review_request` is an explicitly authorized request for additional engineering
review. GPT decides whether evidence is sufficient, the user authorizes the work, and
Core checks deterministic technical admission. Purpose text is stored verbatim; it is
not a natural-language permission test. An eligible technical retry remains independent.

## Command and observed target

Input contains `aiSessionId`, `taskId`, `featureAreaId`, stable `requestId`, nonempty
`purpose`, and `expectedTarget`. Every target field is required: `phase`,
`taskDefinitionRevisionId`, `currentReviewId`, `currentExecutionId`, `pendingDecisionId`.
Empty IDs explicitly assert absence and are compared exactly, never treated as wildcards.
The shared REQUEST_TASK_REVIEW action exposes this target as `expectedReviewRequest`.
Console/Web map `reviewPurpose` to command `purpose`; GPT/Local use the shared MCP command.

A receipt records its stable ID, purpose, observed source phase/revision/Review/Execution,
superseded Decision, creation time and recorded REVIEW_REQUIRED transition. `replay=true`
returns that historical result, not a claim that the Task is still in the recorded phase.
Current Task facts must be read before any new subsequent command.

## Admission states

| Observed Task/operation | Behavior |
|---|---|
| ACTIVE IMPLEMENTATION_READY, no Execution/Run | Eligible with current approved definition and idle authority. |
| CONFIRMED, PENDING candidate with no Run reservation | Candidate is durably superseded; no fake failed/canceled Run is created. |
| IMPLEMENTING/VERIFYING/VERIFICATION_READY/ACCEPTANCE_PENDING, terminal Execution | Exact current Execution must have terminal outcome and accurately bound terminal Run, finalized authority/quiescence, no conflict or unresolved UNKNOWN. A phase name alone proves neither activity nor safety. |
| VERIFICATION_READY between phases | CONSUMED/RUNNING Execution, RUN verification mode, exact SUCCEEDED Implementation Run, no Verification Run or other reservation; the same authority, epoch, workspace and finalization checks still apply. Preserve RUNNING outcome and record supersession without inventing a TASK_ACCEPTANCE decision. |
| CREATED/PREPARING or any active reservation/writer | Refused until original coordination has safely settled it; no automatic cancel/recover. |
| REVIEW_REQUIRED | Expose the existing pending-start request where recorded; identical receipt replays, fresh request cannot overwrite it. |
| REVIEWING | No parallel request/start; existing failed-review continuation still follows its original protocol. |
| REVIEW_APPROVAL_PENDING | Use original REVISE/current proposal workflow. |
| Terminal Task, retired/retiring area, legacy read-only | No reopening; new work follows the existing new-Task routing. |
| File operation active or file authorization PREPARING | Refused without waiting for file I/O to finish inside the supplemental transaction. |

The Core read predicate is reused in the final write transaction. Runtime/database errors
remain read failures; known scope/busy/lifecycle mismatches are explicit unavailable
reasons. A projected available action is not authorization to skip write-side checks.

## Atomic supersession and history

V55 adds immutable supplemental request, Review-start and Execution-supersession relations
plus a current request pointer. These reference existing Task/Review/Execution and approval
records; they do not create a second execution or approval engine.

The writer transaction repeats the full observation, stores the request and superseded
Execution relationship, settles an unresolved TASK_ACCEPTANCE with `REQUEST_REVIEW`,
closes only the exact eligible old Task file authorization, clears current Execution and
confirmed TaskPlan, and enters REVIEW_REQUIRED with an event. Any failure rolls the entire
transition back. The old Execution outcome and old Run/result/prompt never change.
The verification-before-reservation gap has no acceptance gate: `pendingDecisionId` is
explicitly empty, only the candidate supersession is recorded, and late verification
start is rejected by the original Task phase/current-Execution checks.

The old Review, approved definition and APPROVE receipt remain historical sources. Their
Task pointers persist while awaiting the new Review, but the Review phase removes dispatch
qualification. `currentReviewRequest` describes that state explicitly. Existing required
Review depth is preserved; otherwise the exact source Review's depth supplies the floor.
No new Review is falsely reported as running before `task_review_start` succeeds.

The original Review start transaction links the new Review to the current supplemental
request. Multiple review attempts/REVISE rounds preserve their individual Review records.
Failed starts or review results do not restore the old candidate or file authorization.

## Reapproval and new implementation

A new APPROVE on a previously approved Task must reference the current supplemental
request, its exact newly started Review and the original revision. Old approval receipt
replay is resolved first and never re-materializes authorization. Approval creates a new
immutable definition through the existing reducer, with a source-revision link; old
approval materializations remain intact. V55 removes only their obsolete one-per-Task
uniqueness constraint, retaining one immutable row per approval and its unique revision.

Execution and TaskPlan IDs for new approvals derive from Task plus exact Review Approval,
so a Task with zero or many historical Executions does not collide with its first fixed
ID. Persisted legacy IDs remain readable. `execution_start` retains the original exact
reservation/dispatch protocol; empty initial Execution targets can only replay their own
approval-owned candidate or exact legacy candidate, never a same-revision retry successor.
A new approved Direct File definition receives new revision-bound file authority instead
of a Coding Agent Execution.

A V55 integration fix also adds the existing RETRY_IMPLEMENTATION receipt to the original
current-Execution guard. The prior V54 source is unchanged; retries remain independent of
supplemental review. Every new revision/candidate still needs its exact canonical source.

## Direct File and competing commands

The original Direct File Authority's `WithCapability` holds the DB operation gate in
shared mode before acquiring its original SQLite writer transaction and grant mutex.
Supplemental read qualification and transition use a nonwaiting exclusive try-lock on
that same gate. A file operation already admitted therefore yields an immediate busy
result. The transition holds the gate through transaction completion and takes locks in
the same order; it does not wait for file I/O while holding a DB write transaction.

An ACTIVE exact Task/revision/session file authorization can be closed in the same
transaction. PREPARING or unrelated/standalone authorization blocks and is not closed.
After the transition, old file requests fail their original state/phase checks. New
approval creates a new grant; old grant IDs and historical receipts never reactivate it.
Review executor selection remains separate from Implementation mode.

Execution reservation and supplemental transition compete at the original SQLite writer
boundary. If reservation wins, active authority/Run prevents supplemental admission. If
supplemental transition wins, old current pointers, phase and supersession guards reject
late fresh starts/acceptance/retry. Exact historical receipts remain readable; they do not
switch current revision or cause dispatch. This is a static control-flow account, not a
claim that a race or replay experiment has been run.

## Conversation and read-only Card

After accurate user authorization, GPT may call `task_review_request` then
`task_review_start` without a second confirmation. It supplies explicit Review executor,
depth and complete incremental instructions. New Review approval is still a separate
user decision. Analysis-only requests do not start work; no REJECT, fake REQUEST_CHANGES,
replacement Task or arbitrary phase setter substitutes for this transition.

Card remains read-only and shows the shared pending-review status; browsing never changes
GPT's write target. Repository reads, FeatureArea pagination, source inspection and
RETRY_IMPLEMENTATION retain their existing contracts. Actual inspected workspace/commit
facts remain distinct from a registered development-ref baseline; executors must not
reset or merge merely to make those identities appear equal.

Formatting and source review do not validate a live host, migration, concurrency or replay.
No business objects are used to verify this implementation. Runtime and host acceptance
remain separate pending work.
