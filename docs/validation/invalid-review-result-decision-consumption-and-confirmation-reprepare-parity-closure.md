# INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_AND_CONFIRMATION_REPREPARE_PARITY_CLOSURE

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-f760cd2772f62878)。

Task: `INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_AND_CONFIRMATION_REPREPARE_PARITY_CLOSURE`
Spec: `INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_AND_CONFIRMATION_REPREPARE_PARITY` (§1–§20, reasoning effort xhigh)
Date: 2026-09-21

## 1. Snapshot

| Field | Starting | Final |
| --- | --- | --- |
| Branch | `dev_20260908_1913` | `dev_20260908_1913` |
| HEAD | `3f3309d feat: close review confirmation and invalid result resolution` | `82ad760 add skeleton` — see §10: an **external** commit landed mid-verification and swept this phase's uncommitted work in; this session ran no `add`/`commit`/`merge`/`push` |
| SQLite schema head | v87 (`internal/storage/sqlite/migrate.go:18`, last entry `{Version: 87, SQL: invalidReviewResolutionV87SQL}`) | v87 — this phase adds **no** migration; all 72 `*migration*.go` files byte-identical (`protection-manifest.json`) |
| Working set at opening | 62 porcelain entries | 75 before the external commit, 0 after it; byte increment = **23 changed + 7 added, 0 removed** (`increment.json`) |
| Worktree byte manifest | `opening-manifest.sha256` (1,993 paths) | `closing-manifest.sha256` (2,038 paths; +38 = the unrelated `skeleton/` module) |
| Live DataRoot | `~/Library/Application Support/DevBridge/data` | fingerprinted read-only, never opened by any gate (§9) |

This phase changed **no** live Task, Review, Decision, Confirmation, intent, receipt or Git ref, and
called **no** real model or billable service. Every gate used test-owned `t.TempDir()` DataRoots,
test SQLite databases and fake Hosts.

## 2. Root cause of the Decision contradiction

Live evidence (§1, read-only diagnostics only) reproduced deterministically
(`logs/g1-repro-read-vs-write-authority.log`):

```text
read projection : state=REVIEWING action=REVIEW_RESULT_RESOLUTION_REQUIRED approvalStatus=PENDING
                  availableActions=[REQUEST_RE_REVIEW OPEN_PROJECT_REPOSITORIES ABANDON_TASK]
durable         : decisionRows=0 receiptRows=0
commit writer   : code=DECISION_VERSION_CHANGED source=DOMAIN stage=UNIQUE_CONSUMPTION
                  nextAction=PREPARE_NEW_VERSION message="The frozen Review Decision is no longer pending."
```

The raw Review result **is** retained as an `INVALID` materialization, but no
`review_approval_decisions` row was ever created for its resolution Decision: the Review-completion
candidate query excludes Reviews that already carry a materialization, so the pass that would mint
the Decision skipped this Review (fixture shape documented at
`internal/storage/sqlite/invalid_review_result_decision_authority_test.go:22-27`). The two surfaces
then derived "the current Decision" independently:

* the read side synthesized `PENDING` from Task phase + materialization and offered
  `REQUEST_RE_REVIEW`;
* the write fence required a durable Decision and compared the intent's
  `preparedDecisionVersion` against a re-derived token, so it refused;
* that token had been the **Decision ID**, so prepared and current could never disagree meaningfully
  and could never agree either.

§4 classification: **E — different stores each maintained their own "current Decision" fact**
(primary), with **C — prepared/current version token defined inconsistently** as the enabling
defect. **A** is excluded (the Decision was never durably `PENDING`), **B** is excluded (it was not
terminal-but-stale; it had no durable row at all), **D** is excluded (prepare advanced no version —
`decisionRows=0` before any prepare). The fix was made at the authority layer, never at the UI: no
error message was patched.

## 3. The single Decision reducer (§5)

`internal/storage/sqlite/review_decision_snapshot.go`

* `reviewDecisionSnapshotTx(ctx, q, decisionID)` (:114) — one function reads Review→Task ownership,
  `Task.currentReviewId`, phase/status, materialization, raw/provenance/result digests, the durable
  Decision row, resolution receipt, consumption and successor identity, and derives
  Status / ReasonCode / ReasonMessage / RequiredAction / NextAction in one place (`finish()`, :200-250).
* `reviewDecisionVersionToken` (:106) — `confirmation.ValueHash` over those durable facts. It is
  never the Decision ID; it is stable for identical state and changes when status or ownership
  changes (`TestReviewDecisionVersionTokenIsStableAndDistinct`).
* `Version` is stamped on every exit path (:120, :229), so no surface can see a snapshot without a token.

Consumers — every read surface **and** the write transaction, all through the same call:

| Surface | Call site |
| --- | --- |
| commit / consumption fence | `internal/storage/sqlite/confirmation_store.go:78` (inside the write tx; escalates to `DECISION_SUPERSEDED`/`DECISION_OWNER_CHANGED`) |
| invalid-result workflow guidance (`workspace_get`, `TaskView`, `FeatureAreaProductProjection`) | `internal/storage/sqlite/review_approval_store.go:473` |
| review-approval guidance | `internal/storage/sqlite/review_approval_store.go:536` |
| `confirmation_options_get` Decision gate | `internal/controlapi/confirmation.go:314-332` |
| prepare preflight | `internal/controlapi/confirmation_validation.go:216-231` |

A read can no longer report `PENDING` while the writer reports otherwise without an actual
concurrent version change: `TestInvalidReviewResultOrphanDecisionReadAndCommitAgreeAndResolveOnce`
drives exactly the live shape and requires both to agree, and
`TestConfirmationReprepareRacesTheDecisionResolutionAndLeavesOneAuthority` proves it under `-race`.

## 4. Precise codes and the version token (§6)

`decisionId != decisionVersion`. `wp.Approval` now carries a real
`DecisionVersion` plus the reducer's `ReasonCode`
(`internal/workflowprogression/workflow.go:62-81`), and the codes are split exactly as specified:

| Code | Condition |
| --- | --- |
| `DECISION_NOT_PENDING` | no durable Decision backs the frozen identity, or it is resolved/abandoned without a successor |
| `DECISION_VERSION_CHANGED` | the Decision exists but its durable facts moved after prepare |
| `DECISION_SUPERSEDED` | a successor Review already resolved it |
| `DECISION_OWNER_CHANGED` | the owning Task/Review identity moved away from the frozen target |

Pinned by `TestConfirmationDecisionGateCodesTerminalAuthorityPrecisely`,
`TestInvalidReviewResultAbandonedDecisionCodesNotPending`,
`TestInvalidReviewResultWithoutAuthorityIsNotPending`,
`TestInvalidReviewResultTerminalDecisionOffersNothingAndCommitsNothing`.

## 5. Write transaction and options parity (§7, §8)

Commit order in one transaction (`confirmation_store.go`): exact intent → identity/version/contentHash
→ Decision snapshot → `preparedDecisionVersion` → `currentReviewId`/phase → materialization identity
→ topology/development context → no-successor proof → request/payload identity → consume Decision →
create successor Review/Run/start authority → receipt → commit. A legitimately `PENDING` Decision
still commits `REVISE` successfully (G3), and version drift is refused with
`REJECTED_NOT_CONSUMED` and zero business side effects (G5).

`confirmation_options_get` now reads the intent's latest durable submission **first**
(`internal/controlapi/confirmation.go:302-400`, variant routing at :384 and :634-641):

| After a recorded rejection | Value |
| --- | --- |
| `available` | `false` |
| `attemptState` | `TERMINAL_REJECTED` |
| `receiptState` | `REJECTED_NOT_CONSUMED` |
| `retryDisposition` | `REPREPARE_REQUIRED` |
| `reprepareAllowed` | `true` |
| `nextAction` | `PREPARE_NEW_VERSION` |
| `REVISE` variant | `available=false`, `reasonCode=INTENT_REPREPARE_REQUIRED` |

`NOT_ATTEMPTED`/`NOT_OBSERVED` remain reachable only with no submission at all
(`TestConfirmationOptionsTrackTheDurableRejectionReceiptAcrossSurfaces`).

## 6. Replacement prepare, lineage and version (§10, §11)

`Facade.ReprepareConfirmation` (`internal/controlapi/confirmation.go:193-267`) is the formal
`PREPARE_NEW_VERSION` action and the only replacement-entry intent constructor: it reuses the
existing prepare ledger through `ReplayPrepare`, proves the recorded no-effect rejection, refuses a
source that is not `PENDING`/rejected/superseded with typed codes, reads the canonical Decision
snapshot, clears the stale frozen facts
(`PreparedDecisionVersion`/`PreparedTopologyRevision`/`PreparedDevelopmentContextHash`) and delegates
to `PrepareConfirmation`, which re-freezes them atomically. Client input is limited to
`aiSessionId, sourceIntentId, sourceVersion, sourceContentHash, requestId` + optional feedback.

* New intent, new contentHash, **`version=1`**, `sourceIntentId` as the only lineage field;
  the rejected source becomes `SUPERSEDED` and keeps its rejection history readable.
* `PREPARE_NEW_VERSION` is a **product action name**, not an in-place upgrade of the old intent's
  version to 2 — documented in `docs/architecture/purpose-scoped-confirmation.md:909-921` and pinned
  by `TestConfirmationReplacementLineageNeverIncrementsVersion` and
  `TestConfirmationSourceIntentIdIsLineageOnly`.
* Exact replay returns the same replacement; same requestId + changed payload returns
  `CONFIRMATION_REQUEST_CONFLICT`; concurrent duplicate prepares create exactly one replacement; a
  Decision that turned terminal yields a typed rejection with **zero** new intents
  (`TestConfirmationReprepareReplaysExactRequestAndRefusesChangedPayload`,
  `TestConfirmationReprepareConcurrentRequestsCreateExactlyOneReplacement`,
  `TestConfirmationReprepareRefusesWhenTheDecisionIsNoLongerActionable`,
  `TestConfirmationReprepareRefusesForeignOrUnrejectedSources`).
* No successor is created by prepare:
  `TestConfirmationReprepareReplacesRejectedIntentAndRemainsTheOnlySuccessorAuthor`,
  `TestConfirmationReplacementSuccessorReviewAppearsOnlyAfterCommit`.

## 7. Public `confirmation_commit` and text Continue (§12, §13)

`confirmation_commit` is registered as a real MCP tool
(`internal/mcpserver/confirmation_tools.go:470`) with a closed input schema
(`confirmationCommitSchema`) and the existing Confirmation output schema, listed in the business
catalog (`internal/mcpserver/confirmation_catalog.go:27,34,85`), and served through the same
`confirmationService` handler as the Card, so both surfaces reach the identical Core method.

* G14 (`TestPublicConfirmationCommitIsTheOnlyModelDiscoverableSubmissionEntry`) asserts over a **live
  SDK `tools/list`**: exactly one `confirmation_*` tool requires `intentId`+`contentHash`+`requestId`+
  `variantId`, the discovery set is exactly `[commit, get, options_get, reprepare, show]`, every
  confirmation tool shares one output envelope (`reflect.DeepEqual`), and `dev_bridge_card_get` stays
  app-only.
* G15 (`TestCardShapedAndTextShapedConfirmationCommitRunOnOneHandler`) proves one handler: a
  Card-shaped commit produces COMPLETED + exactly one successor Review + one accepted
  `feature_area_review_start_requests` row, and the read-back by `requestId` matches that envelope;
  then nine injected derived-authority fields (`outcome`, `consumed`, `retryDisposition`,
  `reprepareAllowed`, `decisionVersion`, `successorReviewId`, `force`, `assumeNotConsumed`,
  `retryAnyway`) are each refused with no change to the successor count.
* G16 (`TestTypedRejectionReadbackNeverDegradesToUnknown`): a typed rejection survives read-back as
  `REJECTED_NOT_CONSUMED` with the JSON containing neither `UNKNOWN` nor `READ_ORIGINAL_RECEIPT`; an
  unobserved requestId either refuses with `CONFIRMATION_REQUEST_NOT_OBSERVED` or states the explicit
  unrecorded shape — never a consumption claim, never `EDIT_SELECTION`.
* G17 (`TestTextContinueOverAReprepareRequiredRejectionUsesThePublicReplacementEntry`): the text
  Continue path reads the exact five-field identity first, returns the typed rejection with
  `PREPARE_NEW_VERSION`/`REPREPARE_REQUIRED`, never commits a terminal intent, and reaches the
  replacement through `confirmation_reprepare` on the real wire.
* A surface with no commit handler reports its own condition
  (`TestAbsentCommitServiceReportsItsOwnConditionNotAnUnknownOutcome`,
  `CONFIRMATION_COMMIT_TOOL_UNAVAILABLE` → `READ_CURRENT_TARGET`) and states no outcome — no
  fabricated `UNKNOWN`.
* Routing copy for the model is pinned verbatim in
  `internal/mcpserver/confirmation_card_copy_guidance_test.go` against
  `mcpserver.ChatGPTConfirmationInstructions` (§13/§14).

## 8. Card and outer copy (§9, §14)

The Card already behaved correctly on the live intent and its behaviour was preserved:
`entryBound=true`, `staleIntent=true`, `confirmedOutcome=REJECTED_NOT_CONSUMED`,
`editableRetry=false`, `pendingRequestId` retained. Options, Card and workspace now agree on
`requiredAction`, `allowedActions`, `reasonCode` (G8), and the outer text is fixed:

| Situation | Copy |
| --- | --- |
| recorded rejection | 本次重新审查确认未被消费。原因：冻结的审查处置状态已发生变化。需要重新准备新的确认。 |
| after replacement prepare | 新的重新审查确认已准备 |
| after replacement commit | successor Review 已创建并开始准备 — only then |
| forbidden | 当前结果未知 / 无法确认是否已提交 |

`docs/architecture/purpose-scoped-confirmation.md` carries the same contract (rejection copy at
:899-921, replacement prepare at :909, text Continue at :938).

## 9. Gates G1–G20 (§16)

All gates run on test-owned DataRoots; every named gate below actually executed (no zero-match, no
compile-only, no SKIP counted).

| Gate | Evidence | Result |
| --- | --- | --- |
| G1 exact repro (read `PENDING` vs writer `DECISION_VERSION_CHANGED`) | `TestInvalidReviewResultDecisionReadProjectionWithoutDurableAuthority`, `logs/g1-repro-read-vs-write-authority.log` | PASS |
| G2 Decision fact diagnosis A–E | §2 (durable-row evidence, not an error message) | PASS |
| G3 truly `PENDING` → prepare→commit `REVISE` succeeds, one successor Review/Run/start authority, same Task | `TestCardShapedAndTextShapedConfirmationCommitRunOnOneHandler`, `TestConfirmationReprepareReplacesRejectedIntentAndRemainsTheOnlySuccessorAuthor` | PASS |
| G4 truly terminal → no prepare/commit/reprepare anywhere | `TestInvalidReviewResultTerminalDecisionOffersNothingAndCommitsNothing`, `TestConfirmationReprepareRefusesWhenTheDecisionIsNoLongerActionable` | PASS |
| G5 prepare/commit version drift → `REJECTED_NOT_CONSUMED`, 0 side effects | `TestInvalidReviewResultDecisionCommitValidatesTheFrozenVersionToken` | PASS |
| G6 options before rejection | `TestConfirmationOptionsTrackTheDurableRejectionReceiptAcrossSurfaces` | PASS |
| G7 options after rejection | same gate (§5 table) | PASS |
| G8 Card/workspace/options action-set parity | `TestConfirmationReplacementIntentMountsItsOwnCardInstance`, `TestConfirmationOptionsTrackTheDurableRejectionReceiptAcrossSurfaces` | PASS |
| G9 replacement prepare (new id, `version=1`, `sourceIntentId`, new hash, 0 successor) | `TestConfirmationReplacementLineageNeverIncrementsVersion`, `TestConfirmationReplacementSuccessorReviewAppearsOnlyAfterCommit` | PASS |
| G10 replacement exact replay | `TestConfirmationReprepareReplaysExactRequestAndRefusesChangedPayload` | PASS |
| G11 changed replacement payload → conflict | same gate | PASS |
| G12 Decision terminal before reprepare → 0 new intent + typed rejection | `TestConfirmationReprepareRefusesWhenTheDecisionIsNoLongerActionable` | PASS |
| G13 old intent history, never re-committable | `TestConfirmationRejectedNotConsumedLeavesTheSameIntentEditable` (edit-selection branch), G17's refused fresh-requestId commit, `TestConfirmationSourceIntentIdIsLineageOnly` | PASS |
| G14 public commit discovery on a real SDK session | `TestPublicConfirmationCommitIsTheOnlyModelDiscoverableSubmissionEntry` | PASS |
| G15 Card/text commit parity on one handler | `TestCardShapedAndTextShapedConfirmationCommitRunOnOneHandler` | PASS |
| G16 transport error + typed read-back never degrades | `TestTypedRejectionReadbackNeverDegradesToUnknown` | PASS |
| G17 text Continue over a `REPREPARE_REQUIRED` rejection | `TestTextContinueOverAReprepareRequiredRejectionUsesThePublicReplacementEntry` | PASS |
| G18 concurrency: commit vs reprepare, reprepare vs resolution, two reprepares | `TestConfirmationReprepareRacesACompetingCommitOnTheRejectedIntent`, `TestConfirmationReprepareRacesTheDecisionResolutionAndLeavesOneAuthority`, `TestConfirmationReprepareConcurrentRequestsCreateExactlyOneReplacement` | PASS |
| G19 response loss / Core reopen: no duplicate replacement, no duplicate successor Review | replay + `TestConfirmationConcurrentCardInstancesStayIsolated`, successor counts asserted in G9/G15 | PASS |
| G20 trunk regression (prepare atomicity, exact read identity, Card instance identity, full-validator parity, safe fallback, typed rejection, UNKNOWN no-redispatch, target injection, same-Task successor, Review confirmation, multi-repo Coding) | full `internal/controlapi`, `internal/mcpserver`, `internal/storage/sqlite`, `internal/confirmation`, `internal/localmcp` suites | PASS |

## 10. Verification matrix (§17)

| Gate | Result |
| --- | --- |
| `go test` `internal/confirmation` + `internal/storage/sqlite` | `ok 0.587s` / `ok 95.833s` (`logs/57-focused-unit-1.log`) |
| `go test` controlapi / mcpserver / localmcp / workflowprogression / scopereview / bridgehttp / consoleapi | `ok 458.838s` / `92.902s` / `220.212s` / `1.778s` / `3.118s` / `116.208s` / `6.456s` (`logs/57-focused-suites-2.log`) |
| Targeted race — Decision read/write | `ok 35.962s` (`logs/57-race-decision-readwrite.log`) |
| Targeted race — commit/reprepare | `ok 512.242s` (`logs/57-race-commit-reprepare.log`) |
| Targeted race — options/receipt read + successor creation | `ok 78.546s` (`logs/57-race-options-receipt-successor.log`) |
| Targeted race — replacement exact replay + commit wire | `ok 175.059s` (`logs/57-race-replacement-replay-commit-wire.log`) |
| Package race — confirmation / storage-sqlite / mcpserver / workflowprogression / scopereview | `ok 2.065s` / `ok 1611.863s` (120m budget) / in-scope subset `ok 78.084s` / `ok 1.775s` / `ok 1.488s` |
| Package race — controlapi (full, 120m) | all tests executed in 6352.516s; the single `--- FAIL` re-ran `-race -count=3` → `ok 106.386s` |
| Package race — bridgehttp | full package 1591.647s, one flake; `-run TestConsole` 13 tests → 12 pass, 1 flake; non-race full package `ok 116.208s` |
| `DATA RACE` occurrences across every race log | **0** |
| Root aggregate `go test ./... -count=1 -timeout=30m` | **65 packages ok**, exit 0 (`logs/57-aggregate-root.log`) |
| Desktop aggregate `go test ./... -count=1` | first pass 1 flake (`managed Core process was not created`), re-run **8/8 ok**, exit 0 (`logs/57-aggregate-desktop-retry.log`) |
| `go vet ./...` root and desktop | exit 0, **empty output** both |
| MCP/Card JavaScript `node --test internal/mcpserver/*_test.js` | 195 tests / 191 pass / 0 fail / 4 skipped |
| BridgeHTTP JavaScript `node --test internal/bridgehttp/*_test.js` | 116 / 116 pass / 0 fail |
| Desktop frontend `npm test` | 52 pass / 0 fail |
| Vite production build `npm run build` | exit 0, `dist/` emitted |
| Card production build `node scripts/build-chatgpt-ui.mjs` | exit 0, 2 self-contained ChatGPT UI assets (+`manifest.json`), neither is the Confirmation Card |
| Card asset identity | `TestConfirmationCardAssetIdentityMatchesDeployedResource` PASS 0.16s; `_meta.mainScriptSHA256` = recomputed `<script id="confirmation-main">` digest = `79b4b9f5595e7510f2a1d9478519bfb8b3c7698e6b0df50d7260cdda93adaebc` (source = served = advertised tool meta = resource meta = fixture) |
| Frozen-asset protection manifest | 132 pinned paths (60 carried from the previous round + 72 SQLite migration files), 0 missing, 10 legitimately-in-scope changes, **0 migration changes** |
| `gofmt -l` over every changed Go file | empty (7 pre-existing drifted files elsewhere, untouched) |
| `git diff --check`, `git diff --cached --check` | exit 0 while the work was uncommitted; after the external commit the only `git diff --check HEAD^ HEAD` findings are inside `skeleton/*.md`, **0 in any Phase-5 path** |

Classified anomalies, each re-run in isolation to prove it
(`scratch/race-anomaly-classification.md`):

1. `internal/storage/sqlite` and `internal/controlapi` full-package `-race` aborted at the 35m
   wall clock → both completed at a 120m budget (27m / 106m).
2. `TestConfirmationReprepareReplaces…` failed once with
   `git resolve commit failed: signal: segmentation fault` — the external `git` binary crashed under
   load; `-race -count=3` re-run 3/3 green.
3. `TestExternalWorkflowDiscoveryContract` +
   `TestReviewReasoningEffortFreshSchemaMatchesRuntimeAdmission` fail under `-race` with
   Streamable-HTTP deadline / `client is closing` errors. Pre-existing: identical failures are logged
   in the previous phase's baseline (`race-f-baseline.log`, `race-a.log`,
   `race-classify-summary.log:race_f_exit=1`) while the in-scope Card/confirmation subset passes
   (`ok 46.547s` then, `ok 78.084s` now).
4. `TestConsoleAggregatesExecutionPhasesAndKeepsFrozenDefinitionHistory` (`internal/bridgehttp`) fails
   only under `-race` (4/4 isolated and subset runs, ~35s each vs 0.49–0.77s historically,
   `phase/ref mismatch` on a reserved-but-unstarted verification Run); the package passes in full
   without `-race`, the other 12 `TestConsole*` tests pass with it, `internal/bridgehttp` has **zero**
   Phase-5 edits, and every package feeding the asserted facts (`bridgehttp`, `consoleapi`, `run`,
   `executionproposal`, `workflowpresentation`, `execution`) is outside the change set.
5. The desktop aggregate's `managed Core process was not created` flake cleared on a full re-run,
   matching the same classification recorded in the previous round.

## 11. Independent read-only review (§18)

| # | Question | Finding |
| --- | --- | --- |
| 1 | Multiple Decision authorities left? | No. One reducer, three production read sites + one write fence, all via `reviewDecisionSnapshotTx` (§3 table) |
| 2 | Is `decisionVersion` still the decisionId? | No — `reviewDecisionVersionToken` hash; `TestReviewDecisionVersionTokenIsStableAndDistinct` |
| 3 | Do workspace and commit share one snapshot? | Yes, same function inside the write tx and in guidance reads |
| 4 | Does options still ignore a recorded rejection? | No — durable submission is read first (§5); G6/G7 |
| 5 | Can the old intent commit again? | No — refused with the writer's code and `PREPARE_NEW_VERSION`; G13/G17 |
| 6 | Does replacement really mint a new intentId? | Yes; source flips to `SUPERSEDED`; G9 |
| 7 | Is version wrongly incremented? | No — replacement is `version=1`; `TestConfirmationReplacementLineageNeverIncrementsVersion` |
| 8 | Is `sourceIntentId` lineage-only? | Yes — `TestConfirmationSourceIntentIdIsLineageOnly` |
| 9 | Is `confirmation_commit` really in `tools/list`? | Yes, on a live SDK session over Streamable HTTP; G14 |
| 10 | Do Card and text share the Core handler? | Yes — one registered submission entry, one envelope, one read-back match; G15 |
| 11 | Can a typed rejection become UNKNOWN? | No — G16 asserts both JSON and routing fields |
| 12 | Does replacement response loss duplicate an intent? | No — exact replay returns the same replacement; G10/G19 |
| 13 | Does successor response loss duplicate a Review? | No — single successor asserted by count, `Task.current_review_id` and the `review_re_review_requests` chain; G15/G17/G19 |
| 14 | Is UNKNOWN no-redispatch intact? | Yes — G20 trunk regression plus `TestAbsentCommitServiceReportsItsOwnConditionNotAnUnknownOutcome` |
| 15 | Were live objects untouched? | Yes — §12 runbook below is still pending on `confirm_13fc…`; DataRoot fingerprinted only |

## 12. Post-deploy live runbook (§19 — manual, not executed here)

Against the current live intent `confirm_13fc3825f13b1a7cb369ae53477c4874`
(session `ai_session_61cd0dd6d5389edc4b38e3bd9c4b305b`, Task
`task_20402fc91e7271d8724e1bb4f5e5dbbc`, Review `review_bfcf593ae6fb34725276a5793893496b`),
read-only verification must first show `REJECTED_NOT_CONSUMED` / `REPREPARE_REQUIRED` /
`reprepareAllowed=true`. Then, **by explicit user action only**:

1. `confirmation_reprepare` with `aiSessionId`, `sourceIntentId=confirm_13fc…`, `sourceVersion=1`,
   `sourceContentHash=2fd43421c17635ce59264c34ad60a578368bfed25f09c54ad196e6d7bddf246d` and a fresh
   requestId → expect a **new** intentId, `version=1`, `sourceIntentId=confirm_13fc…`, new contentHash;
2. `confirmation_show` on the new intent → the user reviews and confirms;
3. `confirmation_commit` on the new intent → exactly one successor Review on the same Task.

The old intent must never be committed again; nothing in this phase may be automated against the
live DataRoot.

## 13. Deployment note on the external commit

Commit `82ad760 "add skeleton"` (`lazyun <269333685@qq.com>`, 2026-09-21 08:53:34 +0800) landed while
the `-race` batches were running. It contains this phase's work plus an unrelated new module
`skeleton/` (`module devbridge.local/skeleton-contract`, own `go.mod`, no `go.work`, therefore
excluded from root `go test ./...` and `go vet ./...`). File bytes were not altered by that commit, so
every gate above still describes the same content. This session performed no git write operation, and
the §20 instruction "结束时不暂存、不提交、不合并、不 push" was honoured: nothing was staged,
committed, merged or pushed by this session, and no branch was reset, cleaned, checked out or rebased.

## 14. Markers

Published (all §16/§17/§18 gates above pass):

```text
INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_PARITY_CLOSED
CONFIRMATION_REJECTION_OPTIONS_RECEIPT_PARITY_CLOSED
CONFIRMATION_REPREPARE_REPLACEMENT_INTENT_CLOSED
CONFIRMATION_PUBLIC_COMMIT_TOOLING_PARITY_CLOSED
CONFIRMATION_TEXT_CONTINUE_TYPED_READBACK_CLOSED
INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_AND_CONFIRMATION_REPREPARE_PARITY_CLOSED
```

Retained from earlier rounds (re-verified by the G20 trunk regression):

```text
CONFIRMATION_REJECTED_NOT_CONSUMED_RETRY_AND_READ_IDENTITY_CLOSED
CONFIRMATION_NEW_INTENT_CARD_INSTANCE_IDENTITY_CLOSED
CONFIRMATION_SOURCE_INTENT_LINEAGE_ONLY_CLOSED
CONFIRMATION_CROSS_INTENT_CARD_STATE_ISOLATION_CLOSED
CONFIRMATION_HOST_CARD_FULL_VALIDATOR_PARITY_AND_SAFE_FALLBACK_CLOSED
CONFIRMATION_CARD_ENTRY_COLLECTION_CANONICALIZATION_CLOSED
CONFIRMATION_INTENT_PREPARE_ATOMICITY_CLOSED
CONFIRMATION_EXACT_READBACK_PARITY_CLOSED
CONFIRMATION_TYPED_REJECTION_READBACK_AND_STALE_INTENT_CLOSED
INVALID_REVIEW_RESULT_RESOLUTION_AND_TARGET_IDENTITY_INJECTION_CLOSED
SAME_TASK_RE_REVIEW_AUTO_START_CLOSED
CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED
```

Still open (unchanged by this phase):

```text
CONFIRMATION_REPLACEMENT_INTENT_CARD_PRESENTATION_HOST_ACCEPTANCE_PENDING
CONFIRMATION_NEW_INTENT_CARD_INSTANCE_BINDING_HOST_ACCEPTANCE_PENDING
PARTIAL_IMPLEMENTATION
REAL_CODING_AGENT_E2E_NOT_RUN
MANUAL_DESKTOP_ACCEPTANCE_PENDING
MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING
CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED
```

Explicitly **not** claimed: no typed rejection was downgraded to UNKNOWN, the old intent was never
re-committed, the Decision ID is not the version token, the replacement never reuses the old id or
increments its version, commit authority is not Card-only, no duplicate successor Review exists, no
live Confirmation was mutated, no ChatGPT Card acceptance was performed and no global final
acceptance passed — human Host/Desktop acceptance and the §19 live runbook remain the user's.

## 15. Audit artifacts

`/private/tmp/devbridge-invalid-review-result-decision-consumption-reprepare-parity-audit/`:
`baseline.txt`, `opening-manifest.sha256` (1,993), `closing-manifest.sha256` (2,038),
`increment.json`, `protection-manifest.json` (132 paths), `logs/` (focused suites, five targeted
races, seven package races + retries, aggregates, vet, JS, frontend/build, Card build,
`57-card-asset-identity.log`, `57-card-identity-values.txt`, `57-diff-check-gofmt.txt`,
`57-diff-check-final.txt`, `57-gofmt-scope.txt`, `57-live-dataroot-mtime-check.txt`),
`scratch/race-anomaly-classification.md`, `scratch/chatgpt-ui-build-57/`.

This report is the only file changed after `closing-manifest.sha256` was taken
(`docs/validation/invalid-review-result-decision-consumption-and-confirmation-reprepare-parity-closure.md`,
sha256 `4515d0e1be451304cffd0b2a4570929627585ceaeefdd28db8e252053e94eedd`), so the byte manifest
above is the closing observation of the *implementation* tree. A repo-wide search finds **0** Go,
JavaScript, Python or shell readers of `docs/validation`, so no gate depends on this file and no gate
was re-run because of it. `git status --porcelain` shows exactly this one untracked report; the tree
is otherwise clean, and nothing was staged, committed, merged or pushed.
