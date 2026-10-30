# Upper Review authority and command replay

Schema v49 adds observation-bound Upper cancellation and recovery to the existing
canonical owner protocol. It does not introduce another workflow, cancellation
registry, or Coding Agent context. The v49 migration is additive source; upgrading
a running database is a separate deployment operation.

## Observation version

New Upper `operation_cancel` and `agent_operation_recover` commands must carry
the exact version returned with their shared action target:

```json
{
  "expectedUpperVersion": {
    "authorityEpoch": "runtime_observed_writer",
    "grantEpoch": "runtime_original_authorization",
    "grantGeneration": 1
  }
}
```

`authorityEpoch` identifies the Core writer when the action was observed.
`grantEpoch` identifies the existing Review/Run/claim/grant authorization; it can
be older than the writer when recovery or old-epoch cancellation is needed.
`grantGeneration` identifies that grant's authorization generation. A second
Core restart invalidates an unadmitted command even if the grant has not changed.
The `runtimeEpoch` on existing Upper file/report targets retains its grant-epoch
meaning.

Durable `Review.executorKind` selects this contract. Missing backend identifiers
do not select Upper. Core validates both epochs and generation in the write
transaction, along with Project, FeatureArea, Task, Review, Run, claim and grant
ownership. Missing or partial versions cannot authorize a new Upper command.
Coding commands retain their existing conversation and backend requirements.

Clients carry the entire target unchanged. Target comparison and request identity
include version values, not pointer addresses or JSON property order. An unknown
response retains the original request ID, payload and version. A refresh must
not substitute a newer version into that request; a new version requires a new
explicit intent and request ID. Retirement captures its own exact version and
uses this same command protocol.

## Lifecycle and file access

Recovery, grant activation, source-file authorization and successful report
submission require an available DISCOVERING or ACTIVE FeatureArea and its exact
ACTIVE Task in REVIEWING with the same current Upper Review. They also retain
the original workspace, baseline, scope, snapshot, epoch and generation fences.
RETIRING, RETIRED and DECOMPOSED do not reopen source-file access. Historical
reports and audit records remain readable because those reads grant no source
access.

The retirement transaction is the database linearization boundary. File I/O is
performed outside the writer transaction against observed, confined snapshot
facts. A short final writer transaction rechecks lifecycle, exact authorization
and the captured database bindings before granting access or committing content
evidence/report success. Failed final authorization does not return the read
content. Retirement, cancellation or recovery that commits in between prevents
the stale observation from committing authorized evidence or a successful result.
Content already legally returned before retirement is not retroactively revoked.

Recovery only records same-Review/same-Run reauthorization and returns
`grantState: PREPARING` with its new authorized version. It creates no Task, Run,
Execution or AgentConversation and does not prepare the workspace. Explicit
`review_access_prepare` reuses the existing Core Prepare operation with an exact
Upper file target (`runtimeEpoch` and `grantGeneration`); it continues the durable
PREPARING stage, does not acquire another generation, and never fills in a newer
target. Prepare must recheck lifecycle and the unchanged snapshot before
activation. `review_access_get` remains a pure read. Replaying a completed recovery receipt
does not execute Prepare or imply that the grant is still ACTIVE.
The original start coordinator only prepares the initial generation (1), with
its original runtime target checked before workspace/Run changes and again
before grant activation. An old start request cannot pick up a recovered
generation from the mutable Run and silently activate it.

## Cancellation and convergence

Upper has no Coding backend to interrupt, but has durable file/report authority
to revoke. Current and old grant epochs use the same exact-owner cancellation
transaction, without first rebinding an old grant to the current epoch. It binds
the cancellation intent, cancels the original Run, terminalizes the exact grant
(including RECOVERY_REQUIRED), releases only the matching claim, and returns the
still-current ACTIVE Task from REVIEWING to REVIEW_REQUIRED. It does not cancel
the Task; retirement's own convergence terminalizes Task intent separately.

Preparation, reading and waiting-for-report states require the full durable
owner and grant combination. COLLECTING or stranded FINALIZING cannot override
an existing successful report. A report that completed first remains immutable;
canceling that completed operation returns its terminal fact. Cancellation that
completed first fences late Prepare, content evidence and report submission.
`NO_BACKEND` describes the absence of a Coding backend, not physical termination
of an external process.

## Three request categories

| Category | Authorization and effects |
| --- | --- |
| New, without a durable admission | Check current authority, observed grant version, exact owner and operation-specific lifecycle/state before admission. |
| Admitted but pending | Match the original normalized payload and grant version; converge only that intent. A later writer can finish old authority revocation without treating it as a new authorization. Recovery cannot bypass pending cancellation. |
| Completed receipt | Validate Project access and original payload, then return the recorded result. No generation update, Prepare, grant close, claim release or dispatch occurs. |

The existing receipt/intent tables store nullable `request_payload_json` and
`result_json`. New Upper receipts bind the complete normalized business payload,
including the observation version, and freeze their returned version/result.
Project and request ID remain the durable lookup scope; authenticated session
access is checked independently from the business payload.

Old-format rows retain NULL provenance. Completed old receipts use their original
durable contract; no version or hash is manufactured for them. Legacy pending
Upper cancellation can use its original intent epoch only when it still matches
the exact Run/claim/grant. Generation changes require epoch changes in the original
protocol, so it cannot converge onto a later reauthorization. Historical grant
inconsistencies are not repaired by reads or completed receipt replay. Any repair
of existing data requires a separate explicit procedure.

If an older implementation already reauthorized a grant across an unresolved
legacy cancellation, its original intent epoch no longer matches the grant.
Startup/replay reports that conflict rather than closing the newer authorization
or rewriting the historical intent. This is a distinct historical inconsistency;
ordinary pending cancellation across an authority-only restart still converges.

These are source contracts. Static inspection and formatting do not demonstrate
runtime cancellation, recovery, crash handling, concurrency or replay acceptance.
