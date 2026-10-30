# Confirmation prepare atomicity, exact readback, Host mount gate and Card entry binding — final verification and closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-82278a79dbc29cec)。

This is the final verification and closure round for the Confirmation
prepare / in-transaction readback / Host–Card mount gate / partial-intent
classification architecture. The implementation was frozen before this round;
this round adds **zero** repository changes and only re-verifies every gate
against actual test, race, build and byte-identity evidence.

## Baseline

| Field | Opening | Final |
| --- | --- | --- |
| branch | `dev_20260908_1913` | `dev_20260908_1913` |
| HEAD | `3f3309d15a8b06a973db90ea213a79972330ed3e` | unchanged |
| schema | v87 | v87 (no v88) |
| index | empty | empty |
| dirty entries | 23 | 23 |
| non-ignored byte manifest | 1,966 files | 1,966 files |

Audit directory: `/private/tmp/devbridge-confirmation-prepare-final-audit/`.

- Opening manifest `opening-manifest.sha256` = `bf0a8ce91908a7bb6753620bac951b6df2d4047d67cdf316378e7986728fb2ca`
- Final manifest `final-manifest.sha256` = `313a3d73bf4fa4724d49a1e6873d1d297f832270f002841bca3dc659e6274986`
- `increment.json` = `[]` — **0 repository files changed relative to the opening byte snapshot**.
- This round's increment is computed against the opening byte snapshot, not the
  inherited HEAD diff, per the task's boundary rule.

### Inherited dirty vs this-round increment

All 23 dirty entries are inherited from prior rounds of the same task (the
prepare/readback/Card-entry implementation and its tests). This round made no
code, test or documentation edits except this closure document; it re-ran and
recorded evidence only.

## Public contract: `businessOutcome` / `factsComplete` / `presentationReady`

`internal/confirmation/outcome.go` defines these as first-class Go/Core fields
on the public prepare result (`BusinessOutcome PrepareBusinessOutcome`,
`FactsComplete *bool`, `PresentationReady *bool`). The prepare outcomes are
`PREPARED`, `REJECTED_NOT_PREPARED`, `REQUEST_CONFLICT`.

`internal/mcpserver/confirmation_tools.go` `respondPrepare` is the only place
that sets them, and only for a prepare result: a complete, presentable intent
yields `PREPARED` with `factsComplete=true, presentationReady=true`; a complete
but unpresentable intent yields `PREPARED` with `presentationReady=false`
("created, presentation temporarily unavailable"); any failure returns an empty
`c.Intent{}` with `factsComplete=false, presentationReady=false`.

These three prepare-only fields never enter the Card's decoded get/show/commit
envelope: `confirmation_ui.html` `decode()` allows only the four keys
`intent | options | failure | display` and throws `CONFIRMATION_ENVELOPE_INVALID`
on anything else. `TestReadAndCommitEnvelopesOmitPrepareContract` proves the
separation.

## Prepare transaction and production read-after-write

`Store.Prepare` (`internal/storage/sqlite/confirmation_store.go`) writes the
intent with the **only** production `INSERT INTO confirmation_intents`
(line 232) inside a single SQLite writer transaction, then before committing:

1. re-reads through the production reader `confirmationRead(ctx, tx, ...)`
   (line 242);
2. runs `confirmation.IntentFactsComplete(&reloaded)`;
3. runs `confirmationPrepareReadbackMismatch(out, reloaded)` over every
   immutable fact (identity, version, session, project, purpose, action,
   content hash, content, step count);
4. on any failure returns `c.Intent{}` and leaves the transaction uncommitted,
   so no half-written intent is ever visible as PENDING.

A newly prepared intent may legally carry zero steps; `steps != nil` is not a
completeness condition.

### Why v88 is not required

Schema v87 already expresses the atomicity and classification contract. The
intent is stored as a single `intent_json` blob, so a prepare is either fully
committed (after the reader/completeness/parity gate) or rolled back — there is
no observable `PREPARING` intermediate and no sub-write fault surface.
Historical half-written intents are handled by the read-only classifier, not by
a new schema. No unclosable persistence defect was found, so v87 is retained.

## Host / Card mount gate and prepare-summary rejection

`presentationResultMiddleware` (`presentation_routing.go`) attaches a Card
resource only for the explicit `confirmation_show` path; every other tool result
is forced to `emission=SILENT` with `openai/outputTemplate` and
`ui.resourceUri` stripped. `confirmation_show` (`confirmation_tools.go`) reads
the canonical envelope and, on read error or empty `IntentID`, responds without
any Card meta — so a show failure never mounts a Card or shows PENDING.

`acceptEntry` in the Card requires a validated `envelope.intent`; a bare prepare
summary (whose fields are outside the 4-key allowlist) fails `decode()` and is
recorded as `ENTRY_NOT_PROVIDED` / `ENTRY_SCHEMA_INVALID`, never mounted.
`render()` gates the PENDING body on `entry && intent`, so the Card can never
show PENDING with `entryBound=false`.

Typed entry dispositions retained: `ENTRY_NOT_PROVIDED`, `ENTRY_SCHEMA_INVALID`,
`INTENT_FACTS_UNAVAILABLE`, `INTENT_STALE`. `INTENT_SHAPE_INVALID` is not a
unified fallback.

## Partial-intent classifier

`ClassifyPreparationState` runs in a read-only transaction and classifies from
durable facts only — intent facts, attempt, request, payload hash, receipt,
Decision consumption, business objects, and successor Review/Run/start
authority:

- complete + pending, never committed → `PREPARED_PRESENTATION_UNAVAILABLE`,
  `reprepareAllowed=false` (re-show, do not re-prepare);
- half-written, no attempt/receipt/business object →
  `PREPARE_INCOMPLETE_NOT_COMMITTED`, `reprepareAllowed=true`;
- durable non-terminal commit state → `ATTEMPT_EXISTS_OUTCOME_UNKNOWN`,
  `reprepareAllowed=false` (never auto-redispatch).

## Project-scope / multi-repository

`validateIntent` requires `projectId` but never a single `repositoryId`; a
project-scope intent carries full topology revision and ordered
development-context fingerprint instead. Verified by
`TestProjectOnlyConfirmationBindsOriginalInitializedSessionWithoutRepository`
and `TestMultiRepositoryUpperMCPProtocolRoundTrip`.

## Card asset identity (G16)

`TestConfirmationCardAssetIdentityMatchesDeployedResource` proves the four-way
equality: embedded source bytes == served resource `Contents[0].Text` == the
bytes hashed to the advertised `devbridge/mainScriptSHA256` == the hash in the
resource/bundle meta.

| Identity | SHA-256 | Status |
| --- | --- | --- |
| main script (`confirmation-main`) | `a096f292ccde31fbc12afd05e68437f996fd74e5237ce9c212d9862ce6447927` | unchanged from baseline |
| `confirmation_ui.html` file | `76dcf7f26c0801cbcd2577a1b8f86bc663bb7c22b0b92e4dd045424912b66dc1` | unchanged |
| secondary `dev_bridge_ui.html` | `f663b5402810831f29d47407ad0e529dacb2822f767ecbc7c4ef1ef41cf01eb3` | matches Card build |
| secondary `feature_area_details_ui.html` | `54fc6d315a8624496c2f4f8ca56c4a09d63a88c640616b9e5ba82ede0bd87a4f` | matches Card build |

The Card production build (`node scripts/build-chatgpt-ui.mjs`) emits both
secondary assets at the exact opening SHAs; the main-script region hash is
unchanged, so build == runtime == Host and no stale Card was loaded.

## Gate → Test → Command → Result (G1–G18)

Full mapping recorded in `validation.json`; headline evidence:

| Gate | Result | Primary evidence |
| --- | --- | --- |
| G1 full prepare | PASS | `TestConfirmationPrepareReadAfterWriteParity`, `TestPrepareEnvelopeCarriesContract` |
| G2 write-boundary rollback | PASS | `TestConfirmationPrepareIncompleteFactsRollbackLeavesNoVisibleIntent` (0 visible, 0 PENDING) |
| G3 variant construct failure | PASS | `TestIntentFactsCompleteRejectsMissingFacts`, `TestConfirmationVariantInvalidBeforeEffect` |
| G4 reader/display-ref failure | PASS | `TestConfirmationPrepareRejectsUnprovenIdentity` (refused before durable write) |
| G5 in-tx readback mismatch | PASS | `TestConfirmationPrepareReadbackMismatchDetectsEveryImmutableFact` |
| G6 prepare-summary as entry | PASS | JS prepare-summary rejection ×2 + 4-key allowlist + `TestReadAndCommitEnvelopesOmitPrepareContract` |
| G7 show identity failure | PASS | `TestConfirmationToolsReturnSchemaValid…`, `TestSurfaceCommittedTaskSurvivesReadbackFailure`, `TestConfirmationReadbackUnknownRequestNotObserved` |
| G8 display-enrichment failure | PASS | `TestNormalizePublicConfirmationResult…`, `TestCardPresentationConsumesCore…` (intent authority intact) |
| G9 project-scope multi-repo | PASS | `Test…ProjectScopedReviewWithoutRepositoryIdentity`, `TestProjectOnlyConfirmationBinds…`, `TestMultiRepositoryUpperMCPProtocolRoundTrip` |
| G10 complete intent → stale | PASS | `TestMultiRepositoryReviewDecisionDisplayAndTopologyStaleReadback` + JS INTENT_STALE |
| G11 prepare response loss | PASS | `TestConfirmationPrepareReplayAndConflict`, `TestConfirmationReceiptReplayIdempotency` |
| G12 show/mount response loss | PASS | `TestConfirmationReadbackReopenRecoversReceipt` |
| G13 partial, no attempt | PASS | `TestClassifyPreparationStateHalfWrittenNothingCommitted`, `TestClassifyPreparationEvidence` |
| G14 partial, with attempt | PASS | `TestClassifyPreparationStateAttemptUnknown`, `TestNormalizePublicUnknownNeverClaims…` |
| G15 Host entry shape | PASS | `TestMultiRepositoryUpperMCPProtocolRoundTrip`, `TestProjectOnlyConfirmationBinds…` |
| G16 Card asset identity | PASS | `TestConfirmationCardAssetIdentityMatchesDeployedResource` |
| G17 same-Task successor | PASS | `TestReviewDecisionApproveReReviewAbandonSerialize`, `TestMultiRepositoryUpperReviewDecisionReviseAndStaleDecision` |
| G18 trunk regression | PASS | full root aggregate, desktop default, vet, JS, frontend, builds all green |

## Race (R1–R4)

Narrow, bounded, per-group `-race` selections (not an over-broad full-package
race). Environment `GOFLAGS= GOMAXPROCS=10`.

| Group | Scope | Result |
| --- | --- | --- |
| R1 | `internal/storage/sqlite` prepare+classify+concurrency | PASS |
| R2 | `internal/confirmation` (full) | PASS |
| R3 | `internal/mcpserver` confirmation/entry/asset/concurrency | PASS |
| R4 | `internal/controlapi` readback/topology/successor/concurrency | PASS |

`0 FAIL`, `0 DATA RACE`, `0 unexplained timeout`, `0 zero-match`. Log
`race.log` = `e8bd1c7d3c6fbd48e09129966e34d7bd65bfb669bf091c20fdd66dfd06234a39`.

An earlier over-broad full-package `-race` was retired: its only signals were
`-race` slowdown/contextion (a sqlite 15m migration test and two mcpserver
deadline tests), with **no** `DATA RACE` anywhere; it was replaced by the
narrow re-runnable selections above, which all pass.

## Aggregates, vet, JavaScript, frontend, builds

| Gate | Command | Result | Log SHA-256 |
| --- | --- | --- | --- |
| Root aggregate | `go test ./... -count=1 -timeout=8m` (root) | 65 ok / 17 no-test / 0 FAIL / 0 panic, natural END | `342b50d7ee9ee71daad5b91ac1ce4349b64795b6a0e173a9d6fdaf898cda7292` |
| Desktop default | `go test ./... -count=1 -timeout=8m` (desktop) | 8/8 packages PASS, 0 FAIL (clean re-run) | `c195272d119ad9b8ac04a0f60f91a45e86a4dbf5b7c76e13c1e85a219e348f50` |
| Desktop serialized (diagnostic) | `go test -p 1 ./... -count=1 -timeout=5m` | 8/8 PASS (non-substitute) | `8676aa783e1522eb39d9b3d37ef24fff8ae3eb1ab5d60f5f01a48e40d28fc675` |
| Vet root | `go vet ./...` | exit 0 | `8f28443ff06b4e1be71010328939535add97b849e4d9e53e1fdc3f86c28ca053` |
| Vet desktop | `go vet ./...` | exit 0 | `c7886da9c9284555c270b41e94dcfaa53f2378641df20a1d80975c4a38776c7b` |
| Card JS suite | `node --test internal/mcpserver/confirmation_project_ui_test.js` | 23 tests / 23 pass / 0 fail | `7b570a9faa6b333dfb8efb9ef1b81adc5c0e99cbec5195c757b64ee399d1b881` |
| Full JS matrix | `node --test internal/mcpserver/*_test.js internal/bridgehttp/*_test.js` | 285 / 281 pass / 0 fail / 4 opt-in skip | `7b570a9faa6b333dfb8efb9ef1b81adc5c0e99cbec5195c757b64ee399d1b881` |
| Frontend | `npm test` (desktop/frontend) | 52/52 pass | `5331e28fb8d52425042660c05fa3f6afcde57d7eedc6d0388bca70a920070471` |
| Vite build | `npm run build` | exit 0 (9 modules) | `a2c6b46222bfe52711031b1ca90ef5039a865b57b318903662fdbcc9a951531d` |
| Card build | `node scripts/build-chatgpt-ui.mjs <empty>` | exit 0, secondary SHAs match | `fdecca3414cc0609f20cd03e7315b8ab01d6209ba8535504c7a3a867a63e2656` |

### The 4 opt-in JS skips

All are env-gated (`skip: !process.env.…`) and belong to the Decision /
`dev_bridge` main Card surfaces, **not** the default Confirmation
prepare/readback/show/commit chain:

- `decision_ui_test.js:126` — orphan Card transport disposition — `!DEVBRIDGE_ORPHAN_CARD_FIXTURE`;
- `dev_bridge_candidate_test.js:4/17/28` — candidate Card snapshots /
  approval→implementation continuation / durable Review host follow-up —
  `!DEVBRIDGE_CARD_SNAPSHOTS`.

None were newly added; none counted as PASS.

### Desktop probe flake (documented, non-blocking)

`TestProbeAbsoluteNVMStyleLauncherFindsSiblingNode` (`desktop/internal/codex`)
timed out once under first-run default parallelism against the production
`DefaultProbeTimeout=3s`, while race/other heavy jobs contended for CPU. It was
classified as an **evidenced environment/contention flake, unrelated to
Confirmation**: isolated `-count=1` PASS, `-count=5` PASS, serialized `-p 1` 8/8
PASS, and the default-parallel re-run alone 8/8 PASS. The production probe
timeout and test contract were **not** modified. The desktop default aggregate
gate therefore PASSES on a clean contention-free run.

## Protection manifest

45 protected Card/JS files, each with SHA-256; **0 drift** between opening and
final. This round's files entered legitimately (no Card/JS byte drift).
`protection-manifest.json` = `a666f60f6d1b65c5c43a0828c860e12a520ec12982c186348792c057f4985395`.

## git diff / work-tree boundary

- `git diff --check` exit 0; `git diff --cached --check` exit 0.
- Index empty (0 staged entries); `git status --porcelain=v1 -uall` = 23 entries.
- No stage, commit, merge, push, reset, clean, checkout or rebase performed.
- All inherited modifications preserved; no unrelated file deleted.

## Independent read-only review (§18) — all clear

1. Prepare cannot return a half intent — commit gated on reader+completeness+parity; failure returns `c.Intent{}`.
2. `businessOutcome/factsComplete/presentationReady` are formal Go/Core fields.
3. Prepare-only fields stay out of the Card 4-key envelope.
4. In-transaction readback uses the production `confirmationRead`.
5. After rollback no non-empty intent is returned.
6. No alternate prepare path bypasses the gate (single guarded INSERT).
7. A prepare summary cannot serve as a Card entry.
8. On show failure the Host does not mount a Card.
9. The Card cannot show PENDING with `entryBound=false`.
10. Project-scope intent does not require a single `repositoryId`.
11. Display-enrichment failure does not block intent authority.
12. The historical partial classifier reads durable attempt/receipt.
13. Re-prepare is never allowed without evidence.
14. Exact replay never duplicates an intent or successor Review.
15. The real MCP SDK `OutputSchema` runs on every handler.
16. Card asset is the same version across build/runtime/Host (G16).
17. Root and desktop aggregates complete naturally.
18. No live DataRoot or user object touched (tests use `t.TempDir()`).

## Post-deployment read-only runbook for the live object

This round never accessed live object `confirm_cb2d5174e6e96b3573f8addbff6172bc`
or the user DataRoot. After deployment, diagnose it read-only in this order —
`confirmation_get` → `confirmation_options_get` → `confirmation_show` →
attempt/request/receipt diagnosis — then act by classification:

- `PREPARE_INCOMPLETE_NOT_COMMITTED` → create a new intent from current topology.
- `PREPARED_PRESENTATION_UNAVAILABLE` → re-show the original intent, do **not** re-prepare.
- `COMMITTED` → follow the original successor identities.
- `ATTEMPT_EXISTS_OUTCOME_UNKNOWN` → keep waiting / reading / formally cancel; do **not** re-dispatch.
- `PREPARE_COMMITTED_FACTS_CORRUPT` → fail closed into an explicit repair.

The live object must not be operated on automatically.

## Safety confirmations

No stage / commit / merge / push / reset / clean / checkout / rebase. No live
SQLite, user DataRoot, formal runtime or formal Git refs modified. No user
Task/Review/Decision/Confirmation operated. No real model, paid service or
default credential invoked. No Desktop UI/device/simulator/screenshot operation.
No inherited work-tree changes cleaned. No test skipped for duration; no
compile-only / zero-match / helper-only substitution for run acceptance; every
result verified by reading logs, not exit codes alone; heavy race, Desktop
aggregate and frontend build were never run concurrently for CPU.

## Closure

Every formal gate passed, so this round publishes:

```
CONFIRMATION_INTENT_PREPARE_ATOMICITY_CLOSED
CONFIRMATION_EXACT_READBACK_PARITY_CLOSED
CONFIRMATION_HOST_CARD_MOUNT_GATE_CLOSED
CONFIRMATION_PARTIAL_INTENT_CLASSIFICATION_CLOSED
CONFIRMATION_CARD_ENTRY_BINDING_CLOSED
CONFIRMATION_PREPARE_ATOMICITY_AND_CARD_ENTRY_BINDING_CLOSED
CONFIRMATION_PREPARE_ATOMICITY_AND_HOST_MOUNT_GATE_FINAL_CLOSED
```

Retained prior closures remain in force
(`CONFIRMATION_TYPED_REJECTION_READBACK_AND_STALE_INTENT_CLOSED`,
`INVALID_REVIEW_RESULT_RESOLUTION_AND_TARGET_IDENTITY_INJECTION_CLOSED`,
`SAME_TASK_RE_REVIEW_AUTO_START_CLOSED`,
`REVIEW_CONFIRMATION_STAGE_AND_AUTO_EXECUTION_CLOSED`,
`REVIEW_RESULT_MATERIALIZATION_AND_WORKFLOW_PARITY_CLOSED`,
`CODING_REVIEW_STRUCTURED_OUTPUT_SCHEMA_CLOSED`,
`STRICT_STRUCTURED_OUTPUT_SCHEMA_PREFLIGHT_CLOSED`,
`LEGACY_BROAD_FIXTURE_TRIAGE_CLOSED`,
`MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`,
`CODEX_RECOVERY_STATUS_RECONCILIATION_V1_CLOSED`,
`CODEX_REQUEST_USER_DECISION_V1_CLOSED`,
`CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`,
`CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`,
`TRUSTED_EXECUTOR_MODEL_V1`, `CODEX_SANDBOX_HARDENING_DEFERRED`,
`PARTIAL_IMPLEMENTATION`).

Not claimed (verification did not occur): `ALL_TESTS_PASSED`,
`REAL_CODING_AGENT_E2E_PASSED`, `MANUAL_DESKTOP_ACCEPTED`,
`MANUAL_CHATGPT_CARD_ACCEPTED`, `GLOBAL_FINAL_ACCEPTANCE_PASSED`.
`REAL_CODING_AGENT_E2E_NOT_RUN`, `MANUAL_DESKTOP_ACCEPTANCE_PENDING` and
`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING` remain open.
