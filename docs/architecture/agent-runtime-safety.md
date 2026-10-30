# Agent runtime safety (Wave 1)

The [Task-owned v46 contract](task-execution-v0.1.md) defines semantic progression.
Task owns Review [0:N] and Execution [0:N]; Review has one Run and Execution has
serial implementation/verification Runs. AgentExecution is the operation envelope;
its cancellation or recovery does not independently terminalize the semantic Task.

One DataRoot has one writer Core. GPT and Local MCP subprocesses proxy that
Core's handlers; they never open its database or run startup recovery. Desktop
still supervises the three endpoints. Independent DataRoots remain concurrent.

Startup resolves the DataRoot and performs read-only validation, then acquires
its OS lock at `core-writer.lock` before creating managed directories, publishing
the marker, opening/appending the process log, opening/migrating SQLite,
allocating a durable runtime epoch, or recovering old operations. Creating an
absent root directory and the retained lock file are the only pre-ownership
filesystem writes. Validation repeats under the lock before layout mutation.

`dataroot.Open` returns an owned handle requiring `Close`. Core passes that live
handle to `controlplane.OpenWithDataRoot` without releasing or reacquiring it;
the process closes services and SQLite, writes its final diagnostic and closes
the logger before releasing the root lock. Direct `controlplane.Open` owns the
handle itself and releases it on bootstrap failure or after plane shutdown.
The existing `state/core-writer.lock` remains a nested database descriptor lock
for direct SQLite callers and runtime epoch fencing; it is no longer the Core
bootstrap ownership boundary.

Unix uses nonblocking `flock`; Windows uses exclusive `LockFileEx`. The open
handle owns the lock, not the pathname. Close or process death releases it; the
file is retained so subsequent owners lock the same inode. There is no lease,
heartbeat, expiry, lock stealing, or distributed election. A competing Core
fails with the existing writer-conflict stderr diagnostic before any managed
layout repair, log append, migration or recovery. A stale lock file alone does
not prevent restart, including a crash before marker publication. Root symlink
aliases resolve to the same canonical ownership path.

## Authority and callbacks

AgentConversation is FeatureArea context lineage; the FeatureArea claim is
operation serialization and authority; Run is concrete execution history; the
backend Thread is a continuation handle. Runtime epoch identifies the writer
incarnation. They remain separate records.

Migration 27 adds append-only epoch history and the authorized epoch pointer.
New Runs and claims capture the epoch in their reservation transaction. It is
immutable; historical NULL ownership is not attributed to a new process.

Every store write transaction acquires SQLite's write lock and validates the DB
instance's epoch against durable authority. Runtime contexts additionally carry
the immutable Run/epoch pair. Run-targeted writes recheck the current claim,
operation kind/ID, Run and conversation. Host Tool contexts require an active
exact Thread/Turn, the current conversation and claim, and no finalization intent
at the actual SQL mutation. An authorization read before computation is not
sufficient. The model cannot supply an epoch.

This covers Run state/results/token usage, Goal reports, approval decisions and
delivery, Review access and prompts, workspace accounting, product
completion, and finalizers. The supervisor transfers its context to the reserved
verification Run before preparing it. Old callbacks keep their original context
and cannot adopt the successor. Rejections return `RUNTIME_EPOCH_FENCED` internally
and existing structured invalid-operation errors at Host Tool boundaries.

## Dispatch and finalization

`MarkThreadStartPending` commits UNKNOWN before external `thread/start`. Only
persisting the returned Thread binding resolves that marker to NO_ADVANCE. Losing
the response or binding therefore requires recovery; the Bridge does not invent
a Thread lookup capability. `MarkTurnPending` commits UNKNOWN before `turn/start`.
An accepted Turn with a lost response has no fabricated Turn ID.

After observing backend completion and closing/joining the owned transport,
the coordinator durably records an immutable, exact-owner finalization intent.
That closes the Host Tool mutation window. Local evidence/accounting work may
finish next. The terminal Run/result/token transaction consumes the intent and
CAS-updates the boundary, canonical safe head, continuation safety, quiescence,
and exact claim release. A failure aborts that complete transition; the preceding
observation remains available for restart reconciliation. Conflicting replays
fail. An identical committed replay does not repeat correctness writes.

An Execution may own serial implementation and verification Runs. Implementation
completion records its exact safe boundary while retaining the same operation
claim for verification. The product-terminal Execution transaction releases the
claim for its latest Run. UNKNOWN may instead release authority through the
permanently fenced, quarantined orphan path; this never changes its boundary. The intent
allows startup to deterministically fail an interrupted aggregate without
replaying a backend action. A successful backend Run alone does not imply the
entire Execution succeeded.

Review Approval atomically freezes the initial immutable definition of the
already-existing Task. Execution reservation follows under that Task. Canonical runtime reservation/finalization enforce the existing
writer, epoch, claim and immutable-definition guards. See [final v0.1](task-execution-v0.1.md).

## Restart and explicit recovery

Startup inspects older claims before generic interrupted-Run recovery. It never
recovers a current-epoch live operation. Categories depend on durable facts:

| Classification | Durable evidence | Legal result/action |
| --- | --- | --- |
| NO_ADVANCE | No external dispatch, or a persisted Thread binding before any Turn marker | Deterministically terminalize interrupted work and release its exact claim |
| KNOWN_SAFE | Exact completed boundary plus quiescence/intent and matching canonical head | Apply pending finalization; preserve a committed result, otherwise record interrupted failure; release safely |
| UNKNOWN | Dispatch may have advanced without a provable exact safe exit | Preserve UNKNOWN; pending cancellation automatically orphans; otherwise exact explicit recovery |
| CONFLICT | Ownership, epoch, Thread/Turn, intent or canonical boundary disagree | Preserve claim; expose RECOVERY_REQUIRED with exact identity and manual reconciliation diagnostic |

A healthy current-epoch Turn is BUSY while its exit is unknown. A terminal
UNKNOWN operation is RECOVERY_REQUIRED. If its epoch is still authorized, Core
can cancel through the exact owned supervisor, or restart before explicit recovery; elapsed time is never authority. After restart,
`agent_operation_recover` accepts only the precise old claim and conversation.

The shared GPT/Local MCP command requires `aiSessionId`, `featureAreaId`,
`operationKind`, `operationId`, `runId`, `agentConversationId`, and `requestId`.
It uses the existing explicit session/project-scoped product-command authority
seam, like context reset. It does not grant Execution authorization, remove
workspace files, or use a new generic Decision policy. It is never an Agent Host
Tool. Request replay validates the complete original target and returns the
original receipt even after later work starts; changed replay payloads fail.

Recovery requires an older fenced epoch, an exact terminal stranded operation,
UNKNOWN classification, and the expected current conversation. One transaction
ends that conversation with `UNSAFE_CONTEXT_BOUNDARY`, quarantines its workspace,
converges the exact child operation and appends a permanent orphan fence,
releases only the old claim, and creates a fresh threadless canonical successor for
a usable FeatureArea, and records an immutable recovery receipt. A terminal or
read-only legacy FeatureArea receives no successor and remains UNAVAILABLE; its
old UNKNOWN facts remain unchanged. The old Run and UNKNOWN boundary remain
history. No summary, Thread copy/fork, or guessed safe boundary is injected. New
Agent work claims the successor AgentConversation and starts its fresh Thread.
Task.status and Task.phase stay independent of child cancellation/recovery.
Only an authoritative Task-level decision, including explicit Task rejection or
FeatureArea retirement convergence, may terminalize semantic Task intent.
The successor uses the authoritative FeatureArea workspace policy; quarantined
workspaces do not determine canonical context.

Acceptance, Task commit, FeatureArea submission and workflow guidance share
Core's unresolved runtime authority predicate. An exact permanent orphan with
ended context, quarantined/detached workspace and released claim remains
immutable history and no longer blocks new canonical work. An unresolved
UNKNOWN/CONFLICT, incomplete revocation or current writer still blocks.
Revocation does not assert backend death or permit old callbacks to regain
authority; this distinction survives restart through the same durable facts.

Legacy unbound NO_ADVANCE observations need special care: pre-v27 did not mark
thread/start before dispatch. Such an old claim is classified UNKNOWN. Even a
previously quiesced immutable observation has an explicit abandon-and-rotate path;
its original provenance is preserved verbatim, while the recovery receipt
records the unknown operational outcome. Migration does not fabricate history.

Ordinary context reset remains idle-only and cannot bypass any claim. Reset
starts fresh semantic context intentionally; recovery explicitly abandons a
stranded unknown operation. Neither uses timeout-based unlocking.

## Crash acceptance matrix

| Window | Durable state after interruption | Next legal action |
| --- | --- | --- |
| Claim commits before thread/start marker | Reserved Run; no advance | Startup deterministic failure/release |
| thread/start accepted, binding lost | Thread-start pending, UNKNOWN | Restart then exact explicit recovery |
| Thread binding commits before turn/start | NO_ADVANCE, identified Thread | Startup safe failure/release |
| Turn marker commits; request accepted; response lost | UNKNOWN, no guessed exit | Restart then explicit recovery |
| TurnStarted arrives; durable Turn binding fails | UNKNOWN | Restart then explicit recovery |
| Completion observed; intent commits; terminal transition interrupted | Immutable observation, all-or-none Run/boundary/claim transition | Startup consumes known-safe intent, never replays Turn |
| Finalization commits; caller loses response | Terminal Run and completed boundary/released claim, or committed aggregate intent | Idempotent result/recovery reconciliation |
| Explicit recovery commits; old callback arrives | Old history and new canonical context/claim | Reject stale epoch/owner; successor remains unchanged |

Storage tests inject transaction failures at intent insertion, boundary/intent
application, claim deletion, recovery rotation/quarantine/receipt, and Execution
materialization. The subprocess protocol gate runs the real JSON-RPC transport:
accepted Turn, lost response, restart, recovery, fresh successor Thread, and stale
callbacks. Cross-adapter tests use the same real Core and durable recovery receipt.

Wave 2 ownership/lifecycle changes, TaskContext removal, immutable Task definitions,
DevelopmentCycle and Git lifecycle are outside this change.
