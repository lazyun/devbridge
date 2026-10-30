# Task-first presentation v0.1

The product view is one exact Task: title/goal, status, semantic progress, Codex
activity, Goal Progress, Usage, Result and eligible actions. FeatureArea remains
the persistent ACTIVE work domain after a Task finishes. Current Task and Latest
Task are separate facts; an idle area explicitly says 当前无运行中的 Task and
supports another Task through canonical Review admission.

## Shared read model

`workflowpresentation.TaskView` is built in Go, attached by `controlapi` and
returned through the existing Web/Console and app-only card transports. GPT and
Local MCP use compact `workspace_get`; full Task fields require include TASK. Text fallback renders the same
status, progress, accounting availability, result and capabilities before advanced
facts. Existing exact stage lineage remains diagnostic data, not a second rail.
No schema, workflow reducer, ownership or runtime fencing is changed.

Development progress is Review → Approved → Implementation → optional
Verification → Acceptance → Done. Review and Inspection use their own minimal progression.
Task/Execution/Run/Workspace are never semantic stage names. Historical and
terminal Task views do not borrow another current operation's actions. Review
APPROVE enters IMPLEMENTATION_READY; `execution_start` starts the approved Task.

## Usage and results

Run storage has a task-scoped read. BuildTask includes every distinct Run with
matching Project, FeatureArea and Task ownership, across attempts/Executions.
Separate Review Tasks are not billed again to a linked Development Task.
Duration sums all observed Run intervals, with Implementation/Verification/Other
breakdowns. This is accumulated run duration, not calendar elapsed time between
first intent and final completion. Missing intervals mark durationComplete=false.
23:50 Implementation + 08:02 Verification = 31:52 even with missing tokens.

Tokens retain AVAILABLE/PENDING/UNAVAILABLE. All Runs must have AVAILABLE,
attributed usage before complete totals are returned. Any UNAVAILABLE or invalid
available-without-usage Run makes the Task UNAVAILABLE and omits token totals.
Otherwise any PENDING Run (or no Runs) yields PENDING. Details identifies the
unavailable/pending Runs. No raw conversation cumulative count substitutes for
Run attribution; no unavailable count becomes zero. Duration is independent.

Result is the existing Task outcome plus a bounded exact Run result/failure
summary. Transport success alone never establishes Task acceptance.
VerificationReport v2 keeps Bridge-observed and Agent-reported facts separate;
its per-item statuses are evidence, not a Core Task pass/fail reduction. Prose is
not parsed into workflow authority. Report, Run transport, current Execution
acceptance and Task overall state are separate shared fields.

## Activity and actions

Native Codex plan is optional passive telemetry and has its own disclosure.
Structured Goal Progress remains visible independently. Activity first displays
what Codex is doing, its waiting/health state and current Run elapsed time.
Runtime request responses preserve exact Task/Run/thread/turn/connection/item/
request identities and use the existing resolver. Permission, cancellation,
recovery, Review approval and new Task actions revalidate authoritative targets.
The adapters implement no execution or approval transitions.

## Navigation and refresh

Task list exposes only Task / Status / Time. Its whole row opens that exact Task.
The opened nonterminal Task owns a 5-second read timer, including user waits.
Terminal response ends polling; close/switch aborts or fences the previous read;
hidden pauses and visible resumes only a nonterminal Task. No project/list timer
fans out across Runs. Details is a local disclosure of already-read diagnostics.
Card reads the app-only `dev_bridge_card_get` with exact per-entry selection and
uses `workspace_get(include=["BRANCHES"])` for branch inventory. Collapsed entries
do not install a workspace poller. Refresh/expand/collapse never writes Core.

Card and Web use grouped surfaces, one progression rail, distinct Goal Progress
and Native Plan, adjacent Usage/Result, and secondary Verification/Details.
Card provides its own restrained engineering palette in light/dark; narrow cards
stack outcomes and use a vertical rail to keep stage names readable. Long titles
retain their full accessible name. The isolated visual hosts have no mutation
backend and prove layout only, not real ChatGPT host integration.

The current [consolidated surface and component contract](gpt-surface-consolidation-v1.md)
separates capabilities from real pending decisions. Cancellation appears once in
the exact Task operation area; settings, refresh and collapse belong in the Header.
