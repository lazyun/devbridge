> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

# V0.1 final state semantics closure

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Starting HEAD: `42315bca77722d227077bffa2c03d667d546f024`.
Development branch: `dev_20260908_1913`.
This record covers F1–F4 only. The containing implementation commit is the
closure commit; its final SHA is reported with the delivery.

## F1: cancellation authority

GPT/Local option meanings and private Card `cancelEffect` distinguish
`DECLINE_ACTION` from `CANCEL_LEGACY_RUN`. Whole-Execution cancellation continues
through the existing exact `execution_cancel` protocol. Generic workflow context
interruption is an infrastructure failure, with the existing UNKNOWN/recovery
rules; it is not evidence of safe cancellation.

Production searches covered `CANCELED`, cancellation/cancel-Run paths, Approval
`CANCEL`, and Run/Task/Execution terminal mutations across `internal`, `cmd`, and
`desktop`. Runtime stores are the SQLite stores wired by `controlplane.Open`.

| Production path | Audited terminal authority |
|---|---|
| `approval.Service.Decide`, `ApprovalStore.Decide`, `cancelRunForApproval` | Workflow choices only decline the action. The direct cancellation writer is guarded to standalone legacy Runs. |
| `run.Service.Fail/FailWithUsage`, `RunStore.Update` | Workflow `CANCELED` requires the existing cancellation intent, latest Execution Run, epoch/claim, exact Thread/Turn, interrupted terminal, and quiesced finalization intent. |
| `finalizeCanceledExecutionTx` | Existing authoritative transaction applies the boundary, Run, Execution, Task, receipt, and claim release. Execution and Task CAS must each affect exactly one row; conflicts roll back. |
| `ExecutionProposalStore.Complete` | Cannot create a canceled outcome; it can read back a cancellation already committed by the authoritative transaction. |
| `TaskStore.Update` | Cannot cancel a Task owned by a pending/running Execution or retained Execution claim. Rejected and safely finalized history do not block standalone compatibility. |
| `agent_recovery_store.finalizeRecoveredRunTx` | Existing explicit recovery only completes a KNOWN_SAFE exact interrupted terminal with durable protocol evidence, using the same finalizer. |
| `RuntimeSupervisor.CancelRun`, legacy control/HTTP facade | Reject workflow-owned Runs; they cannot bypass exact Execution cancellation. |
| GPT, Local MCP, active Card | Both transports call the same cancellation core. Active Cards direct cancellation to chat; pending proposal rejection is not active runtime cancellation. |

Other matches are status readers/projections, Approval/Probe entity states, or
historical migration backfills. They do not provide another live workflow
cancellation terminal writer. Historical migrations were not changed.

Focused fixtures cover action-only Approval cancellation for five Approval
kinds, sibling preservation, no redelivery, safe Implementation/Verification
cancellation, ACK without terminal, failed transport join, success-wins,
cancel-wins, UNKNOWN and explicit recovery, stale Run/epoch/claim, response loss,
one logical interrupt, and atomic crash/Task-conflict rollback. Older Console
and storage fixtures that directly fabricated canceled workflow state were
updated to establish protocol facts or seed explicitly historical records.

## F2: request and receipt contract

| Decision | Input and returned guarantee | Retry/readback |
|---|---|---|
| Task confirmation | Stable `requestId`; `DURABLE_REQUEST_RECEIPT`; unchanged atomic exact receipt | Same request replays its durable result; changed request/payload conflicts return authoritative receipt evidence. CONFIRM creates the durable plan and one pending Execution without starting implementation. |
| Execution authorization | Exact proposal identity and option, no `requestId`; `AUTHORITATIVE_ENTITY_STATE` | A consumed/rejected subject is read without another runtime handoff. Its authorization/reservation does not prove backend startup. |
| Runtime Approval | Exact Approval identity and legal payload, no `requestId`; `AUTHORITATIVE_ENTITY_STATE` | A decided/delivered subject is read without redelivery; DECIDED, DELIVERED, superseded, and expired remain distinct. |
| ScopeExpansion | Legacy-only | Both legacy options are rejected with `SCOPE_EXPANSION_LEGACY_ONLY`; historical rows are not mutated. |

Non-Task results have no adapter-generated receipt, accepted/replay claim,
request binding, or resolution timestamp. `entityDecision` reports the exact
Decision identity, current subject state, recorded option when available,
already-resolved/conflict facts, and optional transition error. A changed subject
is a different Decision; there is no cross-subject caller request binding.
GPT and Local share the same entity resolvers. A post-commit error triggers only
exact subject readback, not another mutation. Uncertain runtime handoff or
delivery remains separate from the recorded choice.

Tests include real SQLite plus fresh GPT/Local HTTP MCP sessions, injected
post-commit response loss, a live controlled Approval backend, same-subject
readback, opposite-choice/identity/permission-payload conflicts, concurrent CAS,
supersession, absence of non-Task receipts, and unchanged Task receipt recovery.
Card tests also reject mismatched subjects and label CONSUMED as
“Execution authorized,” including when Run detail is unavailable.

## F3–F4: projections and catalog

Console reuses the existing Review focus DTO and durable evidence projection.
It displays frozen mode, summary, primary/supporting modules and paths;
authorization; inspection observed; independent successful, failed and
forbidden/denied counts; and the Bridge Host Tools observation boundary.
Missing counts display as unknown rather than zero. DB-reopen/HTTP tests and
bilingual UI tests prove that authorization and denied-only access are not
successful inspection. Review persistence and ownership were not changed.

ScopeExpansion is read-only legacy history in the canonical v0.1 architecture.
The stale public/private mutation promises were removed from canonical docs.
Two fresh production discoveries compare the complete model/private tool
catalog with the documented tables; neither `scope_expansion_propose` nor
`ui_scope_expansion_decide` is registered. No new action or state machine was
added.

## Validation

All final gates passed on the integrated implementation after correction of the
historical test fixtures.

| Gate | Result |
|---|---|
| Cancellation/Approval and generic terminal writer focused suites | PASS |
| Task and non-Task Decision focused suites, fresh transport parity | PASS |
| Review durable reopen/HTTP evidence projection | PASS |
| Fresh production tool catalog/canonical docs contract | PASS |
| Root `go test -count=1 ./...` | PASS |
| Root `go test -race -count=1 ./...` | PASS |
| Root `go vet ./...` | PASS |
| Core production `go build -trimpath ... ./cmd/...` | PASS, output in a disposable directory |
| Console and Decision Card JavaScript suites | PASS, 71 tests |
| Desktop frontend tests | PASS, 41 tests |
| Desktop frontend Vite production build | PASS |
| Desktop `go test -count=1 ./...` | PASS |
| Desktop `go test -race -count=1 ./...` | PASS |
| Desktop `go vet ./...` | PASS |
| Wails native production compilation and macOS app packaging | PASS in a disposable source copy |

The first concurrent full run identified four old fixtures that depended on the
closed cancellation bypass; those fixtures were corrected without relaxing the
production checks. Under concurrent gate load, two Desktop process probes hit
their existing time limits; an isolated full Desktop rerun passed. No Desktop
production or timeout changes were made. Wails emits its existing SDK
deprecation/binding diagnostics but compilation and packaging succeed.

Opt-in real-Codex/external E2E was not enabled, and this closure does not claim a
new real-Codex acceptance run. Deterministic runtime and fresh transport tests
are the evidence for this task. No Global Final Acceptance Re-review was run.

## Whole-diff review and disposition

Whole-diff review found no remaining P1/P2 within F1–F4. Task-created build
outputs, disposable source copies, and temporary caches were removed; existing
ignored user artifacts were preserved. Final Git state is verified at delivery.
The development worktree is retained; no push, Release, or workflow overview
Card work is part of this closure.
