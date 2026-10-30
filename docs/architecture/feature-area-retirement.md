# Durable FeatureArea retirement

Current Task/Execution authority follows the [final v0.1 model](task-execution-v0.1.md).

Current identity ontology is defined by [Task → AgentExecution → Run](agent-execution-ownership.md)
(schema v46). Task is the Upper LLM work definition, Execution the universal agent
work envelope, and Run a backend attempt. ExecutionProposal remains the
DEVELOPMENT authorization subtype; semantic owner and safety rules below continue
to apply. Earlier schema descriptions below are historical where they describe
Task → Run or an ExecutionProposal-only Execution façade.

Schema v46 retains the durable FeatureArea retirement and canonical cancellation contract.
RETIRE is a durable FeatureArea-wide stop intent. A single explicit user action
commits `DISCOVERING / ACTIVE → RETIRING`; the write-side coordinator then reaches
`RETIRED` automatically. DECOMPOSED and legacy read-only objects are immutable.
DISCOVERING, active work, pending Decisions and UNKNOWN claims are not blockers.

## Commit and admission ordering

`FeatureAreaStore.beginRetirement` holds one short SQLite write transaction:
Project/FeatureArea authorization and request conflict checks, immutable
`feature_area_retirements` identity/session/time, RETIRING lifecycle update,
status event and lifecycle receipt. The commit happens before the service wakes
`retirement.Coordinator`. No backend cancellation occurs inside this transaction.
Response-loss replay returns the current state of this same intent, including
retries with another request ID. Existing v36 RETIRED records retain their facts
without fabricated intent metadata.

Admission and retirement serialize at the authoritative SQLite write boundary.
If admission commits first, its visible Task/current owner joins convergence. If
retirement commits first, new engineering advancement fails closed. Existing
claims remain until canonical finalization or orphan disposition safely releases
them. Different FeatureAreas retain separate claims; no backend wait holds a
SQLite transaction and there is no Project scheduler/global retirement lock.

## Work convergence

The coordinator starts from visible FEATURE_AREA-scoped Tasks and resolves their
exact current owner in one snapshot. It also includes terminal Tasks retaining a
current claim or unfinished aggregate. PROJECT-scope INSPECTION is untouched.

| Current work | Canonical owner | Completion |
| --- | --- | --- |
| Unlaunched Development / pending definition decision | TASK | CANCELED, NO_BACKEND; reject pending plan without confirmation receipt |
| Pending/confirmed Execution without Run | EXECUTION | CANCELED, NO_BACKEND; no fake Run, Thread, Turn or interrupt |
| Implementation | EXECUTION + exact Implementation Run | Existing durable cancellation/finalization protocol |
| Verification | EXECUTION + exact Verification Run | Implementation history preserved; only current Run canceled |
| Review | Task-owned REVIEW + exact Run | Converge the exact child operation; retirement separately closes semantic Task intent |

The coordinator calls `RuntimeSupervisor.CancelExecution`/schema-36 cancellation,
never generic Run cancellation. It reuses existing pending user cancellation
identity; otherwise deterministic area/owner/Run identity makes retries stable.
There are no retirement-specific child cancellation receipts. Snapshot changes
fail closed and are retried against fresh authoritative ownership.

Task CONFIRM/REVISE and Execution authorization lose authority immediately at
RETIRING. Owner cancellation rejects pending TaskPlan intent and expires pending
Approval state without synthetic decisions/confirmation receipts. An Approval
CANCEL continues to mean refusal of that action, not whole Execution cancellation.
Late decision callbacks cannot start work or revive canceled owners.

## Known terminal and UNKNOWN_ORPHANED

Known completion requires exact terminal/finalization facts and the existing
quiescence contract. Local process Close, interrupt ACK and claim release never
prove historical backend physical death. A terminal Run with a stranded aggregate
can cancel that aggregate using its recorded safe boundary, preserving Run history.

UNKNOWN_ORPHANED is also valid completion: the original UNKNOWN outcome and false/
unknown quiescence remain unchanged. One canonical orphan transaction permanently
revokes old write authority, quarantines workspace/context, converges the exact
child owner, writes disposition and releases the exact claim. The retirement
coordinator separately terminalizes the semantic Task under the committed
FeatureArea stop intent; child orphan recovery alone does not do so. No successor engineering
context is created for RETIRING. Old epoch/Thread/Turn/Run callbacks and Host Tools
remain fenced. A descendant may still write its quarantined old workspace; it
cannot regain authoritative Bridge state. `RETIRED != every backend physically dead`.

## Automatic continuation and final commit

The coordinator wakes after accepted intent, at startup, and after owned runtime
terminal/finalization callbacks. Each accepted child cancellation is followed by a
completion check. A 100 ms internal reconciliation tick also observes external
claim release, orphan/finalization/recovery commits and restart gaps. All these are
write-side operations. Web/Card Refresh, history and presentation reads do not
advance retirement. Coordinator shutdown joins its loop before supervisor/DB close.

Crash before intent commit leaves no partial stop. Crash after intent, cancellation
intent, dispatch, orphan commit or last Task terminal resumes from durable state.
The canonical protocol consumes exact prior receipts/boundaries without repeating
interrupt or quarantine. The orphan transaction itself remains all-or-nothing.
Conflicting identity remains fenced rather than guessed.

## Shared presentation and retained history

Core retirement projection includes request ID/time, optional completion time,
server-authored summary, remaining Task count, engineering availability false and
optional retry diagnostic. Workflow, text/JSON, GPT, Local MCP and Console use it.
`can_retire` is true for normal DISCOVERING/ACTIVE, independent of Task/Review,
Decision, claim or recovery state; already closing/closed/immutable is unavailable.

Web acknowledges committed RETIRING without waiting for final completion. Lists
and drawer show 关闭中 and server stop progress, then 已关闭. Closing remains visible
in the active list; Closed/All retains completed history. Task rows retain semantic
status and phase, with cancellation progress shown separately; UNKNOWN_ORPHANED
is shown as Agent结果未知 · 已隔离.

## Migration and validation

Migration 37 rebuilds the FeatureArea lifecycle CHECK while preserving rows,
foreign keys, indexes and triggers, then installs immutable retirement metadata
and admission/completion guards. Migration replay is idempotent. No live database
editing is part of deployment or acceptance. See the
[closure evidence](../testing/feature-area-retirement-v2-closure.md) for deterministic
state/race/restart/fault, adapter/UI and isolated candidate runtime validation.
