> Historical validation record; not a current workflow contract. See the [final v0.1 model](task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# ChatGPT FeatureArea Card production integration

> The current v38 progression contract is [Review Approval workflow authority](review-approval-workflow.md).
> New Tasks follow Review → Approval → Task → Preparation → Implementation → Verification.
> TaskPlan confirmation and separate Execution authorization in this historical
> integration record apply only to legacy Tasks. Current clients consume Core
> `workflowGuidance`; Review Approval is shared across GPT, Local MCP, Web and Card.

This change builds on the accepted presentation foundation and connects its
client-neutral projection to production actions. It does not change the Task
receipt resolver, Execution authorization, Approval delivery, cancellation, or
recovery transition protocols.

## Integration history

| Milestone | SHA |
|---|---|
| Development before foundation merge | `7ff12eac7ad9f7d7206470f6d82e35afa45dc848` |
| Foundation commit | `dca49c270febcc169f3b05f5b343afc98aba3de6` |
| Unified baseline | `dca49c270febcc169f3b05f5b343afc98aba3de6` |
| Production integration start | `dca49c270febcc169f3b05f5b343afc98aba3de6` |

Foundation was fast-forwarded without rewriting its 17-file commit. Production
integration is developed on `codex/chatgpt-feature-area-production` in a separate
worktree. The containing integration commit and final development SHA are recorded
in the delivery response; a commit cannot embed its own content-derived SHA.

## Default routing

The production Card integration is enabled by default. It connects renderer
actions to their existing authoritative action owners, then reconciles from
exact readback and a fresh projection; the Card never becomes workflow state.
Development branch creation is implemented. Creation, branch selection, and
Git checkout remain three separate actions: create writes only the validated
ref, select changes only Development Cycle metadata, and checkout remains an
explicit Git operation.

Superseded by [unified workspace Card navigation](dev-bridge-unified-workspace-card-navigation-closure.md):
only explicit `feature_area_card_show` / `development_context_card_show` emit a
main Card, and both mount the same long-lived Dev Bridge Card resource. The table
below records the original integration routing.

| Entry / action | Default main resource |
|---|---|
| Project list/open; development branch list/select/create | DevelopmentContextCard |
| Explicit FeatureArea projection/workflow read; FeatureArea creation/lifecycle; Review start; Task create/prepare; cancellation/recovery | FeatureAreaCard |
| Exact history / subject reads; app-only decision adapters | No additional main card |
| View details | Independent FeatureArea Details view |

The default HTTP server installs presentation without an opt-in flag. The old
Decision resource and app-private compatibility endpoints remain readable for
historical conversation items, but are not the default main route. Local MCP
keeps resource-free contracts. Its branch-create schema is shared with GPT MCP.

The routing middleware preserves established structured command results and adds
the complete neutral text projection plus exact session/FeatureArea identity and
projection metadata. A follow-up projection failure does not turn an already
committed command into a failed mutation. The public
`feature_area_presentation_get` returns the same full projection as JSON and text;
Console continues to call the same facade and renderer.

Cards are conversation items, without a background delivery channel. Explicit
entry, status requests, remount and production actions can recall the current
card. Ordinary discussion and detailed reads do not repeatedly insert main cards.

## FeatureArea states

| Authoritative facts | Main card interpretation |
|---|---|
| Review active | Current Review phase and observed progress |
| Preparation active | Preparation progress; no implementation authorization |
| Prepared TaskPlan pending | Confirm / Revise, only if projected available |
| Execution proposal pending | Authorize / Reject, only if projected available |
| Reserved or running operation | Actual projected status; a click does not imply startup |
| Pending runtime Approval | Exact Approval options and cancellation effect |
| Cancellation requested | `CANCEL_REQUESTED`, never terminal canceled |
| Safe cancellation completed | Authoritative `CANCELED` result |
| Unknown unsafe operation | Historical `UNKNOWN` plus current recovery condition |
| Explicit recovery completed | Current context `READY` only with matching recovery lineage; original failed/unknown progress and the historical `UNKNOWN` boundary remain intact |
| Failed, blocked, rejected, unavailable, retired | Preserve lifecycle, phase, operation, blockers and available capabilities |

All renderer actions come from `capabilities[].available`; status labels do not
authorize actions. The compact context includes Project, development branch and
current observed revision. Original selected revision and historical baselines
remain distinct from the current branch head.

## Decision actions

| Decision | Write adapter | Identity / guarantee | Readback |
|---|---|---|---|
| Task Confirm / Revise | `ui_task_decide` | Canonical TaskPlan Decision; stable server-hydrated requestId; `DURABLE_REQUEST_RECEIPT` | `ui_decision_get` |
| Execution Authorize / Reject | `ui_execution_decide` | Exact proposal; no requestId; `AUTHORITATIVE_ENTITY_STATE` | `execution_get` |
| Runtime Approval | `ui_approval_decide` | Exact Approval and legal option/payload; no requestId; `AUTHORITATIVE_ENTITY_STATE` | `ui_approval_get` |

Task confirmation creates exactly one pending Execution and does not start
Implementation. Approval permissions are included only for the exact permission
grant option. Workflow-owned Approval CANCEL means `DECLINE_ACTION`; the legacy
standalone `CANCEL_LEGACY_RUN` effect stays distinguishable. Whole Execution
cancellation never uses the Approval adapter. ScopeExpansion stays `LEGACY_ONLY`.

Actions reconcile from exact subject readback and fresh projection after success,
conflict, response loss or failure. Request identity is transport metadata, never
a frontend receipt store or a second resolver. Late responses, changed identities,
foreign message sources and teardown cannot submit stale subjects or replace the
current card with old projection data.

## Cancel and Recovery capabilities

| Condition | Cancel | Recover |
|---|---|---|
| Exact current consumed Execution, latest active Implementation/Verification, matching claim/epoch/thread/turn/boundary, live runtime owner, no unsafe intent or pending receipt | Available with exact Execution and Run target | Unavailable |
| Pending cancellation intent | Hidden; authoritative request remains pending | Only if existing recovery core permits |
| Wrong/stale Run, claim, operation or runtime epoch; or no live cancellation owner | Hidden | Hidden unless exact existing recovery prerequisites match |
| UNKNOWN alone | Does not grant availability | Does not grant availability |
| Old epoch, terminal UNKNOWN, canonical conversation and exact stranded claim/boundary accepted by recovery core | Hidden | Available with exact operation, Run and conversation |
| Recovered current conversation | Hidden for the old operation | Hidden; no automatic successor |

The read eligibility port reuses recovery classification and mirrors existing
cancel/recovery guards without calling their write paths. The facade also reads
the supervisor's exact live-session predicate and checks that capability targets
match the operation already observed in this projection. The authoritative action
revalidates all guards. Cancel uses only `execution_cancel` and stable SHA-256
request identity. Recover uses only `agent_operation_recover`. Intent-before-
interrupt, one-shot dispatch, fencing, interrupted terminal, transport join,
quiescence, safe boundary, atomic finalization and replay remain core-owned.

## Refresh and Details

The unified bound Card Refresh calls only the app-private pure
`dev_bridge_card_get` plus branch inventory. Before Project selection it calls
`project_list`; `project_open` is invoked only by an explicit Project click.
Refresh never opens a Project, selects/creates a branch, materializes
Preparation, resolves a Decision, cancels, recovers, or starts backend work.
Compatibility workflow reads keep their established domain behavior and are not
used as Card Refresh.

Development branch creation is an authorized, additive Bridge command: it verifies
session Project/Repository ownership, validates an exact local ref and immutable
commit, and atomically creates the Git ref. Exact existing entity state is
replayable and a different head conflicts. Selection and checkout are separate;
creation does neither. Selecting a branch preserves earlier workflow provenance.

Details composes existing session-scoped read APIs, with pagination and original
identities for Review result/prompt, Tasks and definition revisions, Preparation
attempts, Execution/Run history, workspace/Git/evidence, true Task request receipts,
Approval entity state, cancellation receipts, recovery receipts and conversation
rotation, plus legacy ScopeExpansion history. It creates no history persistence.
Missing optional facts are handled without substituting another object's state.
The backend scans all existing paginated sources before filtering by FeatureArea
and applying each section's page window. Historical Workspaces are loaded by the
original Run's Workspace ID, with owner validation and quarantine state retained.

The Details implementation and read model remain available, but the FeatureArea
Card currently hides its default `View details` entry. This keeps Details an
explicit separate presentation rather than a default Card navigation path.

ChatGPT navigation uses a registered modal template and returns to the conversation
using the host close action. Host API behavior is based on the official
[ChatGPT UI reference](https://developers.openai.com/plugins/reference) and
[UI building guide](https://developers.openai.com/plugins/build/chatgpt-ui).

## Validation and review

Validation covers foundation merge smoke; full Go packages; affected race and vet;
Card/DOM/transport tests; fresh GPT/Local MCP catalog and closed schema tests;
pure-read database counts and content fingerprints; renderer text/JSON parity;
and the offline production UI build with the three self-contained embedded assets.
Deterministic runtime suites cover Task replay/conflict/no-start, non-Task entity
readback/no-redelivery, exact cancellation races and unsafe boundaries, recovery
rotation/quarantine, and no automatic successor.

Whole-diff review is separate from implementation. Findings addressed include
navigation capabilities accidentally routed as Approval mutations, canonical
Approval kind/readback and permissions, message origin and identity fencing,
Development Context host initialization, branch conflict error mapping, missing
recovery history rendering, truncated history pagination, live cancellation owner
eligibility, and historical recovery requirements incorrectly remaining current.

The final independent review included every new untracked source, resource, test
and documentation file. Its remaining finding count is **P1: 0, P2: 0**. The two
last P1 findings were closed with direct regression evidence:

| Finding | Resolution and evidence |
|---|---|
| A recovered failed Execution still appeared recovery-required | The exact receipt, canonical successor conversation, current operation/phase and latest Run fence now determine current readiness. `TestExplicitRecoveryReprojectsFailedExecutionAsReady` exercises real SQLite startup classification, Facade recovery and Facade re-projection; it preserves failed Run/Execution history and the UNKNOWN boundary while terminalizing the Task as canceled with UNKNOWN_ORPHANED, verifies quarantine and rotation, and creates no successor Run. Focused repetition and race both pass. |
| SQL cancellation eligibility could advertise a Run without a live owner | The facade also requires the supervisor's exact live-session admission predicate. Missing owners fail closed. Read-only lock-order review and live/stale/stopped-owner tests pass. |

The final validation commands and results are recorded below. Full Go validation
reported 53 passing test packages; packages without tests are additional entries.

| Gate | Command / scope | Result |
|---|---|---|
| Foundation merge gate | Projection/text/parity and Final State Semantics focused smoke at unified baseline | Passed; 44 baseline UI tests |
| Full Go packages | `go test ./...` | Passed, 53 test packages |
| Affected race | `go test -race` for git, developmentcycle, workflowpresentation, controlapi, mcpserver, localmcp, consoleapi, sqlite, agentconversation, approval, execution, taskpreparation | Passed |
| Static analysis | `go vet ./...` | Passed |
| Go production build | `go build -o <temporary-output>/ ./cmd/...` | Passed |
| Frontend/DOM/transport | `node --test internal/mcpserver/*ui_test.js` | Passed, 72/72: legacy Decision 26, FeatureArea 26, Development Context 16, Details 4 |
| Production frontend build | `node scripts/build-chatgpt-ui.mjs <empty-temporary-output>` | Passed; three self-contained resources and SHA-256 manifest |
| Fresh GPT/Local catalogs and schemas | `TestExternalWorkflowDiscoveryContract`, `TestLocalMCPDiscoveryContract`, production routing tests; also included in full package gates | Passed |
| Refresh purity | Terminal Preparation projection before/after table counts and full-content fingerprints, including receipt/recovery tables | Passed |
| Recovery current/history separation | `TestExplicitRecoveryReprojectsFailedExecutionAsReady`, repeated three times and under race | Passed |
| Details | Exact ownership/history, 105-item pagination across two FeatureAreas, quarantined Workspace readback, UI navigation/close | Passed |
| Formatting and patch integrity | `gofmt -l` over all changed Go files; `git diff --check` | Passed |

The last frontend check also covers Refresh before Project selection, including
an empty project list: it calls only `project_list`, never `project_open`. That
final resource adjustment was followed by another complete UI/build and MCP
package test, race and vet pass. Fresh GPT/Local catalog tests were explicitly
rerun with `-count=1`.

No required implementation items remain deferred. Historical ScopeExpansion
mutation remains unavailable by the frozen `LEGACY_ONLY` contract; compatibility
Decision resources remain available only for existing conversation items. A host
without modal support receives an explicit Details-unavailable message and retains
the complete text/JSON read surface. No real host navigation acceptance is claimed.

No Desktop code is changed. Opt-in external real-Codex/real-ChatGPT host acceptance
is not claimed; deterministic tests and production resource builds are the
executed acceptance surface. No Global Final Acceptance Re-review, release,
push, generic DAG, cross-FeatureArea orchestration or async delivery is included.
