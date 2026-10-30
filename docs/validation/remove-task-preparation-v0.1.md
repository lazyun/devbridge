# REMOVE_TASK_PREPARATION_FROM_V0_1_ARCHITECTURE

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-ed55e774a2c6e3d0)。

This is a removal/validation record, not a historical product contract. The current
model is [Task and Execution, final v0.1](../architecture/task-execution-v0.1.md).

Status: **REMOVE_TASK_PREPARATION_FROM_V0_1_ARCHITECTURE_READY**.

## Repository boundary

- Starting branch: `dev_20260908_1913`; starting HEAD: `6beb7fcbdd1363c3da36914fd5df7b53c90d7fe0`.
- Task branch: `codex/remove-task-preparation-v0-1`.
- Task worktree: `/private/tmp/devbridge-remove-task-preparation`.
- Final HEAD: `6beb7fcbdd1363c3da36914fd5df7b53c90d7fe0`; implementation remains uncommitted in this isolated worktree. No workflow-required Implementation commit was attached to this manual architecture task.
- Fresh baseline was clean, schema 42, with no release tag/compatibility commitment found. The original checkout remains at the baseline and clean.
- No development/WebUI merge, rebase, push, release, live DataRoot mutation or paid Codex turn was performed. Other worktrees were not edited. Existing Desktop frontend dependencies were read to build this worktree.

## Final model and deletion

Schema version remains **42**, with one canonical final DDL. Fresh installation
and actual v41 migration use that DDL. The obsolete already-version-42 development
schema is rejected before allocating a runtime epoch, with explicit recreation
guidance. Unsupported source entities containing data are rejected without
changing source data/version. There is no retired-operation reader, backfill,
kind mapping, optional compatibility column, API facade or v43 migration.

Removed the `internal/taskpreparation` package and SQLite aggregate store;
`task_preparations`/access-evidence storage; related indexes, triggers, FKs;
Run/TaskPlan/Execution/revision associations; PREPARATION kind and semantic owner;
ordinal, result, prompt and inspection-evidence prerequisites; claim, cancel,
recovery, finalization and runtime admission arms; `task_prepare`; automatic first
analysis/retry/handoff; Card/Web/Console/MCP fields and stage states.

The formal hierarchy is Project → FeatureArea → Task → AgentExecution → Run.
REVIEW, DEVELOPMENT and INSPECTION are the native kinds. Existing LEGACY_UNKNOWN
history remains read-only for unrelated unprovable ownership, with no new
runtime/mutation authority. WITHOUT ROWID identity, replay protection, exact Run
binding and canonical ownership checks remain intact.

TaskPlan retains independent authorization value in existing internal TaskPlan
flows; it is not a new product stage. Review Approval internally applies one
plan matching the immutable Task definition. ExecutionProposal retains its
Development authorization role; no second approval is added.

## Approval, continuation and scope

One Review Approval transaction creates the Development Task, immutable initial
TaskDefinitionRevision, confirmed Development Execution and universal identity.
An injected identity-insertion failure rolls back the entire materialization.
Cross-client replay returns the same receipt and Task.

The existing write-side reconciliation loop scans bounded batches of confirmed
approved Executions with no Run, then calls canonical Start. It retries only
unreserved work. No client/GPT/UI read or second Start action is needed. A restart
between materialization and reservation resumes the same Execution. Concurrent
sweeps reserve exactly one Implementation Run; optional Verification uses that
same Execution. After reservation, launch failure, crash and finalization remain
the original runtime/recovery protocol and are never a new automatic attempt.

Exact Approval/revision/provenance, cancellation, retirement, same-FeatureArea
claim, recovery and UNKNOWN/UNKNOWN_ORPHANED fences remain canonical prerequisites.
Cancellation before start prevents reservation; after reservation it targets the
exact Run. No new scheduler, finalizer, authority source or behavioral audit was added.

Coding Agent engineering analysis now occurs inside Implementation. An anchored
`SCOPE_CONFLICT:` final result stops the Execution with FAILED and the authoritative
failure code/domain result SCOPE_CONFLICT. It does not run Verification or rewrite
the immutable Task revision. GPT reads the result and chooses new Task/Review/Approval.

## Verification

All runtime tests use deterministic fake/recording Codex backends and temporary
repositories/databases. Candidate gates run built Core, GPT MCP and Local MCP
processes with a fake app-server transport, including Web Approval and mounted Card.
Real paid acceptance gates were not run.

| Gate | Result |
| --- | --- |
| `go test ./... -count=1` | PASS |
| `go vet ./...` | PASS |
| Race: SQLite, execution, Local MCP; Approval, continuation, restart, cancel and claim cases | PASS |
| Packaged Approval/Web observability/unified cancellation/retirement | PASS (all four gates, final built binaries) |
| Fresh final42, actual v41→final42, FK/identity and rejected-source preservation | PASS |
| MCP discovery and current documentation contracts | PASS |
| `node --test internal/mcpserver/*_test.js internal/bridgehttp/*_test.js` | 260 PASS, 4 opt-in fixture cases; packaged Card cases run separately |
| Desktop `go test ./... -count=1` | PASS |
| Desktop frontend `npm test` | 43 PASS |
| Desktop frontend Vite build using existing installed dependency | PASS |
| gofmt and `git diff --check` | PASS for changed Go files and diff |

Legacy REAL-E2E-10/11 and their real runtime helper tests encoded the removed
aggregate and second-start protocol; those obsolete gates were deleted, not
skipped. Their current workflow coverage is in the packaged Approval/observability/
cancellation/retirement gates and Core runtime regression tests. Independent
normal execution, approval, interruption, workspace, verification, scope and
read-only Review harness gates remain. Historical migration DDL exists only in
explicitly marked `_test.go` source fixtures; it is excluded from the production
binary and used to validate actual source schemas.

Changed Go files are gofmt-clean. Six unrelated baseline files were already
not gofmt-clean (`internal/bridgehttp/console.go`, `internal/consoleapi/logs.go`,
`internal/controlplane/dataroot_integration_test.go`, `internal/document/service.go`,
`internal/document/service_test.go`, `internal/projectcontext/bootstrap.go`);
they are unchanged by this task.

## Audit and review inventory

- [Every changed/deleted/new file](archive/remove-task-preparation-v0.1.md#file-f4d0916cb0601890).
- [Every matching source line and classification](archive/remove-task-preparation-v0.1.md#file-1ed853699b00661a).
- [Conservative WebUI overlap candidates](archive/remove-task-preparation-v0.1.md#file-388ee919144360c9).
- Gate outputs are stored in the adjacent validation directory.

The required literal search covers TaskPreparation, task_preparation, task_prepare,
PREPARATION, TASK_PREPARING, PREPARATION_RUNNING, preparationId and taskPreparationId.
Production Go/SQL/API/workflow/tool/enum/UI assets have zero matches. The final DDL contains 318 objects and zero retired-entity matches. The source-line audit classifies 216 matching lines with zero unclassified entries; 21 ordinary setup-language lines are listed separately. Remaining
matches are explicit historical validation/source fixtures, negative assertions
that forbid the deleted contract, and removal-report evidence. Natural-language
Run/worktree/Project Context/response “preparation” means ordinary runtime setup,
not a Task aggregate or analysis stage; its locations are separately enumerated.
No production data is read or remapped through those validation fixtures.

The currently listed tasks/local branches do not identify a distinct active
parallel WebUI worktree. Therefore the overlap file conservatively enumerates
all changed UI assets, presentation/API contracts, related tests and shared docs;
it does not claim a cross-branch conflict has been resolved. Wait for the user's
WebUI completion notice before comparing and merging branches.

Working tree: isolated implementation and validation artifacts are modified/new,
with deleted obsolete files; no staged files or commit. The source development
checkout is unchanged. Keep this branch/worktree for later integration.
