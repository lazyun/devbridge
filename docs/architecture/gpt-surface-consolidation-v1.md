# GPT surface consolidation and collapsible card V1

Implementation baseline: repository `/Users/phoenix/Documents/devbridge`, branch
`dev_20260908_1913`, starting HEAD `ab371137e66dac9e6dd3d8af0f66cc61230f6621`.
The initial tracked working tree was clean. No live runtime, database, service,
browser, agent operation, commit, or deployment is authorized by this change.

## Frozen integration contract

The GPT catalog targets these 30 names (app-only registrations counted separately):
bridge_diagnostics, project_list, project_open, workspace_get, work_list,
operation_get, project_selection_show, workspace_show, details_show,
development_branch_create, development_branch_select, feature_area_create,
feature_area_lifecycle, task_create, task_review_start, decision_respond,
execution_start, operation_cancel, runtime_request_respond, agent_context_reset,
agent_operation_recover, feature_area_submit, task_commit_retry,
direct_file_enable, direct_file_disable, file_list, file_read, file_write,
document_create, repository_search.

Read contracts are shared by GPT and Local MCP. `workspace_get` accepts
`aiSessionId`, optional `featureAreaId`, `taskId`, and closed `include` values
`MODULES`, `BRANCHES`, `TASK`, `RUNTIME_CONFIG`, `NAVIGATION`.
`work_list` accepts `kind=TASK|OPERATION`, `aiSessionId`, optional
`featureAreaId`/`taskId`, and bounded `limit`/`offset`.
`operation_get` accepts `aiSessionId`, `target` copied from a projection
(`kind=REVIEW|EXECUTION|INSPECTION`, `operationId`, optional `ownerTaskId`,
`runId`), and closed includes `RESULT`, `EVIDENCE`, `ACTIVITY`,
`RUNTIME_REQUESTS`. These identities are existing Core identities.

Results distinguish `result`, `context`, `workflow`, `presentationHint`;
read partitions retain generation/source sequence, read errors, and stale
status and make no atomic-snapshot claim. Successful writes survive readback
failure with `refreshRequired`. Presentation does not grant authority.

`presentationHint.required` is a narrow caller-facing fact: it is true only
when an authoritative pending user decision or runtime request belongs to the
exact returned scope. It does not promise a host render or a model tool call.
`REVIEWING`, ordinary RETIRE/CANCEL/RECOVER capabilities, and a decision on an
unrelated Task do not make it true. A failed pending partition remains
unavailable/stale/refresh-required; it must not be converted into “no pending
decision” or a cached authorization. `required=false` still permits an ordinary
progress query or an explicit show.

`workspace_show` requires nonblank `aiSessionId` and `presentationRequestId`,
accepts `display=COLLAPSED|EXPANDED` (default COLLAPSED), optional asserted
`projectId`, `featureAreaId`, `taskId`, `decisionId`, and operation `target`.
It returns complete `aiSessionId`, `projectId`, `workspaceIdentity`,
`entryIdentity`, `presentationRequestId`, `display`, exact selected target,
and workspace initialization data. Same request and arguments replay complete
data; changed arguments conflict. Different requests may create different
entries. Host visibility remains UNCONFIRMED without host evidence.
Only `project_selection_show` permits unbound project selection. Old strict
card entries, if retained app-only, still require a valid Session.

App-only `dev_bridge_card_get` remains the production component's pure read
transport with exact optional FeatureArea/Task and no UI template. Local MCP
registers the shared 27 business tools, without show tools or UI resources.
Management capabilities (RETIRE, CANCEL_EXECUTION, RECOVER) are excluded
from pending decisions; their existing exact capability targets are preserved.

## Edit ownership

- A: model/Local registrations, typed mutation adapters, schemas, adapter_core,
  review_progression, and their focused tests. Do not edit the UI HTML,
  feature_area_presentation.go, presentation routing/lease, or shared read file.
- B: dev_bridge_ui.html, its JS tests and fixture preview transport/scripts.
- C: controlapi and workflowpresentation projections/targets; new
  mcpserver/surface_reads.go and focused tests. Export WorkspaceGetInput,
  WorkspaceGetOutput, WorkspaceGetCore, WorkspaceGetInputSchema; corresponding
  WorkList and OperationGet symbols. Registration belongs to A.
- Integrator: feature_area_presentation.go, presentation_routing.go,
  presentation_lease.go, display contracts, documentation, cross-cutting tests.

All work uses the same checkout. Builds are coordinated; no duplicated
environment or live data is required. This document records the integration
contract, not evidence of completed implementation or host acceptance.

## Old to new mapping

| Previous GPT tool(s) | Current GPT tool |
|---|---|
| development_workspace_get, workflow_get, workflow_guidance_get, feature_area_presentation_get, task_presentation_get, development_branch_list | workspace_get (optional includes) |
| task_work_list, execution_list | work_list(kind=TASK or OPERATION) |
| review_get, execution_get, agent_runtime_state | operation_get(target, include) |
| development_context_card_show without a Session | project_selection_show |
| development_context_card_show, feature_area_card_show with a Session | workspace_show |
| feature_area_details_show | details_show |
| featureArea_create | feature_area_create |
| featureArea_activate, featureArea_decompose, featureArea_retire | feature_area_lifecycle(action=ACTIVATE, DECOMPOSE or RETIRE) |
| review_approval_respond, GPT decision_respond, task_acceptance_respond | decision_respond(kind=REVIEW_APPROVAL or TASK_ACCEPTANCE) |
| codex_request_resolve; Local decision_respond runtime Approval branch | runtime_request_respond(kind=CODEX_REQUEST or RUNTIME_APPROVAL) |
| execution_cancel, operation_cancel | operation_cancel |
| featureArea_commit | feature_area_submit |
| file_create, file_replace, file_delete, file_move | file_write(action=CREATE, REPLACE, DELETE or MOVE) |

All other target tools retain their names. ScopeExpansion was already rejected
by Core with SCOPE_EXPANSION_LEGACY_ONLY; it is not a newly removed ability.
Existing internal Core and selected app-only projection methods remain available
for their existing consumers. They are absent from model-visible discovery and
Local MCP discovery. Historical output shape compatibility is not promised for
GPT or Local MCP: callers must migrate to the typed surface. OPERATION pages
retain unknown legacy provenance in a typed read-only unavailableOperations
branch containing the original Core projection and diagnostic, without
fabricating a semantic target or failing the other valid rows.

## Result and retry boundaries

Business data tools return a compact result/context/workflow/presentationHint envelope.
`bridge_diagnostics` is a frozen bootstrap identity exception: both adapters
advertise and return the same flat eight-field identity schema, before Project
binding. It must never acquire business envelope or workflow guidance fields.
`project_list` and `project_open` retain their current envelope; their longer-term
bootstrap/binding classification is a follow-up contract cleanup item.
Optional named read partitions preserve original source generations/sequences;
independent reads are explicitly non-atomic. Failure keeps successful partitions
but disables their dangerous actions until refresh. Business mutations keep the
committed result/receipt even if exact resource readback fails. Direct File,
runtime Approval, Codex response identity and presentation retry identities do
not acquire a fabricated common durable receipt guarantee.

A workspace show returns model-visible exact identity/display data. The full
production component initialization is in MCP result metadata (`workspace`) so
it need not be repeated in GPT prose. The process-local presentation intent cache
retains the normalized request and first resolved scope, not a business snapshot.
A same-argument retry re-reads current facts and returns complete initialization
with the same entry identity. Its bounded capacity fails closed rather than
silently changing retry behavior. Process restart ends this presentation-only
cache; no business transaction is replayed.

The 40px collapsed component expands only after exact fresh reads. Ordinary
refresh and collapse/expand neither request another show nor send GPT a user
message. Only the existing eligible successful Review completion notification
may request that host message, with its original exact claim/dedup contract.
No automatic test proves actual host mounting or GPT context consumption.

## Presentation flow closure

The rules at the start of `ExternalServerInstructions` are the model-facing
source of truth. A user asking for current work receives a direct
`workspace_show(COLLAPSED)` when Session and target are known. “Show/open the
card” uses `workspace_show(EXPANDED)`. `workspace_get`, `work_list`, and
`operation_get` are SILENT analysis/component reads. A business sequence reads
what it needs, performs canonical mutations, and makes one final exact
`workspace_show` if UI is appropriate. This is not a request to show after
every read or to suppress all later entries for a session.

`project_selection_show` is the sole unbound first-project entry and defaults
to expanded. `details_show` is an exact read-only history/detail entry; it
does not expose business mutation controls, redirect historical objects to the
latest object, or use widget state as authority. Component actions, manual
refresh, expansion, and background refresh use the canonical endpoint followed
by an in-place exact read; they do not call a show tool or emit a natural-
language request for GPT to choose a new target. A text-only request and a
host with no UI continue through the existing SILENT/text path.

A show call has already returned the requested presentation: its response uses
`required=false` with a presentation-complete reason and cannot recursively
ask for itself again. `presentationRequestId` is generated by the caller for
one presentation intent, never invented by ordinary reads or presented as host
turn evidence. Exact Session/Project/target identities are carried through a
same-argument retry; changed arguments for the same ID conflict. Host mount and
visibility remain `UNCONFIRMED` until a host report.

The single hint schema restricts reason to NO_PENDING_INTERACTION,
PENDING_USER_DECISION, PENDING_RUNTIME_REQUEST, READ_UNAVAILABLE, or
PRESENTATION_RETURNED. Typed arguments contain exact Session/Project and optional
FeatureArea/Task/Decision, OperationTarget or bounded ActionTarget; never an
arbitrary tool/payload. Local transforms this hint to TEXT with no ChatGPT tool or
arguments and advertises a matching output schema.

Historical gate status uses exact receipts or explicit changes to the authoritative
Task's CurrentReviewID/CurrentExecutionID. Absence from pending alone remains
HISTORICAL_STATUS_UNAVAILABLE. Runtime Approval reads its own durable status;
Codex Request uses NOT_PENDING_OR_UNAVAILABLE after its exact actionable target
vanishes. Old runtime entry controls never adopt a replacement request.
