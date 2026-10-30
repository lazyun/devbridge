# Review confirmation and result materialization (schema v85)

Coding Review completion has four separate facts. A terminal `Run` records the
runtime outcome. `runs.result_text` retains the exact raw model artifact. The
append-only `review_result_materializations` row records whether that artifact
became a formal result. The existing `REVIEW_APPROVAL:<reviewId>` decision is
the only user-confirmation authority.

`review_result_materializations.outcome` is `VALID`, `INVALID`, or
`SCOPE_CONFLICT`. Every row binds the exact Review, Run and raw SHA-256. A valid
row also stores canonical formal JSON and its SHA-256. A failed row stores a
bounded typed reason and never deletes or rewrites the raw result. Update and
delete triggers make the interpretation immutable. Reconciliation is
idempotent because one Review can have only one materialization row.

Only a valid current Review conclusion can create the existing approval
decision. `APPROVAL_READY` offers approve/start, re-review and abandon.
`FURTHER_REVIEW_REQUIRED` uses the same decision authority but offers only
re-review and abandon; approval is rejected transactionally. Invalid and
out-of-scope results remain in `REVIEWING` with `RESOLVE_REVIEW_RESULT` and no
decision.

The Core `Guidance` projection publishes the same machine facts to HTTP, MCP,
Console, Workbench, Desktop and Card adapters:

- `reviewRuntimeStatus`, `rawResultAvailable`
- `formalResultStatus`, `formalResultAvailable`, `materializationFailure`
- `reviewDisposition`
- `reviewConfirmationStatus`, `reviewConfirmation`
- `requiredAction`, `availableActions`

`reviewConfirmation` aliases `reviewApproval`; both serialize the same durable
decision and never form competing authorities. Clients may translate labels,
but do not infer eligibility from Review or Run display status.

For Coding work, `APPROVE_AND_START` resolves APPROVE with
`START_AUTHORIZED`. The single SQLite writer transaction creates the immutable
Task definition revision, one Execution, its frozen runtime/workset and the
durable execution start effect. After commit the existing continuation worker
consumes that effect. Process launch is intentionally outside the transaction;
failure converges through the existing recovery contract and never reopens the
Review confirmation.

REVISE is the product `REQUEST_RE_REVIEW` command. It preserves the completed
Review, consumes its Decision as `CHANGES_REQUESTED`, stores the exact bounded
feedback and hash, and atomically creates the next ordinal Review and reserved
Run on the same Task. The Task moves directly to `REVIEWING`; a durable accepted
start receipt is consumed by the existing continuation worker, so there is no
second `START_TASK_REVIEW` action. REJECT cancels the same Task without creating
a revision or Execution. Every write
revalidates current Task, current Review, terminal Run, formal result,
disposition, project ownership, runtime authority and exact receipt identity.
