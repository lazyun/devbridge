# WebUI access and action targets

WebUI local-control requests and AI Session requests have different actors. A
Task's creating Session, a source Review's Session, and a historical receipt's
Session are provenance. They do not identify the current browser operator.
Project reads must never select the first Session in that Project or retry
Sessions until one passes ownership checks.

## Local transport boundary

The current deployment has a single local-control operator realm, not a login
system or per-browser RBAC. `cmd/devbridge` validates an explicit loopback listen
address before opening the HTTP listener. Its GPT/Local MCP adapters are sibling
routes of the Bridge HTTP handler, not descendants of WebUI operator admission.

`localoperator.Ingress` is constructed by the local Bridge server from its
deployment data-root identity. Admission requires the actual `net/http`
`LocalAddrContextKey` to identify a loopback listener, a loopback peer, a
loopback/localhost Host at that listener's port, and same-origin browser
Origin/Fetch-Metadata when supplied. A native local client may omit Origin.
Client IP, Origin, lack of caller scope, and component visibility alone do not
establish this actor. A forwarded external UI is not implicitly supported.

Admission supplies an opaque context with a private seal. Browser JSON, query
parameters and headers cannot set its principal, Project, Task, or action.
The authorized internal adapter binds the exact Project, then an exact Task or FeatureArea and
action; rebinding an existing different target fails. Project existence and
business-object ownership are still checked through the original Core stores.
This operator realm can use the local deployment's Projects; no per-Project ACL
or OS-login identity is invented.

The entry wrapper applies to `/ui/api/` and `/v1/`. Static UI/readiness provides
no operator capability. The MCP handlers do not receive local admission, and
Session-specific APIs do not interpret an operator context as Session ownership.
Admission refusal uses the existing safe `code`/`message` response convention.

## Read identity and shared presentation

Session entry points first validate ownership of the actual Session, then its
Project and exact object. The strict caller-facing lookup is separate from the
existing internal/background Session lookup. No durable caller scope is cleared
or replaced and no owner value is copied into `WithCallerScope`.

The local adapter authorizes a Project and reuses the shared Task/FeatureArea
presentation implementation through a restricted operator facade. It does not
create an AI Session, synthesize one, or call a private grant reader as another
Session. Project business facts remain readable with zero Sessions, or with any
number/order of Sessions owned by different AI callers.

Fact permission and action eligibility are distinct. A denied Project read
returns a refusal before exposing business facts. An authorized business read
keeps those facts when an action requires a separate identity or its eligibility
cannot be read. The shared actions status/error and per-action unavailable
reason express that difference. Unknown eligibility must never be promoted to
available, and no inbox or client union may restore a restricted action.

Session-private confirmation intents, binding handles, Upper read targets,
Direct File grant/file capabilities, and restricted diagnostics keep their
original ownership contracts. Project-visible status does not transfer those
capabilities. Active file authorization must still constrain conflicting local
operations even when private grant details are not returned.

## Commands, provenance, and receipts

Local actions require the exact trusted Project/Task/action context and still
delegate to canonical commands. Their final transactions remain responsible for
current targets, revisions, lifecycle, claim/epoch, UNKNOWN/quiescence, file
scope and atomic finalization. A projected available action is not a token
authorizing a later stale request.

Local request IDs use a domain-separated hash of the deployment operator
principal, Project, FeatureArea, Task, action and original stable request ID. Canonical
payload conflict checks remain in place. This namespace does not grant access
to an AI Session's receipt. Where a canonical receipt records an actor, local
operator and AI Session sources are recorded independently and cannot both be
asserted. Historical source rows are preserved, not reattributed.

Commands whose original persistence still requires an acting AI Session cannot
be made local by passing a source/creator Session or an empty scope. They must
have an explicit actor-aware canonical path before being enabled. Until then,
the shared capability and submission both report the specific identity
limitation. Read recovery alone does not establish that all WebUI commands are
implemented, and is insufficient for the combined static acceptance label.

The implemented local command adapters are exact Task/Review/Execution
cancellation, recovery, accepted-Task commit retry, FeatureArea commit, idle
FeatureArea context reset, and exact live runtime-request resolution. Each
reuses its original canonical owner. Commit/retry/reset retain the original
Project/object receipt contract; cancellation/recovery additionally persist the
separate operator source. They do not create a substitute Session.

Review decisions and acceptance, starts, supplemental Review requests,
technical implementation retry, Task creation and retirement remain explicitly
unavailable to the local operator until their acting-Session persistence and
background propagation have a distinct operator contract. Their exact shared
targets remain visible with `OPERATOR_ACTION_IDENTITY_UNAVAILABLE`. This is a
concrete remaining product blocker, not full WebUI command restoration.

Migration V57 is source only: the existing cancellation table requires an AI
Session foreign key and cannot accurately record a local operator. V57 retains
old rows and indexes/triggers, makes the Session nullable, adds a mutually
exclusive immutable operator source, and adds operator source to original
recovery receipts. It introduces no second receipt store. The migration has
not been applied or SQL-executed during this task. Its startup/data-integrity
acceptance remains pending. The closed MCP cancellation receipt schema accepts
the optional operator source; MCP inputs cannot supply that internal actor.

Upper report submission, Direct File file/result operations, Session binding,
and confirmation-owner operations continue through their own Session authority.
Cancellation/recovery does not confer those rights or restore historical grants
merely because an old receipt can be read.

## Exact supplemental Review target schema

The shared ActionTarget output field is `expectedReviewRequest`; the canonical
supplemental command input remains `expectedTarget`. Do not rename either to
make transport DTOs appear identical.

The single existing `TaskReviewRequestTargetSchema` declares a closed object
with five required string fields:

- `phase` (the original nonempty phase contract);
- `taskDefinitionRevisionId`;
- `currentReviewId`;
- `currentExecutionId`;
- `pendingDecisionId`.

The four ID strings permit explicit empty values for absent objects. Omitted,
null, nonstring, or extra fields are rejected. The DTO keeps empty values during
JSON encoding and rejects malformed input during its exact-field decode.
The canonical predicate still checks the actual phase and all five fields;
accepting a string in JSON Schema does not make an unknown phase actionable.

ActionTarget includes this nested field only when applicable, so unrelated
actions are not forced to supply it. Both compact MCP schema inference and the
Card/Task/FeatureArea presentation schema inference use the same target-type
overrides. Workspace capabilities, Task actions, and pending-option targets
therefore share the original nested contract. External and Local registration
retain SDK output validation.

Copies, deduplication, payload hashing and exact comparisons retain all five
fields. Card comparison treats the nested target structurally, rather than
converting two different objects to the same `[object Object]` string.
`expectedUpperVersion` remains a separate independent contract.

## Error evidence and acceptance

The reported `authenticated caller does not own this Session` text has a
reachable production chain: `taskFileCaller` → Direct File TaskStatus → shared
Task presentation → old first-Session console read → HTTP `message` → WebUI
read-failure container. This establishes the source defect, not a captured
browser failure endpoint, browser identity, running instance or deployment.

Disk schema correctness likewise does not establish that a connector has
reloaded tools/list or that a hosted response passes validation. Users must
separately load the changed version and verify original Project/Task pages,
AI caller isolation, supported local command identity/receipts, full MCP
expectedReviewRequest output, stale targets, duplicate requests and revoked
permissions. No data reset, substitute Session or cache clearing is part of
the repair.

Keep `WEBUI_READ_AND_COMMAND_HOST_ACCEPTANCE_PENDING` and
`MCP_ACTION_TARGET_OUTPUT_ACCEPTANCE_PENDING`. Existing confirmation, Codex
dispatch, file-handoff and Verification runtime acceptance are unchanged.
