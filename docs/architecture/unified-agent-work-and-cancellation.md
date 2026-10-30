# Unified Agent work and canonical cancellation (v46)

Current Task/Execution authority follows the [final v0.1 model](task-execution-v0.1.md).

Current identity ontology is defined by [Task → AgentExecution → Run](agent-execution-ownership.md)
(schema v46). Task is the Upper LLM work definition, Execution the universal agent
work envelope, and Run a backend attempt. ExecutionProposal remains the
DEVELOPMENT authorization subtype; semantic owner and safety rules below continue
to apply. Earlier schema descriptions below are historical where they describe
Task → Run or an ExecutionProposal-only Execution façade.

Engineering Review and Execution operations belong to the semantic Task.
Project Inspection uses its separate operation envelope. FeatureArea owns
engineering AgentConversation lineage; child operations do not own Task phase.

| Type | Scope | Owner and phases |
| --- | --- | --- |
| DEVELOPMENT | FEATURE_AREA | Task intent → Review → approval → Execution implementation / verification → Acceptance |
| INSPECTION | PROJECT | Visible envelope → ContextProbe → inspection Run; read-only Project discovery, no invented FeatureArea |

SQLite creates Task-owned Review and Project Inspection operations atomically with their exact owners, before
Run reservation. The Run insert trigger requires exact Task/type/scope/owner
binding and valid lifecycle. Engineering operations still reserve the exact
FeatureArea claim. ContextProbe starts a durable inspection Run before starting
its independent, read-only Codex transport, and records Thread/Turn under its
Run/epoch fence. It has no engineering Host Tools or authorization Decision.
Desktop availability/version checks that start no Thread/Turn create no Task.
CLI response/mutation smoke modes and test helpers are diagnostic paths, not
production work admission. Database reconciliation never fabricates Agent work.

The canonical Task `work` projection carries type, scope, phase, authoritative
status, exact owner, latest Run, Agent, token availability, update time,
cancellation state, backend disposition and read-only/provenance markers.
Console/Web include model and Development Branch provenance alongside these
facts. `work_list(kind="TASK")` pages semantic development Tasks only;
`work_list(kind="OPERATION")` pages Review, Development and Inspection operation
targets. GPT and Local MCP use the same scoped readers. A workspace entry
navigates engineering work and exact Project inspections.
Development workflow decisions continue to select Development Tasks only.

## One cancellation protocol

`operation_cancel(aiSessionId, ownerType, ownerId, runId?, requestId, reason?)`
uses the existing execution-cancellation store and supervisor. Its disjoint
executionId branch also accepts a copied durable envelope identity and delegates
to the same protocol and receipt table.
No adapter owns a second cancellation state machine. Generic Run cancellation
rejects every workflow-owned Run.

New Upper Review commands additionally require the shared target's
`expectedUpperVersion`. See the [v49 Upper protocol](upper-review-core-protocol.md)
for exact observation fencing, atomic old-epoch grant closure, and the separate
new-command, pending-convergence and completed-receipt contracts. The Coding
interrupt and UNKNOWN rules below retain their existing meaning.

| Exact owner | Before Run | Active Run |
| --- | --- | --- |
| REVIEW | Cancel unlaunched envelope/Review | Exact interrupt, then known terminal or orphan convergence |
| EXECUTION | Close unlaunched authorization and cancel its outcome; retain semantic Task | Implementation or verification: exact latest Run only |
| TASK | Unlaunched Development with no active owner/backend | Not a bypass for owned work |

An omitted Run is accepted only when the durable owner has no Run/claim. This
path records `NO_BACKEND`, no interrupt dispatch and no fabricated Run, Turn or
confirmation. Pending TaskPlan decisions become rejected. Existing confirmation
receipts stay historical; canceled owners cannot consume authorization. Canceled
Tasks cannot confirm/revise or create new attempts. Approval `CANCEL` continues
to affect only its own Decision and is never whole-owner cancellation.

Intent commits before any external action. One durable dispatch marker binds
Project, owner, Task, Run, epoch, claim, conversation, Thread and Turn before
`turn/interrupt`. ACK is not terminal. Replay validates the original target and
reason, including after response loss/restart, without resending a destructive
signal. Known completion wins if it commits first (`CANCEL_NOT_APPLIED`). Known
interrupted finalization atomically converges the exact Run and child owner and
releases the claim. REVIEW/EXECUTION cancellation does not automatically terminalize
the semantic Task. Phase transitions remain Core-owned: closing the exact current
UPPER_LLM Review grant can return an ACTIVE Task from REVIEWING to REVIEW_REQUIRED.
Only an authorized TASK cancellation or Task-level
decision, including FeatureArea retirement convergence, terminalizes Task intent.

## UNKNOWN is independent of product terminal

`Task CANCELED != backend proven dead`.
`Owner terminal != historical backend QUIESCENT`.
`UNKNOWN_ORPHANED != QUIESCENT`.

`UNKNOWN_ORPHANED` means physical outcome/quiescence remain unknown while old
Bridge write authority is permanently revoked. The transaction preserves the
original UNKNOWN boundary, converges the exact child operation, expires pending approval
work, quarantines the old workspace, ends the old conversation, creates a fresh
threadless successor only for a usable FeatureArea, appends exact immutable
`agent_orphan_dispositions`, then releases the exact old claim. Existing terminal
Run failures/results and revision/evidence history are preserved. Quarantined
workspaces cannot be dispatched again. New work uses a new controlled workspace.

Claim release now has two legal paths: known safe terminal with finalized
quiescence, or permanently fenced/quarantined UNKNOWN with a terminal child owner
and durable orphan disposition. Release means development authority ended.
`Close()==nil`, epoch rotation, quarantine, claim deletion and recovery receipts
are never SafeTerminationEvidence. Only actual exact observations support the
known path. No new generic boolean “backend joined” proof input is introduced.
The descendant-writer regression deliberately demonstrates writes after direct
app-server Close, keeping this distinction executable.

## Progression after permanent orphan recovery

Task Acceptance, automatic Task commit, FeatureArea submission and workflow
guidance share Core's current unresolved runtime authority predicate. Historical
UNKNOWN/CONFLICT recovery or non-quiesced finalization remains blocking until
the exact operation has a permanent orphan disposition, its old authority is
fenced, its conversation has ended, its workspace is quarantined and detached
from canonical ownership, and its exact claim is gone. An incomplete revocation
cannot authorize progression.

Once those durable facts hold, the old UNKNOWN history no longer represents a
current writer. New canonical work may proceed, including otherwise legal Task
Acceptance and Git delivery, while immutable boundary, finalization, recovery
and orphan records remain intact. A current claim, unresolved authority or
active Direct File grant still blocks conflicting work. Restart reads the same
durable facts; old callbacks remain permanently unauthorized. Neither this
business readiness nor UNKNOWN_ORPHANED proves physical backend death.

## Crash, migration and history

Every revocation/quarantine/disposition/claim update is in one SQLite write
transaction. A crash cannot expose half-orphaned authority. Startup consumes
pending cancellation intents using recorded exact facts; UNKNOWN converges to
orphan without redispatch. Without a cancellation intent, explicit recovery of
an older fenced UNKNOWN operation uses the same orphan transaction. Conflicting
identity remains fail-closed and requires reconciliation. History is never
converted into a guessed successful backend result.

Schema 36 did not change the historical idle-only RETIRE behavior. Schema 37
supersedes that restriction with [durable FeatureArea retirement](feature-area-retirement.md),
reusing this canonical cancellation/orphan contract without new child receipts.
