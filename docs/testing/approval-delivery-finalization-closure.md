> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

# APPROVAL_DELIVERY_FINALIZATION_CLOSURE

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

P1-2 from v0.1 Global Final Acceptance Re-review.

- Source development branch: `dev_20260908_1913`.
- Expected baseline: `4815d64e678b1766e38b4886f428656a77a6b733`.
- Verified actual HEAD at worktree creation: `4815d64e678b1766e38b4886f428656a77a6b733`.
- Baseline difference: none; source worktree was clean.
- Implementation branch: `codex/approval-delivery-finalization-closure`.
- Worktree: `/private/tmp/devbridge-approval-delivery-finalization-closure`.

During delivery checks the source development branch had independently advanced
to `26f55da49454411ae12fba247ea0f2ff08f6979f`. This parallel closure retains its
verified worktree-creation baseline; no changes from that later HEAD were folded
into the implementation.

## Final behavior

A live Approval session retains the first typed delivery failure and signals
local turn cancellation. This cancellation wakes the Coordinator; it does not
certify a canceled Run or a quiesced backend. Failure closes admission and cancels
sibling resolver waits. `Wait` seals admission, joins all admitted decisions,
and returns the retained failure without consuming it. Admission and unregister
are synchronized so a request paused before admission cannot deliver after the
Coordinator has drained the session.

The Coordinator stops Approval request intake, drains decision delivery, closes
the transport, and records the existing finalization intent before submitting
the Run failure through `FailWithUsage` / `RunStore.Update`. If its caller is
canceled, it closes the transport to unblock delivery, then drains again without
the canceled caller context. Exact Execution cancellation also drains delivery
after closing the transport. Existing exact cancellation authority is preserved.

`approval_delivery_failed` and `approval_delivery_persistence_failed` retain
their distinct failure codes and the `approval` stage. The normal finalizer
applies the Run/token/boundary/intent transaction, and the existing product
completion path governs claim release. An exact observed terminal plus successful
transport join can produce KNOWN_ADVANCE. Missing terminal evidence or a failed
join remains UNKNOWN; UNKNOWN retains its claim and explicit recovery rules.
Intent or terminal-transaction failure cannot fall back to Approval's standalone
terminal writer.

With no live session, the durable human decision remains DECIDED and delivery
returns `ErrSessionNotLive`; no Run/Task failure, token finalization, boundary
advance, or claim release is fabricated. Existing restart/recovery owns the
stranded operation. A duplicate decision is not redelivered. Standalone legacy
Approval failure handling remains available. No schema, public decision action,
receipt model, or new recovery state machine was added.

## Regression evidence

`internal/storage/sqlite/approval_finalization_closure_test.go` uses real SQLite
stores and the production Coordinator with a gated synthetic backend:

- Backend delivery fails after the human decision is durable.
- MarkDelivered fails while an exact backend terminal is already observed.
- Caller cancellation races an admitted delivery.
- Transport Close is held: Run and Task remain active, token accounting remains
  pending, sibling Approval remains pending, and no finalization intent exists.
- Close fails: quiescence is false and UNKNOWN retains the claim.
- Finalization intent insert or application fails: terminal state/token/boundary
  changes do not escape the failed transaction, and the Task remains active.
- Successful failure finalization emits one Run terminal event, preserves the
  human choice and decision timestamp, and cancels outstanding Approvals through
  the existing terminal cleanup. The selected Approval remains undelivered;
  CANCELED here records supersession by Run failure, not a human CANCEL choice.
  Only a safely finalized claim is released after product completion.
  restart retains UNKNOWN ownership and stale-epoch failures are fenced.
- A decision paused before admission cannot cross a sealed drain; two admitted
  deliveries are both joined, sibling resolver cancellation is propagated, and
  the first failure survives repeat reads and a canceled waiter.

The previous SQLite test expecting workflow Approval delivery to terminalize a
Run directly now requires a still-active Run and the Coordinator failure signal.

## Validation and scope

| Check | Result |
|---|---|
| Root `go test ./... -count=1` | PASS |
| Approval / Execution / SQLite package suites | PASS |
| Approval / cancellation / finalization targeted race suites | PASS |
| Full `go test -race ./internal/approval ./internal/execution ./internal/storage/sqlite -count=1` | PASS |
| Final closure assertions under `-race` | PASS |
| Root `go vet ./...` | PASS |
| Production `go build -trimpath ... ./cmd/...` | PASS |
| `git diff --check` | PASS |

The default Go build cache was outside the writable sandbox, so checks use a disposable
`/private/tmp` cache. Root HTTP/MCP tests require local listening ports and were
rerun with that capability after the sandbox rejected binding a test port.

Whole-diff review covered the Service failure handoff and admission locks,
Coordinator drain/close/intent ordering, storage owner/epoch guard, and the
regression assertions. The new terminal-state assertion follows the existing
RunStore cleanup (CANCELED with the durable human decision retained), rather
than retaining the old standalone writer's DECIDED status after finalization.

This is a bounded P1-2 implementation and diff review, not a new global
architecture or acceptance re-review. No paid backend or opt-in real-Codex E2E
was run. No frontend/Desktop production code changed. The source development
branch is not merged or pushed by this task.
