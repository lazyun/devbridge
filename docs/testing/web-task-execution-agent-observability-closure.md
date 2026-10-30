> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

# Web Task / Execution and Agent observability closure

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Acceptance: `WEB_TASK_EXECUTION_AND_AGENT_OBSERVABILITY_CLOSURE_READY`.

## Baseline and delivery

The starting development branch was `dev_20260908_1913`, clean at
`e7ddb3559db65ee9ca3b3b68963484a7404356d2`, with source schema v40.
Work was isolated in `codex/web-task-agent-observability` at
`/private/tmp/devbridge-web-observability`. The delivery commit is the commit
containing this report; its SHA is recorded in the final delivery response.
Delivery fast-forwards the unchanged development branch and removes this task's
worktree, branch, candidate binaries and build cache. No push or Release.
Formal port 8081, live SQLite and Desktop configuration were not changed.

## Correctness contracts

Workflow guidance previously selected latest Review independently of the Task
and operation graph. The shared projection now selects the claimed/current
Development lineage first, loads its exact Approval, and separates
`latestReviewStatus` from `currentWorkflowReview`. A new admitted Review can
supersede an idle older chain; incidental history cannot displace claimed work.
Approved Development projects Review and Approval COMPLETE. Legacy work uses
explicit `workflowMode: LEGACY` and retains its existing execution/recovery
facts. Current Execution phase and Run use the Execution's exact Run references.

Projection reads compare authoritative guidance generation before/after the
read and retry on change. The published generation hashes the complete result;
its guidance, IDs, progress and capabilities share sourceSequence/generation.
Operation status, conversation and claim time are captured in that projection
as well. Historical Task navigation cannot regain engineering capabilities.
Web request/navigation generations and Card navigation/read sequences reject
late responses and preserve the last successful result on failure.

## Presentation and refresh

FeatureArea rows open from title, other non-action regions and Enter/Space.
Nested actions stop propagation. Hover/focus feedback is present; retirement
capabilities and Active/Closed/All remain authoritative.

Task goals appear once, without inferred Pending/Running/Completed execution
items. Task/Execution detail renders the official `latestPlan` count and statuses,
current item summary, relative last activity, and existing pending request forms.
Missing plan means “Codex 尚未报告执行计划”. Runtime diagnostics contain exact
Thread/Turn and process identity; ordinary Task facts include Agent, Model,
Codex session, Run and phase.

Tasks and Executions have independent local off/5/10/30-second preferences,
default off. Every entry performs a fresh read of its scoped work-tab endpoint.
Only the active tab owns a timer; leaving/hidden pauses it, visibility resumes
with a fresh read, overlapping reads are suppressed, stale navigation responses
are discarded, and errors retain content without automatic refresh Toasts.
Request forms are not replaced while the user is editing them.

Web termination submits project, exact Execution/Run and a stable request ID to
the existing canonical Execution cancellation facade using the Execution's
session. Core's capability controls availability. Confirmation is required;
accepted intent displays “正在终止…” and never creates a frontend terminal
state. An immediate authoritative read follows even with auto-refresh off.
Durable UNKNOWN_ORPHANED dispositions survive ordinary canceled/failed product
state mapping, including recovery without a cancellation receipt.

## Official metadata and process telemetry

Installed `codex-cli 0.149.0` generated both JSON and TypeScript schema via
`codex app-server generate-json-schema` and `generate-ts`. The generated v2
`Thread.name` and `ThreadNameUpdatedNotification.threadName` are the only native
session-name sources. Missing names display “未命名”; Task/FeatureArea names,
Run purpose and agent nickname are not fallbacks. The official contract is
[Codex App Server](https://developers.openai.com/codex/app-server).

Schema v41 adds `agent_process_launches` and `run_agent_processes`. Actual OS
launch records launchId, PID, executable, start/end, exit code and runtime epoch.
Multiple Runs can bind the same launch; immutable launch/binding identities
prevent PID reuse from replacing history. Exit telemetry is epoch-scoped rather
than tied to the first Run's lifetime. Migration adds no guessed historical
bindings and rewrites no workflow authority. Old Runs report UNAVAILABLE.

The existing client-neutral `agentruntime.Snapshot` is the telemetry read model:
`agent` contains name/sessionName/processLaunchId/pid/process metadata; existing
`latestPlan`, `currentItem`, `lastActivityAt`, `waitingState` plus `agentHealth`
remain shared across Run/Console/Web/JSON/MCP reads. Card consumes only the small
Summary (plan count, active step, waiting, relative activity and health).

`POSSIBLY_STALLED` requires authoritative RUNNING, current connected ownership,
no open official request, and activity older than 30 minutes. Open requests take
precedence as WAITING. Terminal/disconnected/unknown Runs are never classified
stalled. This pure classifier performs no writes. PID, plan completion, item
completion and stalled health never authorize cancellation or finalization.

## Evidence and gates

- exact Task/Review inventory and Execution parent identity, frozen main branch;
- official 5-step plan with 2 complete and actual OS PID/launch binding;
- Web and Card current IDs, Review/Approval COMPLETE, shared generation and
  captured Agent operation facts, with IMPLEMENTATION_RUNNING;
- telemetry-only 43-minute aging leaves Run RUNNING and reports POSSIBLY_STALLED;
- official permission request changes health to WAITING despite that age;
- Web canonical cancellation converges to CANCELED or UNKNOWN_ORPHANED with
  terminal cancellation capability unavailable.

The existing official app-server candidate script also passes command approval,
file approval, permission, user input, MCP elicitation, cancellation, retirement
and transport loss. The loss fixture retains UNKNOWN boundary and non-quiesced
truth while its Run becomes INFRASTRUCTURE_FAILED.

Browser acceptance on the final production candidate confirmed real Task rows,
main-branch filtering, native session name, 2/5 plan/current Command/waiting,
Task 5s and Execution 30s independent settings, and FeatureArea title/Enter/Space
navigation with exact Implementation details. JS interaction tests additionally
cover whitespace/status regions, nested-action propagation, canceled confirmation,
exact cancellation payload, accepted-vs-terminal behavior and refresh fencing.

Validation: full `go test ./... -count=1` passed 55 tested packages; affected `go test -race` across
agentruntime/codex/sqlite/run/execution/controlapi/consoleapi/bridgehttp/mcpserver/
localmcp passed; `go vet ./...` passed; 130 Web/Console/Card/Details JS tests; all embedded Web
JS syntax checks; production Core/GPT MCP/Local MCP builds; both self-contained
ChatGPT UI assets built by `scripts/build-chatgpt-ui.mjs`; v40→v41/restart/process
history tests; changed Go gofmt and `git diff --check`.

The final individual race runs passed controlapi, consoleapi, bridgehttp,
mcpserver and localmcp after the last projection changes. One existing 15-second
MCP integration deadline expired during the heavily parallel run; a separate
full mcpserver race run passed in 103.428 seconds, with no data-race report.

Whole-diff review covered all requested inventory, provenance, row interaction,
lineage/generation, official telemetry, process-only observability, refresh,
canonical cancellation and retirement boundaries. Discovered issues were fixed,
including raw Work-status labels, nil session-name caches in constructed Clients,
exact Execution Run selection, and UNKNOWN disposition display. No outstanding
P0/P1/P2 finding remains from this review.

Limits remain explicit: deterministic fixtures validate the installed protocol
contract without a paid real Codex Turn. Historical processes cannot be recovered
from missing telemetry. Stalled detection is advisory, with no automatic timeout,
failure, cancellation or descendant-process containment. No custom progress tool,
prompt injection or Desktop progress toggle was added.
