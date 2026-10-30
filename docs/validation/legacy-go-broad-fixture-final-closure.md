# Legacy Go broad fixture final closure ledger

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-1c8c97f7317ab25b)。

Date: 2026-09-18

## Baseline

- Branch: `dev_20260908_1913`
- HEAD: `9f606647e4bb5bcbff54ab568b123d142042a064`
- Schema: v84
- Index: empty
- Opening non-ignored byte manifest: `/private/tmp/legacy-go-final-baseline.sha256`
- Opening manifest SHA-256: `1cebd8a52e8c99ceeabb7cdb37543faf4807be1557393ded23c6bbf16aa4f5a3`
- Opening JavaScript/Card protection manifest: `/private/tmp/legacy-go-final-protected-js.sha256`
- Protection manifest SHA-256: `58abcda7c394d77157f7d15257c351e2c4667983fb5e5889f46eecf12da98201`

## Matrix and the prior result discrepancy

The root module and `desktop` are separate Go modules. A prior cached or focused package result described as “root Go full” did not exercise the same current dirty tree and command matrix as an uncached `go test ./... -count=1`. The formal matrix therefore fixes working directory, disables the real-Codex opt-ins, disables module network access, and forces test execution. Package-focused probes remain additional entries; they are not evidence that the root aggregate passed.

| Entry | Result | Evidence / disposition |
| --- | --- | --- |
| Root `internal/mcpserver` | PASS | `go test ./internal/mcpserver -count=1 -timeout=8m`; real SDK discovery, loopback transport, SQLite startup wire, Session scope, and catalog tests passed. |
| Root vet | PASS | `go vet ./...` with offline module settings. |
| Desktop vet | PASS | `go vet ./...` from `desktop`. |
| Desktop default aggregate | PASS | `go test ./... -count=1 -timeout=8m` passed under normal package parallelism with the unchanged three-second local `--version` contract. The earlier sandbox run was an environment-only loopback bind denial. |
| Desktop serialized aggregate | PASS | `go test -p 1 ./... -count=1 -timeout=5m`; every Desktop package passed. |
| Root aggregate | FAIL / inventory expanded | `GOCACHE=/private/tmp/devbridge-root-final-unsandboxed-cache GOPROXY=off GOSUMDB=off go test ./... -count=1 -timeout=8m` ran to natural completion. `internal/execution` passed, while current default failures remain in `internal/localmcp` and `internal/storage/sqlite`; closure remains open. The preceding sandbox run was discarded after loopback/Unix socket bind denials. |

## Classified findings

| Surface | Primary category | Evidence | Disposition |
| --- | --- | --- | --- |
| Model MCP catalog and interface docs | `FIXTURE_DRIFT` | Tests and docs still listed direct model creation/start tools after purpose-confirmation migration. | Tests and canonical interface catalog now match fresh production discovery; retired tools remain retired. |
| MCP Session fixtures | `FIXTURE_DRIFT` | Direct `OpenSession`/unscoped sessions produced `SESSION_INITIALIZATION_REQUIRED` or caller-scope mismatch. | Migrated affected MCP fixtures to initialize/bind with the request caller scope. |
| MCP runtime request payload tests | `ASSERTION_SUPERSEDED` | Tests required optional presentation projection and compared enrichment as backend request bytes. | Exact backend DTO is compared after removing optional read-side projection. Raw CODEX_REQUEST responses remain prohibited. |
| FeatureArea startup composition | `PRODUCTION_DEFECT` | Startup froze `CodexConfigID` but omitted its backend fingerprint, so strict Review admission rejected the canonical composition. | Startup preparation freezes `BackendSelection` from the same enabled selected config. Real SQLite + SDK wire and full MCP package pass. |
| Root BridgeHTTP fixtures | `FIXTURE_DRIFT` | Fixtures used retired Session setup, incomplete Review runtime identity, and requests without actual listener transport facts. | Repaired; full `internal/bridgehttp` passes with shared Session, frozen Review, operator ingress, and AgentConversation continuity helpers. |
| Root Control API fixtures | `FIXTURE_DRIFT` | Unit stubs and integration fixtures omitted current caller ownership, Review runtime, and exact Task action facts. | Repaired; focused and full package runs reduced to the migrated current contracts without relaxing production fences. |
| Root Control Plane fixtures | `FIXTURE_DRIFT` | Bootstrap and workflow fixtures used retired Session setup and legacy Review arguments. | Repaired; full `internal/controlplane` passes. |
| Root Execution cancellation fixture | `FIXTURE_DRIFT` | Durable `ClaimInterrupt` succeeded with `dispatch=true` and the exact current Run/Thread/Turn, but the fake backend callback still required the retired literal Thread `thread-cancel`. The authoritative FeatureArea conversation correctly resumed `thread-1`; the stale callback returned `wrong backend target` before opening its observation gate. | Repaired the callback to compare the dispatched Thread/Turn with the cancellation-before-read authoritative Run identity. Both implementation and verification cancellation subtests now call the backend exactly once and pass without changing production fencing. |
| Other Root Execution fixtures | `FIXTURE_DRIFT` | Direct tests omitted current Session-first binding, frozen backend selection, atomic Thread/Run binding, or assumed a fresh Thread where FeatureArea continuity resumes the authoritative Thread. | Repaired; focused cancellation and the complete `internal/execution` package pass. The long SQLite prompt fixture remains byte-exact but keeps its prerequisite Review response inside the bounded evidence envelope; the full-limit case remains covered by the unit prompt test. |
| Desktop parallel probe | `ENVIRONMENT_LIMITATION` | A sandboxed run could not bind test-only loopback listeners. | Unsandboxed default package-parallel aggregate passes; no timeout or production probe behavior changed. |
| Candidate-host snapshots | `OPT_IN_NOT_RUN` | Requires explicit candidate-host environment and is outside the default offline suite. | Retained as opt-in. |

## Integrity

- The opening JavaScript/Card protection manifest still verifies byte-for-byte.
- `git diff --check` passes.
- The index remains empty.
- No live DataRoot, runtime, credentials, Git refs, real model, or paid service was used.

This ledger deliberately does not report the three requested closure markers while the root aggregate contains repairable fixture failures.

## 2026-09-18 cancellation interrupt claim result

- `TestExecutionCancellationImplementationAndVerification`: PASS for implementation and verification.
- `go test ./internal/execution -count=1 -timeout=8m`: PASS.
- Production cancellation, runtime authority, and SQLite interrupt-claim code are unchanged by this repair.
- Root aggregate reached natural completion and still reports repairable default fixture failures in `internal/localmcp` and `internal/storage/sqlite`; therefore the broad closure markers remain intentionally unreported.

## 2026-09-19 LocalMCP and SQLite final-closure continuation

- Opening byte manifest: `/private/tmp/legacy-localmcp-sqlite-opening.sha256` (`81080f1286047137ad02aa9aa08f416e37e8a2a3fb045012dcf584959fa4008a`).
- SQLite opening inventory: 121 failing tests/subtests. Shared causes were current Review author/backend/runtime fields, Session-first fixtures, retired scope-only Direct File grants, implicit-start assertions, runtime provenance, and historical-schema reader drift.
- `go test ./internal/storage/sqlite -count=1 -timeout=8m`: PASS after fixture migration. Historical migration files and production SQLite fences were not changed.
- A retained Local MCP direct Coding Review command had no way to supply the backend selection already required by Core. This was classified `PRODUCTION_DEFECT`; its closed input now requires exact backend config ID and fingerprint for Coding Reviews, validates both against current Core configuration, freezes the result, and includes it in the request payload hash. GPT confirmation DTOs are unchanged.
- The latest unsandboxed combined package run passed SQLite but still found repairable `FIXTURE_DRIFT` / `ASSERTION_SUPERSEDED` families in `internal/localmcp`, plus one stale current-catalog assertion in `internal/mcpserver`. The remaining LocalMCP failures use retired GPT `task_create` / `decision_respond` paths, incomplete operator ingress, outdated cancellation identities, or old Upper Review lifecycle expectations.
- Broad closure markers remain intentionally unreported until LocalMCP and MCP server packages, followed by the complete Root aggregate, pass naturally.

## 2026-09-19 final result

The remaining default failures are closed. No unclassified failure remains.

| Surface | Primary category | Final disposition |
| --- | --- | --- |
| LocalMCP GPT creation and decision fixtures | `FIXTURE_DRIFT` | Retired direct GPT mutations now use the production purpose-confirmation flow. Local retained commands continue to exercise their typed production handlers. |
| Local Coding Review start and Review approval | `PRODUCTION_DEFECT` | The retained Local schemas now accept exact backend config ID/fingerprint evidence where Core already required it. Core verifies the enabled config and freezes the selection; Upper Review rejects this Coding-only field. |
| Local/Web Task action context | `PRODUCTION_DEFECT` | Project-bound operator context is preserved through Task binding. Exact revision-bound Task acceptance commands delegate authority to the canonical operator command owner, which atomically revalidates the current gate. |
| Cancellation cross-surface replay | `ASSERTION_SUPERSEDED` | Requester identity is part of the immutable request. Fixtures read the exact original operator receipt instead of resubmitting its request ID through another principal. |
| Upper Review fixtures | `FIXTURE_DRIFT` | Fixtures now use exact Upper version fencing, explicit access prepare after recovery, pre-snapshot forbidden paths, and the current no-Coding-backend schema. |
| Upper projection/scope serialization | `PRODUCTION_DEFECT` | Uninitialized Upper access is omitted, v1 prepare emits its protocol version, and repository-wide empty scope is serialized as `[]`, never `null`. |
| Three-Task workspace continuity | `ASSERTION_SUPERSEDED` | The fixture creates each Task sequentially, submits canonical Review results, resolves each formal Review gate, and verifies all three Reviews share the exact workspace and baseline without inheriting Task authority. |
| Final Acceptance parity | `FIXTURE_DRIFT` | GPT uses closed EXECUTION confirmation, Local uses the retained typed decision, and Web uses exact operator targets plus explicit runtime selection for an authored successor. Distinct requester receipts are not treated as cross-principal replays. |
| Desktop three-second probe retry | `ENVIRONMENT_LIMITATION` | The first run occurred while the volume had only about 403 MiB free after isolated build caches accumulated and one probe timed out. Only this run's named caches were removed; the unchanged default package-parallel aggregate then passed. |

Final gates:

- `go test ./internal/localmcp -count=1 -timeout=8m`: PASS (`127.679s` after the last fixture changes).
- `go test ./internal/mcpserver ./internal/storage/sqlite -count=1 -timeout=8m`: PASS (`26.952s`, `84.341s`).
- Root `go test ./... -count=1 -timeout=8m`: PASS on the final byte state to natural completion; LocalMCP `161.189s`, SQLite `107.911s`, and every other package passed.
- Root `go vet ./...`: PASS.
- Desktop default package-parallel `go test ./... -count=1 -timeout=8m`: PASS after releasing this run's cache pressure.
- Desktop `go vet ./...`: PASS.
- JavaScript/Card protection manifest: PASS for all 70 entries.
- `git diff --check`: PASS; index empty.

`LEGACY_LOCALMCP_AND_SQLITE_FIXTURE_FINAL_CLOSED`

`LEGACY_GO_BROAD_FIXTURES_REPAIRED`

`LEGACY_BROAD_FIXTURES_REPAIRED`

`LEGACY_BROAD_FIXTURE_TRIAGE_CLOSED`
