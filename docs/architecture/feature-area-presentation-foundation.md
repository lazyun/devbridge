> Historical validation record, superseded by the [final v0.1 model](task-execution-v0.1.md). This milestone does not define current production behavior.

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# FeatureArea presentation foundation

> The current v38 progression contract is [Review Approval workflow authority](review-approval-workflow.md).
> New Tasks follow Review → Approval → Task → Preparation → Implementation → Verification.
> TaskPlan confirmation and separate Execution authorization in this historical
> integration record apply only to legacy Tasks. Current clients consume Core
> `workflowGuidance`; Review Approval is shared across GPT, Local MCP, Web and Card.

> Historical foundation phase. The production Card is now enabled by default
> and connected to authoritative actions; see
> [production integration](chatgpt-feature-area-production-integration.md) and
> [unified Card navigation](dev-bridge-unified-workspace-card-navigation-closure.md).
> This document
> preserves the client-neutral projection and pure-read foundation only.

This foundation reads Bridge-owned facts into one client-independent
`workflowpresentation.Projection`. It does not persist workflow phases, add
workflow ownership, resolve Decisions, or introduce a history model.

```text
Existing Bridge domain services and exact current-operation lineage
    -> controlapi.FeatureAreaPresentationForSession
        -> workflowpresentation.Projection
            -> RenderText
            -> Console JSON / text read
            -> optional ChatGPT FeatureArea Card skeleton
```

The implementation starts at `7ff12eac7ad9f7d7206470f6d82e35afa45dc848`
(`fix: close v0.1 final state semantics`) on the isolated branch
`codex/feature-area-presentation-foundation`. It changes no files from that
Final State Semantics Closure commit. Existing workflow, Console, Local MCP,
Decision, cancellation and recovery interfaces retain their contracts.

## Projection contract

The source schema is `internal/workflowpresentation/projection.go`, version `1`.

| Field | Meaning |
| --- | --- |
| `featureArea` | Stable Project-owned FeatureArea ID and name; independent of Task completion and AI session |
| `developmentContext` | Project and every repository's selected development ref, current branch revision, original selected revision, selection/missing/drift/checkout facts |
| `lifecycleStatus` | FeatureArea lifecycle, separate from execution outcome |
| `workflowStatus`, `currentPhase` | Read-only status and current presentation surface |
| `progress[]` | Ordered REVIEW, TASK, PREPARATION, IMPLEMENTATION, VERIFICATION facts; explicit status, immutable source ID, Preparation ordinal |
| `currentOperation` | Current claim's exact kind, operation ID, Run ID and authoritative operation status; absent when idle |
| `pendingDecision` | Canonical `KIND:subjectId`, explicit subject ID, kind, summary and declared display actions |
| `recovery`, `blockers` | Explicit required/unknown/unavailable facts; never a success fallback |
| `capabilities[]` | Observed Bridge eligibility, reason when unavailable, and canonical Decision option mapping |
| `details` | Navigation view plus exact Project/FeatureArea/Review/Task/revision/Preparation/Plan/Execution/Run IDs and selected baseline |
| `updatedAt` | Latest selected workflow source timestamp; not a transaction version or Git observation timestamp |

Capabilities describe observed Bridge eligibility, not whether a particular
renderer has an action handler. Every mutation still requires revalidation by
its existing authoritative owner. `AUTHORIZE` maps to the existing `EXECUTE`
Decision option and `REJECT` to `CANCEL`; runtime Approval options are copied
verbatim. These mappings are data only. No Card mutation is connected.

The foundation projects read/details and current Decision capabilities.
Recovery eligibility remains explicitly unavailable: a general recovery status
does not prove that the exact operation can currently be recovered. Existing
general workflow projections continue to expose their broader engineering
capabilities. This foundation does not relocate those command guards.

## Current and historical work

The existing `CurrentWorkflow` selector owns object selection, including the
frozen Preparation/TaskDefinitionRevision of an outstanding Execution. The new
projection formats these selected facts and reuses `workflowstate.Derive` for
the common workflow phase. No phase is written to a database.

Repeated Preparation keeps its immutable ID and ordinal. A newly active Review,
or a newer admitted Review after prior work and without an outstanding Development Task or
Execution decision, gets pending downstream progress and its own baseline;
the old Task's completed phases are not attributed to the new Review. The
FeatureArea identity persists. Historical objects remain available through
existing detail APIs, unchanged.

A PREPARING Preparation can coexist with a terminal Run awaiting its owner's
finalization. The read surface exposes FAILED/INFRASTRUCTURE_FAILED/CANCELED
when those Run facts exist, or FINALIZATION_PENDING after a successful Run.
It leaves the durable Preparation PREPARING and never invents a READY result.
Recovery from either operation state or an unresolved cancellation receipt
marks both the overall status and the affected phase, while retaining any
pending Decision and suppressing ineligible actions.

## Pure Refresh entry points

Generic Go APIs:

```go
facade.FeatureAreaPresentationForSession(ctx, aiSessionID, featureAreaID)
facade.DevelopmentContextPresentationForSession(ctx, aiSessionID)
workflowpresentation.RenderText(projection)
```

Console HTTP read (JSON by default):

```text
GET /ui/api/featureAreas/{id}/presentation?project_id={project}&ai_session_id={session}
GET /ui/api/featureAreas/{id}/presentation?project_id={project}&ai_session_id={session}&format=text
```

The Console validates that the supplied existing session belongs to the given
Project. It does not create a session from the Console's selected Project.
Foreign Project identities fail closed. The endpoint uses no-store and rejects
unknown query parameters and write methods.

The old `CurrentWorkflowForSession` path can lazily call
`TaskPreparations.ObserveTerminal`; the old `ui_workflow_state_get` can claim a
continuation. Neither is a valid Refresh implementation on its own. The new
read facade shallow-copies its dependencies and supplies a read-only
Preparation observer while reusing the exact existing lineage selector.
No shared authoritative implementation is changed. Review continuation,
Preparation materialization, branch selection, Project open, Decision
resolution, cancellation and recovery are never invoked by this path.

## Optional ChatGPT adapter

Hosts can register `RegisterFeatureAreaPresentation` on their MCP server, or
set `mcpserver.Config.EnableFeatureAreaPresentation = true`. The compatibility
default leaves the existing production discovery catalog unchanged.

The optional adapter registers:

| Tool | Input | Output |
| --- | --- | --- |
| `feature_area_presentation_get` | Existing `aiSessionId`, explicit `featureAreaId` | Same Projection as structured content, same RenderText as content |
| `development_context_presentation_get` | Existing `aiSessionId` | Same compact DevelopmentContext, with no branch selection |

Both tools declare read-only annotations. At this historical phase the Card
resource was `ui://devbridge/feature-area-card`; it is now the unified
`ui://devbridge/card`. Tool result metadata carries the current
session/FeatureArea mount identity, not workflow state. An initialized mount
performs the pure read; a fresh session in the same Project rebuilds the same
FeatureArea from its identity. Refresh uses a fixed tool allowlist and rejects
late responses from an older identity or refresh sequence.

The Card renders context, name, lifecycle/status, phase/progress, operation,
Decision/recovery summaries, an active Refresh button and one disabled
`View details` entry. The entry's `navigationPayload` retains exact immutable
IDs; it does not show history, prompts, logs, diffs or receipts. Eligible
Decision buttons are disabled visual skeletons. Recovery stays a disabled
skeleton with its eligibility reason. Renderer code does not query domain
objects or derive workflow transitions.

Supported states include no Task/no operation, running/completed/blocked
Review, Task confirmation, first/repeated Preparation, Execution authorization,
Implementation, Verification, terminal Task, UNKNOWN, RECOVERY_REQUIRED and
FORBIDDEN. Failures remain explicit in both renderers.

## Text example

The following is an excerpt of actual RenderText formatting. The complete
output also includes all context, progress sources/ordinals, recovery,
capability reasons, navigation identities and the timestamp.

```text
Feature area:
  ID: "area"
  Name: "Dynamic coding-agent model support"
Lifecycle status: "ACTIVE"
Workflow status: "EXECUTION_AWAITING_AUTHORIZATION"
Current phase: "IMPLEMENTATION"
Progress:
  Phase:
    Name: "IMPLEMENTATION"
    Status: "PENDING"
    Source ID: ""
    Ordinal: 0
Pending decision:
  Kind: "EXECUTION_AUTHORIZATION"
  ID: "EXECUTION_AUTHORIZATION:execution"
  Subject ID: "execution"
  Summary: "Execution authorization required."
  Declared allowed actions:
  - "AUTHORIZE"
  - "REJECT"
Available actions:
  - "REFRESH"
  - "VIEW_DETAILS"
  - "AUTHORIZE"
  - "REJECT"
```

`AvailableActions` uses only explicit capability availability. Unavailable
capabilities may appear in the complete diagnostic capability list as false;
they never appear in the available-action list. Strings are quoted/escaped so
authored text cannot create fake status lines.

## Validation and integration boundaries

Validation covers the projection state matrix, immutable Decision identity and
option mapping, Review restart/history separation, branch facts, repeated
Preparation and lifecycle timestamps. Real SQLite tests verify repeated and
fresh-session reads leave Preparation materialization and continuation markers
unchanged and create no Review/Task/Preparation/Plan/Run/Execution/Decision
receipt/DevelopmentCycle. A terminal Run remains terminal without the read
finalizing its Preparation.

Console/facade equality, HTTP JSON/text equality and MCP structured/text
equality validate renderer parity. Card tests exercise status/progress/Decision
display, unavailable actions, safe text rendering, initial mount, current Git
revision, multiple repositories, pure Refresh and out-of-order responses.

Affected package validation:

```sh
go test -race ./internal/controlapi ./internal/workflowpresentation ./internal/mcpserver ./internal/consoleapi ./internal/bridgehttp
go vet ./internal/controlapi ./internal/workflowpresentation ./internal/mcpserver ./internal/consoleapi ./internal/bridgehttp
node --test internal/mcpserver/*test.js internal/bridgehttp/*test.js
git diff --check
```

Deferred integration points are intentional: enabling this optional adapter in
host launch configuration and updating the shared production MCP catalog;
replacing legacy card mount routing; the independent Details page; exact
Recovery eligibility; and any future Card action integration. The legacy
workflow readers retain their existing behavior and must not be renamed or
treated as pure Refresh.

This task does not change cancellation, quiescence, finalization, claim
release, Decision receipts, replay/conflict behavior, lifecycle serialization,
Task revision ownership or DevelopmentCycle mutation semantics. It performs
no push, release, merge into another task, orchestration or final acceptance
re-review.

## Changed files

- `internal/workflowpresentation/projection.go`: client-independent schema.
- `internal/workflowpresentation/text.go`, `text_test.go`: complete text fallback and availability formatting.
- `internal/controlapi/feature_area_presentation.go`: pure reads and common presentation derivation.
- `internal/controlapi/feature_area_presentation_test.go`, `feature_area_presentation_integration_test.go`: state matrix, lineage, identity and SQLite read-purity checks.
- `internal/consoleapi/feature_area_presentation.go`, `feature_area_presentation_test.go`: Console facade and parity checks.
- `internal/bridgehttp/feature_area_presentation.go`, `feature_area_presentation_test.go`: JSON/text GET and transport validation.
- `internal/bridgehttp/server.go`: one GET route registration.
- `internal/mcpserver/feature_area_presentation.go`, `feature_area_presentation_test.go`: optional MCP resource/tools and transport parity.
- Superseded UI: the foundation's `feature_area_ui.html` was replaced by
  `internal/mcpserver/dev_bridge_ui.html` and `dev_bridge_ui_test.js` when the
  FeatureArea and Development Context resources converged into one Card.
- `internal/mcpserver/server.go`: explicit opt-in configuration and registration.
- `docs/architecture/feature-area-presentation-foundation.md`: this contract and handoff record.
