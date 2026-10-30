# FeatureArea continuous Tasks and canonical workspace — final v0.1

> Project-centric update (schema v73–v74): [implemented contracts and remaining workflow boundaries](project-centric-multi-repository-closure.md). Projects may have zero or multiple repositories; Session selection is Project-only. Workspace membership is not authorization. Multi-repository Review, execution and delivery remain explicitly blocked where the complete authority chain is unavailable; the single-repository flows below describe compatibility behavior.

Project → FeatureArea → Task → Execution → Run remains the work hierarchy.
FeatureArea independently owns the current AgentConversation and Workspace.
Neither a Task nor an Execution, Run or Review owns the canonical workspace.

## Next Task and authorization

An ACTIVE FeatureArea remains ACTIVE after its Task succeeds, fails or is
canceled. Once canonical finalization proves quiescence and releases the exact
claim, Core projects `FEATURE_AREA_IDLE` / `WAIT_FOR_USER_TASK`: no current active
Task, latest terminal Task retained, CREATE_TASK, VIEW_DETAILS and RETIRE
available, and RESET_AGENT_CONTEXT according to existing eligibility. Project
Context and Development Branch prerequisites still gate repository-backed work.
Only explicit RETIRE closes an active area. DECOMPOSED retains its separate
existing split lifecycle; Task outcomes are never FeatureArea lifecycle values.

`task_create` creates a semantic Task from title, problem, constraints and exact
FeatureArea/repository. It is allowed in DISCOVERING and ACTIVE. task_review_start
then starts a read-only operation for that exact Task. APPROVE freezes the Task's
initial immutable definition and activates a discovering area; explicit
execution_start reserves its first delivery cycle. A maintenance or commit goal
uses the same Task contract and grants no merge, push or release authority.

## Storage and Git

Current schema is v46; see [migration policy](task-execution-v0.1.md#schema-and-validation-boundary). Workspace stores required Project, Repository,
FeatureArea and immutable `base_commit`; FeatureArea stores `current_workspace_id`.
SQLite guards exact ownership, immutable origin, unique nonquarantined workspace
per area and current pointer replacement. Run.workspace_id is a factual foreign
key and is checked against the Run's exact area and repository on admission.
There are no Task/Review/Execution workspace owner columns or compatibility maps.
Obsolete same-version databases and old workspace rows require explicit developer
recreation; no automatic data loss or guessed ownership conversion is performed.

The first repository-backed operation persists the Workspace identity, then
creates its deterministic worktree at `<worktrees>/<workspace-id>`, branch
`devbridge/<feature-area-id>/<workspace-id>`. The baseline is the selected
Development Branch's current exact commit, independently of repository checkout.
Worktree metadata and workspace attachment are durable; replay validates the
same repository, path, branch and origin. A crash after physical creation before
attachment recovers the same deterministic worktree after identity validation.
It never resets Git state while recovering.

Review, Implementation and Verification reuse the same ID and path. Later Tasks
see both prior commits and uncommitted work. Review's runtime ceiling remains
read-only. Immutable Task definitions govern each Development Execution; earlier
changes visible in the workspace confer no additional mutation authority. Bridge
retains the trusted-agent model and `SCOPE_CONFLICT` stop contract, without adding
changed-file compliance enforcement.

Review provenance uses `FEATURE_AREA_WORKSPACE`. Its recorded integration ref
and commit describe the authoritative baseline; Run start/end snapshots describe
the actual observed working tree. Workspace evolution is not repository drift.
Drift compares the recorded integration ref, not the source checkout or working
branch. Integration/rebase/merge is a separate explicit intent. Task completion
never automatically integrates or cleans the workspace.

## Recovery, cancellation and retirement

Same-area work retains its existing durable claim and runtime epoch fencing;
different areas own separate workspaces and may run concurrently. A terminal
write does not precede quiescence or bypass the canonical finalizer. UNKNOWN,
conflicting callbacks and retained claims block the next operation.

Agent Context Reset rotates only the conversation. It keeps the canonical
workspace. Canonical recovery can permanently revoke an unknown operation,
quarantine its exact old context/workspace, and detach only that unsafe current
workspace. Historical Runs keep their old workspace associations. A subsequent
safe operation may create an independent replacement; ordinary completion,
cancellation and context rotation do not replace it. Explicit retirement rejects
new Tasks and converges through the existing exact cancellation/recovery owner.

## Current and history projections

Client-neutral Guidance and presentation include `featureAreaStatus`,
`currentTask`, `latestTask`, and `currentWorkspaceId`. `currentTask` is absent at
idle. Existing taskId/executionId/reviewId fields can retain exact historical
result/receipt associations and do not denote current ownership. Selected Task
views retain terminal stages; FeatureArea overview does not keep the latest Task
as its permanent current workflow. Task history and Console Task states remain
terminal facts while the FeatureArea advertises continuation.

Contract tests cover three complete semantic Tasks with their owned
Reviews, narrower subsequent authority, retained dirty files, a later commit,
conversation reset, integration drift, separate concurrent areas, SQLite
creation races, worktree restart recovery, cancellation, orphan fencing and
retirement. All runtime validation uses deterministic fake backends or fixtures;
no paid Codex execution is needed.
