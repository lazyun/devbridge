# Project Inspection canonical runtime v1 closure

Status: `PROJECT_INSPECTION_CANONICAL_RUNTIME_V1_CLOSED`

This report closes the v82 Project Inspection canonical runtime. Recovery Status Inspection G10 was outside that release and was later closed by the v83 integration recorded in [Project Inspection Recovery Status G10 closure](codex-recovery-status-inspection-g10-closure.md). Neither release claims hostile sandbox proof or exactly-once response delivery.

## Repository boundary

| Fact | Starting | Final |
|---|---|---|
| Branch | `dev_20260908_1913` | `dev_20260908_1913` |
| HEAD | `fd8a6afeb213972e8aed00ff23f090ae058b8530` | `fd8a6afeb213972e8aed00ff23f090ae058b8530` |
| Schema | v81 | v82 |
| Index | empty | empty |
| Dirty status entries | 391 inherited | 415 per-file entries |
| Task byte delta from the opening snapshot | n/a | 56 paths: 38 modified, 18 added, 0 deleted; +247,864 bytes |

The opening snapshot contains 1,885 SHA-256 manifest entries at `/private/tmp/devbridge-project-inspection-baseline.W3k6hb`. The increment above compares non-ignored repository files byte for byte with that snapshot. No file was staged, committed, merged, pushed, reset, cleaned, checked out, or rebased.

## Runtime contract

The existing Project-scope Inspection Task remains the business owner. It has `scope=PROJECT`, no FeatureArea, and no fabricated TaskDefinitionRevision or Execution. The concrete runtime owner is `PROJECT_INSPECTION:<contextProbeId>` with `(projectId, inspectionOwnerTaskId, inspectionOperationId)` as its full identity.

Schema v82 adds owner variants to the existing systems:

- AgentConversation: `FEATURE_AREA` or `PROJECT_INSPECTION`.
- claim: the existing FeatureArea claim table remains unchanged; Inspection uses `project_inspection_agent_claims` keyed by operation.
- Workspace: `FEATURE_AREA` or `PROJECT_INSPECTION`; a Project Inspection workspace has no scalar repository or FeatureArea authority.
- RuntimeWorkset: protocol v1 and its v77 hash remain unchanged; Project Inspection uses protocol v2 with explicit owner, exact member set, read scopes, `write=NONE`, runtime roots, backend and model provenance.
- `run_repository_bindings`: Review and Execution history is preserved, while Inspection bindings identify the exact operation, workspace, members, origins, worktrees, and observations.

Prepare is a pure read. It validates the bound Project, exact explicit target repositories and scopes, primary member, current Git facts, backend fingerprint, model, effort, service tier, exact instruction bytes, request ID, and payload hash. Confirm atomically creates the existing Project Inspection owner Task, ContextProbe, and immutable receipt. Same request and payload replays; changed payload conflicts.

Start reserves one Run, one Project Inspection claim, and one canonical conversation before materialization. Dev Bridge then creates the exact managed workspace, members, and worktrees, freezes RuntimeWorkset v2, stores repository bindings and start observations, and only then starts the long-lived App Server, Thread, and Turn. The primary member selects cwd without changing the member set. Original registered checkouts remain inventory roots and are never runtime roots.

The caller's confirmed instruction bytes are sent unchanged to `turn/start`. The production serializer supplies a strict structured output schema. The canonical reducer requires terminal and quiescent backend facts, the current owner/Run/epoch/Thread/Turn/workset/target/report identities, no unresolved request or successor, and exact end observations. It atomically writes the result receipt, terminalizes the operation and conversation, and releases the claim. Mutation produces `SCOPE_CONFLICT` with preserved evidence; malformed output produces `FAILED`; durable cancellation wins a cancel-versus-terminal race and produces `CANCELED`.

Core restart reopens the same Run and reconnects the bound App Server and exact Thread/Turn. A pending `CODEX_REQUEST` reattaches and continues through immutable UserDecision. An UNKNOWN response disposition is not resent. A lost App Server becomes `RECOVERY_REQUIRED`; Core neither launches S2 nor fabricates completion. Reply-loss finalization reads the original receipt, and stale epochs are rejected.

Historical Inspection remains readable with `LEGACY_INSPECTION_NO_CANONICAL_RUNTIME`. Reading or attempting to start it creates no conversation, workspace, worktree, workset, App Server, or Recovery child. A new canonical operation remains available.

Core projections expose Project owner, selected repositories, primary member, workspace/workset, Run/Thread/Turn, pending request, cancellation/recovery, structured result, and legacy limitation. HTTP, MCP, Console, ChatGPT Card, WebUI, and the Desktop embedded workbench consume those facts. Refresh remains read-only and write endpoints cannot inject an outcome.

## G1–G14 acceptance

| Gate | Production evidence | Result |
|---|---|---|
| G1 owner | Prepare/confirm facade verifies one PROJECT Task, `FeatureArea=NULL`, and no revision/execution | PASS |
| G2 targets | app+sdk explicit receipt, backend excluded, primary member explicit, pure prepare, exact replay and forged receipt rejection | PASS |
| G3 workspace | two managed worktrees, unchanged source checkouts, partial member replay, and physical-worktree/metadata-gap recovery | PASS |
| G4 workset | v2 Inspection owner, exact roots/scopes/cwd/backend/model, no fake v1 owner fields, later workspace member cannot expand the frozen Run | PASS |
| G5 continuity | canonical conversation/claim, one S1/Thread/Turn, restart rejoin, lost-S1 fail-closed, same-owner serialization and different-owner independence | PASS |
| G6 result | production Client plus strict report schema, repository-qualified observations, canonical reducer, no engineering object side effects | PASS |
| G7 CODEX_REQUEST | real App Server request, cross-Core reattach, immutable user response, same Run/Thread continuation, UNKNOWN non-resend | PASS |
| G8 mutation | managed source mutation is observed and retained; final status is `SCOPE_CONFLICT` | PASS |
| G9 faults | reservation reopen, exact replay, cancellation, cancel-terminal race, stale callback, reply-loss readback, malformed report, lost S1 | PASS |
| G10 concurrency | eight same-owner replays converge; changed contender conflicts; different Inspections materialize separate roots; FeatureArea-first and Inspection-first retain distinct claim/conversation/workspace | PASS |
| G11 legacy | pure historical read, explicit limitation, no backfill/resume/Recovery source, new canonical operation still works | PASS |
| G12 clients | HTTP/MCP/Console projection tests, Product UI test, two Inspection Card tests, Desktop frontend 49/49 | PASS |
| G13 future source | terminal owner, source Run/Turn/epoch/workset/conversation/S1/workspace observations read back; zero Recovery children | PASS |
| G14 history | v76–v81 migration history, multi-repository Upper/Coding/Direct/UserDecision paths, root/Desktop compile, vet/race/JS/diff checks | PASS with inherited failures listed below |

## Verification

The offline native matrix used the installed Codex 0.149.0 App Server binary, production Client serializer and local deterministic provider. It used three temporary Git repositories and two managed runtime worktrees. It did not call a cloud model.

| Verification | Actual matches | Result |
|---|---:|---|
| Canonical API/store/native matrix | 4 top-level + 8 subtests | PASS |
| `probeexecution` package | 7 top-level + 25 subtests | PASS |
| Primary race matrix | 7 top-level + 20 subtests | PASS |
| lost-S1 and cancel-terminal race delta | 1 top-level + 2 subtests | PASS |
| Inspection/FeatureArea two-order concurrency | 1 top-level + 2 subtests | PASS |
| G3/G4/migration/history focused set | 9 top-level | PASS |
| HTTP/MCP projection focused set | 3 top-level | PASS |
| historical multi-repository/UserDecision focused set | 6 top-level + 7 subtests | PASS |
| Desktop frontend | 49 tests | PASS |
| Product UI and Inspection Card JS | 3 tests | PASS |
| real Codex Project Inspection smoke | 1 test | NOT_RUN (explicit opt-in absent) |

Representative commands:

```text
DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY=/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex go test ./internal/controlapi -run '^TestProjectInspectionOfflineNativeRuntime$' -count=1 -v
go test -race ./internal/controlapi -run '^(TestProjectInspectionClaimAndConversationConcurrency|TestProjectInspectionLegacyReadOnlyRuntimeBoundary|TestProjectInspectionReservedPreparationReplay|TestProjectInspectionOfflineNativeRuntime)$' -count=1
go test ./internal/contextprobe ./internal/probeexecution ./internal/workset ./internal/workspace ./internal/worktree -count=1
go test ./internal/storage/sqlite -run '^(TestProjectInspectionV82MigrationRollbackRepeatAndFK|TestRecoveryStatusV81PreservesPopulatedOperationHistory|TestV82MigrationPreservesFeatureAreaMultiRepositoryWorkspaceMaterialization|TestWorkspaceStorePersistsProjectInspectionOwnerWithoutFeatureArea)$' -count=1
go vet ./internal/contextprobe ./internal/probeexecution ./internal/workset ./internal/workspace ./internal/worktree ./internal/storage/sqlite ./internal/controlapi ./internal/bridgehttp ./internal/mcpserver
go build ./...
(cd desktop && go test ./... -run '^$' -count=1)
(cd desktop/frontend && npm test && npm run build)
node --test --test-name-pattern='Project inspection|Inspection summary' internal/mcpserver/dev_bridge_ui_test.js
node --test --test-name-pattern='inspection|Inspection' internal/bridgehttp/product_ui_test.js
git diff --check
```

The real-model smoke was deliberately run without its opt-in and reported `NOT_RUN: DEVBRIDGE_REAL_CODEX_PROJECT_INSPECTION=1 is required`. That skip is not counted as runtime acceptance.

### Inherited failures

The opening focused baseline contained 27 passing and 51 failing top/subtests. The failures were already present before this task: SQLite `TestWorkspaceAndRunContinuitySurviveReopen`, plus controlapi workspace/card fixtures rejected by `PERMISSION_DENIED` or `SESSION_INITIALIZATION_REQUIRED`. The task repaired the shared HTTP console fixture needed by its own production route.

The broad `internal/mcpserver/dev_bridge_ui_test.js` suite still has 27 pass / 79 fail from the inherited Card contract/fixture divergence. The three Project Inspection client checks are green and the production scripts pass syntax checks. This inherited broad suite is not counted as Project Inspection acceptance.

The full Desktop Go runtime suite retains one inherited failure, `TestDesktopProbeSelectionReachesProductionReviewWithMinimalPATH`, caused by `SESSION_INITIALIZATION_REQUIRED`. Desktop module compile succeeds. Root `go build ./...` succeeds; the read-only module cache emits a non-fatal stat-cache warning.

## Retained status vocabulary

- `PARTIAL_IMPLEMENTATION`
- `CODEX_RECOVERY_STATUS_RECONCILIATION_V1_PARTIAL_IMPLEMENTATION`
- `CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`
- `CODEX_REQUEST_USER_DECISION_V1_CLOSED`
- `CODEX_PENDING_REQUEST_CROSS_CORE_REATTACH_CLOSED`
- `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`
- `MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`
- `TRUSTED_EXECUTOR_MODEL_V1`
- `CODEX_SANDBOX_HARDENING_DEFERRED`
- `REAL_CODING_AGENT_E2E_NOT_RUN`
- `REAL_CODEX_PROJECT_INSPECTION_E2E_NOT_RUN`
- `SAFE_REPOSITORY_UNLINK_CLOSED`
- `MANUAL_DESKTOP_ACCEPTANCE_PENDING`
- `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`

At the time of this v82 report, Recovery Status reconciliation and the G10
Inspection integration were still open. They were subsequently closed by
[`codex-recovery-status-inspection-g10-closure.md`](./codex-recovery-status-inspection-g10-closure.md).
This report still does not claim `EXACTLY_ONCE_CODEX_RESPONSE_PROVEN`,
`HOSTILE_CODEX_SANDBOX_PROVEN`, or
`PROJECT_CENTRIC_MULTI_REPOSITORY_WORKSPACE_IMPLEMENTED`.
