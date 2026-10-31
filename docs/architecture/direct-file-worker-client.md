# Single-run, host-neutral DIRECT_FILE consumer

`internal/fileworker` consumes the existing [file pull-worker protocol](direct-file-pull-worker.md)
through an injected, already-connected MCP transport and trusted implementation
callback. It is not a dot runtime, generic shell, credential manager, model
launcher, filesystem sandbox or authorization issuer. No Core behavior, schema
migration, backend selection or confirmation rule is changed.

## Exact starting authority

The caller must already possess the original approved and separately started
v2 DIRECT_FILE target: Session, FeatureArea, Task, immutable definition revision,
approval decision, workflow, runtime epoch and grant generation. It supplies that
full `directfile.WorkerClaimInput`, stable result request ID and explicit
`VerificationMode` (`NONE` or `RUN`) in `Request`. IDs must be retained before
calling `Run`; the client generates none and never discovers a latest target.
Worker labels remain correlation under authenticated Session scope, not proof of
a dot identity, model, cloud thread or process.

The required mode is an acknowledgement of the existing frozen requirement,
not permission to select or override it. A mismatch refuses the delivered input.
**RUN result submission can dispatch the previously confirmed real verification
backend.** Do not treat this consumer as inherently model-free. NONE requires the
actual approved explicit waiver; it is never a fallback for unavailable RUN.

`New(mcp, request, implementer)` returns a one-use consumer. `Run(ctx)` requires a
bounded context; even failed preflight consumes that local invocation. Concurrent
or repeated Run calls return `ErrAlreadyRun`, never another callback. The
caller-owned transport must not resend business requests automatically; SDK
Streamable HTTP clients should set `MaxRetries: -1`. The callback is trusted and
must honor context cancellation; the package cannot stop arbitrary host code.

## Schema and delivery checks

Before any business call, bounded paginated `tools/list` must expose compatible
closed schemas for `file_worker_claim`, `file_worker_get`, `file_read`,
`file_list`, `file_write` and `file_implementation_result_submit`. Duplicate tools,
repeated cursors, missing worker fences and malformed contracts fail closed.
Schema resolution uses no remote loader. Local representative inputs are schema
probes only, never submitted as work or reports.

The current REPLACE public schema shares CREATE's content branch and does not
itself require `expectedDigest`. The client still requires a valid observed
source digest for REPLACE, DELETE and MOVE, as Core does. It does not claim the
advertised schema provides a stronger guarantee than it actually does.

The exact claim is invoked once. Only a first `DISPATCH_INTENT`, explicit
`replay=false`, and valid executable payload can invoke the callback. Validation
covers the original target/worker/request, raw instruction digest, canonical
`task.EncodeDefinition` digest, typed payload JSON digest, v2 DIRECT_FILE
work specification, matching verification specification, ACTIVE grant and complete
READY member set. The workset digest incorporates private SQL bindings and is
preserved as opaque proof, not reconstructed from public DTOs.

Replays/readback must contain no executable payload. Neither a missing reply nor
a new client permits executing a saved payload, changing IDs, or adopting another
workflow. Delivery copies are detached from private target and result baselines.

## Scoped file effects

The callback receives `Files.Read/List(ctx, memberID, path)` and
`Files.Write(ctx, Mutation)`. Only frozen members can be selected. Write actions
are exactly CREATE, REPLACE, DELETE and MOVE; MOVE is same-member only. Every
mutation requires its own caller-owned request ID, which must be persisted
before calling Write. No append, patch, upsert, command or arbitrary MCP method
is exposed.

The client fixes the original Session/workflow/epoch/generation/member and worker
claim on outgoing operations. It checks canonical relative paths, protected Git
metadata, write scope, bounded UTF-8/base64 content, action-specific fields and
observed source digests before dispatch. `workflowComplete` is never supplied:
v2 authority is closed only by the existing formal result operation. Core
continues to enforce live authority, containment, symlinks and effect journaling.

All callback file operations are serialized. Used mutation IDs are refused
locally; they are never resent, even if a previous reply succeeded. Once a
mutation transport call is attempted, any error, missing/invalid reply or panic
permanently latches `MUTATION_UNKNOWN`. Further file calls and formal result
submission are blocked, **even if the callback catches or ignores the error**.
An error is not proof of no filesystem effect or rollback. No automatic replay,
forensic reconciliation, cancellation or successor is attempted.

Plain DIRECT_FILE file replies currently omit the member/provenance/evidence
fields supplied by UPPER Review reads. The client validates returned path,
content digest/size and action outcome, and rejects contradictory optional
metadata; it does not invent absent provenance. The frozen claim and formal
member result do expose workspace/worktree/start-snapshot facts and are checked.
Request binding and the authenticated Core remain the file identity authority.
A local Git diff in integration tests is only an independent test oracle, not a
new public `file_diff` endpoint.

The callback capability is canceled, revoked and all in-flight calls joined on
callback return. This is API confinement, not proof that a host process died.
The result contains detached `MutationRecord` inputs and validated responses for
caller-owned diagnosis/journaling, not a durable client recovery journal or retry
permission. Unknown records retain their exact dispatched input and no claimed
successful response.

## Formal result and separate verification

The callback returns only its actual `Completion{Summary, VerificationNotes}`
or an error. Callback error/panic produces no synthesized formal result and does
not close Core's grant. An unknown mutation takes precedence over callback success
or failure. Member snapshots and changed paths are always observed by Core, not
provided as claimed evidence by the callback.

With no unknown operation, a valid completion is frozen into the original
`directfile.ImplementationResultInput` and submitted once. The receipt must match
original identities, authored summary/notes, v2 fences, complete members and
frozen workspace/worktree/start facts. This formal receipt has **no public full
payload digest**; the consumer does not invent one. The integration test checks
the exact full-input hash against SQLite only as a read-only test oracle.

RESULT_RECORDED means the file result was recorded, not verification success,
acceptance or Git delivery. Core's result commit/closure and later RUN handoff
are separate: `READY` plus `VerificationStartError` is a recorded result whose
handoff needs attention. `RUNNING` identifies the existing Execution/Run;
`SUCCEEDED` is transport completion, not proof that all engineering checks passed.
The consumer never calls execution start, verification continuation or acceptance.

| State | Observation, never retry permission |
| --- | --- |
| NOT_DISPATCHED | This invocation stopped before claim; does not prove no historical claim exists. |
| CLAIM_UNKNOWN | Claim reply unproven or duplicate local Run refused. |
| DELIVERY_UNKNOWN | Original delivery exists without a proven formal result. |
| CALLBACK_FAILED | Callback failed; no result fabricated or authority revoked. |
| MUTATION_UNKNOWN | Attempted file effect unproven; all further file calls/result blocked. |
| RESULT_NOT_SUBMITTED | Local completion validation/context prevented submission. |
| RESULT_UNKNOWN | Result call may have committed and possibly started verification. |
| RESULT_RECORDED | Exact Core formal result validated; inspect separate verification facts. |

`Observe(ctx)` requires the full compatible tool catalog at preflight even though
its only business call is the original `file_worker_get`.
It never refreshes grants or retries any effect/handoff. Immutable claim and file
result facts are pinned on first successful observation. Verification status,
Run projection, acceptance and commit-error fields may evolve through other
existing Core actors and are reported as observed, not actions of this client.
A cold client with only original IDs cannot independently reconstruct missing
callback inputs or private member bindings; it validates available original
identity and structured result facts without claiming its own callback ran.

## Verification and deployment limits

```sh
go test ./internal/fileworker -count=1
go test ./internal/controlapi -run '^TestFileWorkerClient' -count=1
go test -race ./internal/fileworker -count=1
go test -race ./internal/controlapi -run '^TestFileWorkerClient' -count=1
```

Unit fixtures are explicitly synthetic. Actual Local MCP/Core integration uses
genuine admission/confirmation, real two-member reads and all four mutation kinds,
SQLite effect/result records, response loss after completed handlers, and cold
original-identity readback. The RUN fixture explicitly substitutes a deterministic
test-only native-protocol subprocess and fixture qualification; it actually runs
bounded `test -f same.txt` checks and follows the real Core verification
Execution/Run. It does not qualify a production native artifact or call a model.
All cases retain separate acceptance and source Git delivery boundaries.

No live connector deployment, credentials, model configuration, cloud task,
event wakeup or actual dot callback integration is installed here. The previously
observed connector catalog lacked worker tools; schema preflight refuses such a
surface. Focused client tests do not clear existing full-root native/socket
fixture limitations or establish end-to-end live dot acceptance.

### Retained authoring evidence

An initial integration compile ran before the package was complete and failed;
it executed no Core scenario. The first ordinary run then rejected all calls at
preflight because the new consumer assumed REPLACE's schema required a digest.
The second run admitted claims but refused valid v2 payloads because the new
consumer invoked the legacy WorkSpec validator. Both client assumptions were
corrected without modifying Core. Failed logs remain failures, not qualifications.
Later ordinary unit and real Core runs passed; final frozen qualification is
recorded separately after independent review.

A preliminary integration race attempt has no terminal result or after-source
check in its retained log. It is incomplete evidence, not a race pass.

### Frozen-source qualification (2026-10-31)

- Consumer unit suite: **11 top-level tests and 29 named subcases**; ordinary
  **22.540s** and race **161.012s**, both exit 0. This includes swallowed mutation
  errors, transport panic, in-flight cancellation/join, returned-data isolation,
  immutable observation baselines and evolving verification/acceptance projections.
- Actual HTTP/Core suite: five new admitted workflows (normal, three distinct
  response-loss cases, and RUN verification handoff), plus two existing worker
  regressions in the ordinary invocation: **18.409s**, exit 0. The separate new
  client integration race invocation passed in **258.137s**, exit 0. Neither
  race log reports a data race.
- All six tested Go source hashes matched before and after the ordinary/race
  runs. Root `go build -p 2 ./...`, `go vet -p 2 ./...`, relative documentation
  links and whitespace checks passed. Independent source/document review found
  no blocking issue; these focused results do not qualify a live dot connector,
  production native artifact, full root test suite, acceptance or merge.

The separate [composition gate](../testing/worker-client-composition.md) consumes
an actual UPPER client's proposal through independent approval/start boundaries
and this file consumer over production GPT HTTP; it is not live dot acceptance.
