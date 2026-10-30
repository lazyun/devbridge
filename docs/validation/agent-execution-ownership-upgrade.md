> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

# Agent Execution ownership upgrade — validation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-9e77964d746a6499)。

Historical implementation report. The subsequent [closure validation](agent-execution-ownership-closure.md) supersedes its final readiness claims and file counts.

## Baseline and delivery

- Date: 2026-09-10.
- Freshly verified branch: `dev_20260908_1913`.
- Freshly verified HEAD: `1d16dc7feaac471fec705e4d810aeac7dc9d05a1`.
- Starting working tree: clean. Historical baseline happened to match the fresh HEAD.
- Source schema: v41 → v42. Only temporary test databases were migrated.
- Final branch and HEAD remain unchanged; implementation is an uncommitted working-tree change.
- 75 changed/new files are listed below.

## Delivered contract

`Task → AgentExecution → Run` is durable before a production backend handoff.
DEVELOPMENT uses the existing proposal ID and groups Implementation and optional
Verification. REVIEW, PREPARATION, and INSPECTION have independent immutable
identities, with their existing semantic owners retained.

The two production SQLite Run INSERT sites are `run_store.go` and
`inspection_run.go`; both insert/bind the envelope in the existing admission
transaction. The v42 Run INSERT guard rejects absent or mismatched associations
even for raw SQL. DEVELOPMENT envelope creation is an AFTER INSERT trigger on
`execution_proposals`, inside authorization/READY materialization transactions.

The only production thread/start, thread/resume, and turn/start callers are
Coordinator and the Inspection runner. Both require a durable validation
capability. Coordinator additionally proves the active claim, epoch, exact
AgentConversation boundary and outgoing Thread identity before dispatch.
A compatibility store without this capability cannot reach a backend. Recording
client and real-SQLite/fake-client tests verify zero backend calls for missing,
wrong, stale, unknown, or unbound ownership. Unproven entry rejection cannot
obtain terminalization or cancellation authority through cleanup callbacks.

No second persisted workflow or cancellation lifecycle was added. The shared
Core reducer feeds Task.Work, CurrentWorkflow, workflowpresentation,
FeatureArea details, MCP/Card, Console, and the four-kind Web Execution inventory.
`executionId` is universal; `executionProposalId` remains DEVELOPMENT-only.
Domain owner IDs retain their cancellation and recovery meaning.

## Migration and historical authority

- Fresh schema creation, actual v41 upgrade, four-kind v41-equivalent historical
  fixtures, and schema replay pass.
- Exact owner facts map deterministically. DEVELOPMENT preserves proposal IDs;
  Implementation and Verification preserve the same universal association.
- Migration tests preserve Run identities, status, timestamps and Thread facts,
  and `PRAGMA foreign_key_check` returns no violations.
- Missing provenance becomes explicit `LEGACY_UNKNOWN`, with no fabricated owner.
  Unknown history is readable and immutable; it cannot start a backend or gain
  Host Tool, claim, cancellation, or recovery mutation authority.
- Existing cancellation receipt replay and offline interruption paths recheck
  exact non-unknown ownership. A regression verifies that a legacy pending receipt
  cannot terminalize a safe historical owner, delete its claim, or change its receipt.
- Native UNKNOWN keeps its claim until canonical recovery. UNKNOWN_ORPHANED keeps
  quarantine and permanent stale-write fencing and does not prove backend death.
  Unknown-provenance historical claims remain blocked/read-only rather than being
  silently authorized by migration.
- Existing proposal package/table and legacy fields remain compatible. Inspection
  retains Project scope; its unsupported cancellation action remains unavailable.

## Validation results

- `DEVBRIDGE_REAL_RUNTIME_ACCEPTANCE=0 DEVBRIDGE_REAL_CANCEL_ACCEPTANCE=0 go test ./...`: passed.
- Full affected-package race run: passed for storage/sqlite, agentexecution, run,
  review, taskpreparation, contextprobe, probeexecution, executionproposal,
  execution, agentconversation (no tests), controlapi, workflowpresentation,
  mcpserver, consoleapi, and bridgehttp.
- Subsequent changes received targeted race coverage: legacy cancellation replay,
  exact Inspection correlation, universal cancellation, READY selection, Card
  schema/identity, and the historical snapshot fixture. All passed.
- Four-kind upgrade/dual-Run migration supplemental test: ordinary and race passed.
- `go vet ./...`, `gofmt` verification, and `git diff --check`: passed.
- Node DOM/browser/frontend contracts: **303 passed, 3 skipped, 0 failed** across
  `internal/bridgehttp/*test.js`, `internal/mcpserver/*test.js`, and
  `desktop/frontend/test/*.test.js`.
- The three skipped tests require externally supplied `DEVBRIDGE_CARD_SNAPSHOTS`
  for mounted candidate Review/Preparation/Approval replay. No live-browser or
  production-runtime test was substituted for these optional fixtures.
- Offline ChatGPT UI build validation passed for both self-contained resources.

The historical workflow snapshot fixture now finishes retirement of its 101
historical FeatureAreas through the canonical retirement coordinator before
reading current work. This eliminates background project-event churn observed
in failing snapshots; the four-attempt consistency guard remains unchanged.
The corrected targeted test passed eight consecutive runs.

## Acceptance evidence

| Scenario | Evidence |
| --- | --- |
| Fresh DB / v41 upgrade / replay | SQLite migration suite and `TestAgentExecutionV41UpgradeAndLegacyReadOnly` |
| All historical kinds / Development dual Run | `TestAgentExecutionV41UpgradeAllKindsAndDevelopmentRunIdentity` |
| Review / Preparation / Development admission | `TestAgentExecutionAdmissionAndExactValidation`, SQL missing-association rejection |
| Automatic first Preparation | Approved-first admission, bootstrap/retry, rollback and retirement suites |
| Preparation success + domain CONFLICT | `TestAgentExecutionPreparationConflictRemainsVisible`, reducer and preparation conflict tests |
| READY atomic materialization | TaskPlan materialization atomicity/crash-window tests; pending DEVELOPMENT projection test |
| Implementation → Verification | ExecutionProposal lifecycle asserts the same AgentExecutionID for both Runs |
| Inspection ownership / no FeatureArea claim | `TestProjectContextProbePersistsBeforeUnclassifiedTaskCreation`, Inspection admission/dispatch tests |
| Wrong/missing/stale ownership → zero backend | `TestDurableAgentExecutionRejectsCorruptHandoffBeforeCodex`, wrong resume target and recording tests |
| Shared product projection | Four-kind HTTP inventory versus Core/Task.Work comparison; MCP/Card identity/schema tests |
| Same-FeatureArea serialization | Claim pair/race, admission rollback, ownership lifecycle race suites |
| Cancel / late callback / lost response / replay | Existing execution and SQLite cancellation/runtime safety suites plus legacy receipt regression |
| UNKNOWN / UNKNOWN_ORPHANED | Recovery, orphan rollback/permanent authority, exact orphan projection tests |
| Legacy read-only / no new authority | Migration tests, unknown inventory read test, Host Tool/runtime and cancellation guards |

## Operational boundaries and cleanup

No live SQLite was modified, no official port 8081 runtime was restarted or
replaced, and no real paid Codex Turn was started. No physical device or simulator
was used. No push, release, or commit was made.

Task-specific temporary UI build output, logs, and agent Go caches are removed
at delivery. The pre-existing shared Go cache and pre-existing repository build
artifacts are not cleared.

## Changed files

- `docs/architecture/agent-execution-ownership.md`
- `docs/validation/agent-execution-ownership-upgrade.md`
- `internal/agentexecution/agentexecution.go`
- `internal/agentexecution/projection.go`
- `internal/agentexecution/projection_test.go`
- `internal/bridgehttp/agent_execution_inventory_test.go`
- `internal/bridgehttp/product_console_test.go`
- `internal/bridgehttp/ui/product-app.js`
- `internal/consoleapi/agent_executions.go`
- `internal/consoleapi/product.go`
- `internal/consoleapi/product_execution_cancel.go`
- `internal/contextprobe/probe.go`
- `internal/controlapi/agent_execution_cancel.go`
- `internal/controlapi/agent_execution_read.go`
- `internal/controlapi/agent_executions.go`
- `internal/controlapi/agent_executions_test.go`
- `internal/controlapi/api.go`
- `internal/controlapi/current_workflow_integration_test.go`
- `internal/controlapi/errors.go`
- `internal/controlapi/execution_cancel.go`
- `internal/controlapi/feature_area_details.go`
- `internal/controlapi/feature_area_presentation.go`
- `internal/controlplane/ai_context_test.go`
- `internal/controlplane/bootstrap.go`
- `internal/execution/agent_execution.go`
- `internal/execution/agent_execution_sqlite_test.go`
- `internal/execution/agent_execution_test.go`
- `internal/execution/coordinator.go`
- `internal/execution/coordinator_test.go`
- `internal/execution/errors.go`
- `internal/execution/runtime_supervisor.go`
- `internal/execution/runtime_supervisor_test.go`
- `internal/mcpserver/agent_execution_projection.go`
- `internal/mcpserver/agent_execution_projection_test.go`
- `internal/mcpserver/context_tools.go`
- `internal/mcpserver/decision_ui.html`
- `internal/mcpserver/execution_cancel.go`
- `internal/mcpserver/external_projection.go`
- `internal/mcpserver/external_schema.go`
- `internal/mcpserver/run_tools.go`
- `internal/mcpserver/service.go`
- `internal/mcpserver/ui_workflow_state_tool.go`
- `internal/probeexecution/runner.go`
- `internal/probeexecution/runner_test.go`
- `internal/recovery/recovery.go`
- `internal/review/review.go`
- `internal/run/execution_validation_test.go`
- `internal/run/memory_store.go`
- `internal/run/observability.go`
- `internal/run/run.go`
- `internal/run/service.go`
- `internal/storage/sqlite/agent_execution_migration.go`
- `internal/storage/sqlite/agent_execution_projection_store.go`
- `internal/storage/sqlite/agent_execution_projection_store_test.go`
- `internal/storage/sqlite/agent_execution_store.go`
- `internal/storage/sqlite/agent_execution_store_test.go`
- `internal/storage/sqlite/agent_process_migration_test.go`
- `internal/storage/sqlite/agent_recovery_store.go`
- `internal/storage/sqlite/cancellation_owner.go`
- `internal/storage/sqlite/context_probe_store.go`
- `internal/storage/sqlite/execution_cancel_store.go`
- `internal/storage/sqlite/execution_proposal_store_test.go`
- `internal/storage/sqlite/inspection_run.go`
- `internal/storage/sqlite/integration_test.go`
- `internal/storage/sqlite/migrate.go`
- `internal/storage/sqlite/run_store.go`
- `internal/storage/sqlite/runtime_epoch.go`
- `internal/storage/sqlite/runtime_safety_legacy_test.go`
- `internal/storage/sqlite/task_store.go`
- `internal/storage/sqlite/work_envelope.go`
- `internal/task/task.go`
- `internal/workflowpresentation/details.go`
- `internal/workflowpresentation/projection.go`
- `internal/workflowprogression/schema.go`
- `internal/workflowprogression/schema_test.go`
