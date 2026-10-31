# Pull-coordinated Direct File implementation

This extends external-assistant participation, including dot, beyond read-only
Upper Review to **actual file implementation through Core's existing file API**.
It is not an `AgentID=dot` runtime, a cloud-thread launcher, a shell service or a
synthetic implementation Run. It requires an already-approved immutable
`DIRECT_FILE` v2 definition and a separately confirmed, successfully started
`START_DIRECT_FILE` workflow. A `CODING_AGENT` approval cannot be reinterpreted.

The external assistant needs access only to the existing authenticated MCP
surface. Core resolves and operates on its pinned canonical worktrees; the
assistant does not need the same machine or shared absolute filesystem paths.

## Exact authority and one delivery

Read the exact Task and its active `directFile` state. `file_worker_claim` names
its original AISession, FeatureArea, Task, definition revision, approval decision,
workflow, protocol version 2, runtime epoch and grant generation, plus a fresh
request ID and stable worker label. The adapter-authenticated caller scope must
match the original Session. The label is correlation, not proof of a dot account,
model, cloud thread or process.

Enrollment is allowed only before any mutation receipt or formal file result.
The claim takes the same operation lock as file mutations and result submission,
then validates the current authority inside the writer transaction. Thus a
concurrent legacy mutation either wins first and prevents enrollment, or loses
to enrollment and must satisfy its worker fence before effects.

The immutable receipt binds the full frozen definition, original instruction,
ordered complete member set and scope, pinned worktree/root identities, initial
snapshots and grant version. Core commits `DISPATCH_INTENT` before returning the
first `executionPayload`. Its `instruction` preserves the approved
`WorkSpec.CodexInstruction` bytes; the complete immutable definition (including goals/constraints), bounded file
authority and the frozen verification specification are separate context, never
concatenated text.

Only this first successful response includes executable input. Identical retry
and `file_worker_get` return the original receipt and known result, never another
payload. A lost response means `OUTCOME_UNKNOWN`; changing request or worker IDs,
using a newer workflow or executing from another read is not a safe retry. No
lease expiry, reassignment, notification-based authority, exactly-once execution
or proof of model consumption is claimed.

## File effects and formal results

All existing file containment, symlink, size, per-member write scope, source
digest and effect-journal rules remain authoritative. On an enrolled workflow,
every `file_write` (CREATE, REPLACE, DELETE and MOVE) and fresh
`file_implementation_result_submit` must also carry:

```json
{"workerClaim":{"requestId":"original-delivery-request","workerId":"original-worker-label"}}
```

This is not extra authority. Session/grant reads remain available under their
original rules; worker identity is not an exclusive-reader mechanism. Worker
fields on unsupported legacy or standalone mutation routes fail closed. Omitting
them on unenrolled historical workflows preserves old request serialization and
receipt hashes.

Mutation intent remains durable before filesystem effects. Exact completed
mutation receipts can replay after closure or restart, authenticating the original
worker and caller before current-lifecycle checks. An exact durable post-effect
journal can prove a previously pending effect: replay may complete that old
receipt without running the filesystem callback or refreshing authority. Missing
proof stays unknown; no blind repeated write or rollback occurs.

Formal result submission observes the complete pinned member set and requires
resolved, matching mutation evidence. Its result, grant closure and worker
completion commit atomically. `RESULT_RECORDED` describes a formal file result,
not a successful command run or user acceptance. Result readback projects the
actual separately recorded verification and acceptance facts; the delivery
receipt remains immutable.

## Required verification is not waived

For `DIRECT_FILE` + `RUN`, the existing `START_DIRECT_FILE` confirmation freezes
the verification backend and runtime selection. Formal file result submission
retains the existing separate read-only verification-only Execution/Run and
handoff. Worker notes never count as executed checks. Missing command evidence
still blocks acceptance. Neither a worker claim nor a result can select a new
verification backend, replace required checks or silently change `RUN` to
`NONE`. Existing explicit user waivers retain their exact separate meaning.

The file implementation itself creates no IMPLEMENTATION Run, backend thread or
turn. Execution, engineering analysis, user acceptance and Git delivery remain
separate existing authorities. A worker does not receive acceptance authority.

## Cancellation, restart and uncertainty

There is one immutable delivery per workflow. Same-workflow recovery of an
enrolled worker is unsupported and refused both during fresh confirmation
preparation and by the writer before grant-generation changes. Historical
completed confirmation/result replay remains distinct from fresh recovery.

After restart, the original grant remains `RECOVERY_REQUIRED`; readback does not
refresh it or resend work. Guidance offers the existing exact cancellation only
when cancellation is actually available. Another selected Task must not inherit
this workflow's cancellation or recovery action. Historical worker receipt
responses do not attach current/successor workflow guidance.

Cancellation revokes Core file/report authority, preserves existing effects and
unknown delivery evidence, and does **not** prove an external assistant stopped.
If a mutation is pending/unknown without a valid effect journal, existing safety
rules can block cancellation as well as result submission. No automatic recovery
or forensic reconciliation is added here; that workflow remains blocked pending
explicit supported investigation. The UI must not suggest an unavailable
recovery or cancellation. A separately authorized successor is never automatic.

## Verification and deployment boundary

Deterministic checks exercise production SQLite admission, concurrent claims,
all four real file mutation kinds, scope/claim mismatches, claim-versus-legacy
mutation serialization, journal fault/restart proof, atomic result rollback,
legacy absent-field hashes, exact historical readback and recovery refusal.
Actual Local MCP HTTP tests exercise admitted multi-member writes and results,
restart guidance, and cancellation without creating a model Run or acceptance.
The existing native protocol fixture tests also exercise separately required
verification, including missing command evidence that must not be accepted.
These are Core/protocol tests, not live dot or model acceptance.

Development and deterministic tests do not replace a live connector, change host
permissions, install an event subscription, create cloud tasks or contact a live
provider/model. Claim/get never launch models. Production formal-result submission
retains the existing real backend handoff when required RUN verification was
explicitly authorized; it is not a model-free operation. Event wakeups remain
hints only. General dot command execution, supported
Go-to-cloud dispatch, cloud interruption and persistent host continuation remain
outside this file-based integration.

The host-neutral [single-run file consumer](direct-file-worker-client.md) adds
exact-target schema preflight, callback-scoped file APIs, an irreversible
mutation-uncertainty latch and explicit result/verification observation. It does
not install a live dot host or bypass the separate verification/acceptance gates.
