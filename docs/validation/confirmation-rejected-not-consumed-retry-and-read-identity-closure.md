# Rejected-not-consumed manual retry, exact read identity and replacement version semantics — verification and closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-3b7d5c3d1c82b4d6)。

Task id: `CONFIRMATION_REJECTED_NOT_CONSUMED_RETRY_AND_READ_IDENTITY_CLOSURE`.

This round fixes exactly the three defects the previous independent review confirmed, and
nothing else:

1. A Card that received an explicit `REJECTED_NOT_CONSUMED` with `nextAction = EDIT_SELECTION`
   kept holding the old `pendingCommit`, so the user could not change the selection and submit
   a new command.
2. The public Confirmation read input had no `contentHash`, so `get` / `options_get` / `show`
   and the receipt read-back were not bound to the frozen intent content.
3. Replacement intents are always created at `Version: 1`, but that field's formal semantics
   were unfrozen and could be read as a replacement ordinal.

Everything else in the Confirmation subsystem was frozen from earlier rounds (prepare
atomicity, public collection canonicalization, Go/Card validator parity, safe fallback, Card
instance identity, `sourceIntentId` lineage-only, Host replacement Card instance allocation,
Review materialization, same-Task successor Review, UNKNOWN no-redispatch, Repository
topology, Codex runtime). **Whether the real ChatGPT Host adopts
`devbridge/presentationInstanceId` is manual Host acceptance and is *not* claimed closed by
any automated test in this round.**

## Baseline

| Field | Opening | Final |
| --- | --- | --- |
| branch | `dev_20260908_1913` | unchanged |
| HEAD | `3f3309d15a8b06a973db90ea213a79972330ed3e` | unchanged — nothing staged, committed, merged or pushed |
| schema | v87 | v87 — this round adds **no** migration |
| Card contract | `PURPOSE_CONFIRMATION_V5` / `ui://devbridge/confirmation/v5` | unchanged (V5) |
| index (staged) | 0 entries | 0 entries |
| `git status --porcelain` | 41 entries (14 ` M`, 27 `??`) | 62 entries (25 ` M`, 37 `??`); **0 paths lost**, +10 round-created untracked, +11 tracked files that were clean at opening |
| non-ignored byte manifest | 1,983 files (Phase-3 final) | **1,993** files |

Audit directory: `/private/tmp/devbridge-confirmation-rejected-not-consumed-retry-and-read-identity-audit/`.

### Inherited dirty vs this round's actual increment (§1)

The round's increment is computed **only** against this round's own opening byte manifest
(`opening-manifest.sha256`, 1,983 entries captured before the first Phase-4 edit), never
against the cumulative diff from HEAD. Against that baseline Phase 4 created **10** paths,
modified **34**, deleted **0**, leaving **1,949** files byte-identical:

Created — `internal/confirmation/retry_disposition_test.go`,
`internal/controlapi/confirmation_read_identity_test.go`,
`internal/controlapi/confirmation_rejected_retry_lifecycle_test.go`,
`internal/controlapi/confirmation_replacement_lineage_test.go`,
`internal/mcpserver/confirmation_canonical_docs_test.go`,
`internal/mcpserver/confirmation_closed_input_schema_test.go`,
`internal/mcpserver/confirmation_rejected_retry_fixture_test.go`,
`internal/mcpserver/confirmation_rejected_retry_test.js`,
`internal/mcpserver/testdata/confirmation_rejected_retry_v5.json`, and this document.

Modified (34) — the full per-file old/new digest list is in `increment.json`; the code and
asset subset is: `internal/confirmation/contract.go`, `internal/confirmation/outcome.go`,
`internal/controlapi/confirmation.go`, `internal/controlapi/confirmation_failure.go`,
`internal/mcpserver/confirmation_schema.go`, `internal/mcpserver/confirmation_tools.go`,
`internal/mcpserver/confirmation_projection.go`, `internal/mcpserver/confirmation_instance.go`,
`internal/mcpserver/external_workflow.go`, `internal/mcpserver/confirmation_ui.html`,
`internal/mcpserver/testdata/confirmation_card_contract_v5.json`,
`internal/mcpserver/testdata/confirmation_card_instance_lifecycle_v5.json`,
`docs/interfaces.md` and `docs/architecture/purpose-scoped-confirmation.md`. The remaining 20
are tests that had to follow the new contract shape.

Inherited-dirty and **not** touched by Phase 4 (byte-identical to the opening manifest, so
absent from the increment): `internal/storage/sqlite/confirmation_store.go`,
`internal/controlapi/confirmation_display.go`,
`internal/controlapi/confirmation_validation.go`,
`internal/confirmation/{canonicalize,completeness,preparation_state,display}.go` and the other
paths that were already dirty when this round opened. Nothing inherited was reverted, cleaned
or committed.

### Frozen-asset protection manifest (§2 禁止面)

`protection-manifest.json` pins **60** paths carried from the previous round's final byte
manifest — the BridgeHTTP product UI and its 12 JS suites plus DOM harness, the other
non-Confirmation Cards (`dev_bridge_ui.html`, `decision_ui.html` and their tests),
`internal/confirmation/{canonicalize,completeness}.go`,
`internal/controlapi/confirmation_validation.go`, the shared contract-vector harness
`confirmation_contract_vectors_test.go`, and `scripts/build-chatgpt-ui.mjs`:

- **0 missing** (every pinned path still exists), and for all 60 the previous-round digest
  equals this round's opening digest, so nothing drifted *between* rounds either.
- **47 unchanged** during this round — byte-identical across previous-round final, opening and
  closing. That is the §2 freeze holding on the surfaces this round had no licence to touch.
- **13 changed**, and every one of them is inside this round's own 34-file modified set
  (`protectedChanged ∖ modifiedByRound = ∅`):

| Protected path | previous → closing | Why this round had to change it |
| --- | --- | --- |
| `docs/interfaces.md` | `ed2f5fe4f5…` → `381d0da3d7…` | §7 read identity, §3/§4 retry vocabulary and §10 version semantics are published contracts, and the spec requires them written into the canonical docs as well as tests. |
| `docs/architecture/purpose-scoped-confirmation.md` | `99c966b473…` → `7140f9cf6c…` | same |
| `internal/confirmation/contract.go` | `edc5e1c990…` → `6171dd94e1…` | defect 2: `ReadInput` gains the required `ContentHash`. |
| `internal/confirmation/outcome.go` | `a58986ef9f…` → `4256021619…` | defect 1: the single `deriveRetryDisposition` reducer. |
| `internal/mcpserver/confirmation_projection.go` | `c53f48fe65…` → `81895597fe…` | defect 1: public projection of the derived retry facts. |
| `internal/mcpserver/confirmation_schema.go` | `c370b5a965…` → `54f850de4f…` | defect 2 + §12: read schema requires `contentHash`, closed-DTO refusals. |
| `internal/mcpserver/confirmation_tools.go` | `04e2c045d9…` → `89c3619cec…` | defect 2: `confirmation_show` goes through the one gated read path. |
| `internal/mcpserver/confirmation_ui.html` | `39250093c8…` → `47631673a2…` | defects 1 and 2 on the Card: the unlock predicate and the five-field `readInput()` / `receiptInput()`. This is the §16 re-record. |
| `internal/mcpserver/testdata/confirmation_card_contract_v5.json` | `e0136c2723…` → `90af3d1aee…` | hand-authored shared vectors: retry-disposition vectors and the two `_meta` strings (finding #2). |
| `internal/mcpserver/testdata/confirmation_card_instance_lifecycle_v5.json` | `ee3def198c…` → `5be367c75f…` | a **captured** Phase-3 fixture that had to be re-captured because the embedded Card script digest moved; the provenance guard forced this rather than letting a stale Card replay (§16). Never hand-edited. |
| `internal/mcpserver/confirmation_card_instance_mount_test.js` | `b666cf51b3…` → `90a338128b…` | replays that re-captured lifecycle fixture. |
| `internal/mcpserver/confirmation_project_ui_test.js` | `62fe1aa83c…` → `51b204666f…` | defect 2: project-scoped Card reads now supply the frozen digest. |
| `internal/mcpserver/confirmation_public_output_test.go` | `5d71cfdc0b…` → `f36fe3ef0b…` | defect 2: public-output expectations carry the fifth identity field. |

Notably **absent from the changed set**: every `internal/bridgehttp/` product-UI file and test
(25 paths), `dev_bridge_ui.html` / `decision_ui.html` and their tests,
`confirmation_contract_vectors_test.go` (the shared Go/Card vector harness),
`scripts/build-chatgpt-ui.mjs`, `internal/confirmation/{canonicalize,completeness}.go` and
`internal/controlapi/confirmation_validation.go` — which is also the file review finding #8
points at, and it stayed byte-identical through this round.

## Defect 1 — an explicit non-consumed rejection must return the Card to editing

### Root cause

`applyIntent` projected every recorded `REJECTED_NOT_CONSUMED` into
`pendingCommit = originalSubmission(submission.request)` and froze it there. Holding the
original request is correct for `STALE` and for a `PREPARE_NEW_VERSION` refusal — that held
identity is the only thing a later read-back can name — but for the editable refusal it left
the confirmation control armed against a request the server had already and permanently
refused. The user could not re-submit anything.

### The one unlock predicate (§3/§5)

`internal/mcpserver/confirmation_ui.html:493` `canUnlockRejectedSubmissionForUserRetry(submission,
currentIntent, heldRequest)` is the only path into the editable state, and it reads only
durable facts: `recorded === true`, `retryDisposition === 'EDIT_SELECTION_AND_RESUBMIT'`,
`outcome/businessOutcome/consumptionDisposition/consumed`,
`nextAction === 'EDIT_SELECTION'`, `redispatchSameIntent === false`, an
absent `receiptRequestId` plus a 64-hex `payloadSHA256`, `state === 'PENDING'` with
`commit == null`, the submission request matching the current intent identity and
`contentHash`, and — if a request is still held — that it is exactly the rejected one
(`:506`). Anything else fails closed. A Host transport error, a timeout, a Card refresh, a
missing successor Review or an unchanged Task cannot reach `true`, because none of them is
read here.

Reaching it (`:517`) sets `pendingCommit = null`, `pendingHash = ''`, `stale = false`,
`editableRetry = true` — the Card's name for `EDITABLE_RETRY_READY` — and shows the frozen
copy `本次确认未被消费。请修改选择或重新确认。` (`:510`). Preserved untouched:
`lastSubmission`, `lastReceipt`, `lastRejectedRequestId`, `lastRejectedPayloadSHA256`,
`lastFailure`, the 24-entry diagnostic ring, intent identity, Card entry identity, display,
content targets, reader references, Core options and the original receipt. The next
submission is a fresh user command with a fresh `requestId` and a fresh payload digest;
`redispatchSameIntent` stays `false` and nothing re-sends on its own.

Core, not the Card, decides who may unlock: `deriveRetryDisposition`
(`internal/confirmation/outcome.go:196-219`) is the single reducer behind
`submission.retryDisposition`, and `EDIT_SELECTION_AND_RESUBMIT` additionally requires
`State == "PENDING"`, `Commit == nil` and a not-yet-expired `ExpiresAt`, so an expired
intent re-derives to `REPREPARE_REQUIRED` on the very next read.

### Proof on three independent planes

| Plane | Evidence |
| --- | --- |
| Durable Core / wire | `internal/controlapi/confirmation_rejected_retry_lifecycle_test.go` — `TestConfirmationRejectedNotConsumedLeavesTheSameIntentEditable` re-reads the refusal from SQLite and asserts the durable conjunction plus `EDIT_SELECTION_AND_RESUBMIT`, and that re-sending the original `requestId` returns only the original rejection receipt. |
| Reducer table | `internal/confirmation/retry_disposition_test.go` — `TestRetryDispositionFollowsTheFrozenConsumptionTable`, `TestRetryDispositionFailsClosedForUnknownAndConsumed`, `TestRetryDispositionLeavesSubmissionlessIntentAlone`. |
| Real embedded Card driven by a real click | `internal/mcpserver/confirmation_rejected_retry_test.js` (11 tests) drives `confirmation_ui.html` itself over the shared fixture, and `internal/mcpserver/confirmation_rejected_retry_fixture_test.go` (5 tests) re-derives the same bytes in Go. |

### Mutation (red) runs — each isolates one protective layer

Three scratch variants of the Card were built in the audit directory, each replayed by the
untouched JS harness (whose HTML path is overridable via
`DEVBRIDGE_CONFIRMATION_UI_HTML`), and each proved to fail:

| Mutation | Variant main-script digest | Result |
| --- | --- | --- |
| Remove the §4 release body | `d0b85339…` | red — `logs/roundC-card-red-green-red-unlock-removed.log` |
| Bypass the predicate (unlock unconditionally) | `e788bf30…` | red — `logs/roundC-card-red-green-red-unconditional-unlock.log` |
| Drop only the receipt-pointer clause (`:500`) | `9ef55e83…` | red — `logs/roundC-card-red-green-red-validator-pointer-clause-removed.log` |

Interpretation in `logs/roundC-card-red-green-variants.txt`. So the fix is load-bearing at
the release site, at the predicate, and at the Card's §3 receipt validator — not merely
consistent with the assertions.

## Defect 2 — exact read identity with the frozen `contentHash` (§7)

* `internal/confirmation/contract.go` — `ReadInput` now carries `ContentHash` as a plain
  required field of the read identity; `OptionsInput` embeds `ReadInput`, so options cannot
  be read with a narrower identity than the intent it belongs to.
* `internal/mcpserver/confirmation_schema.go:41-50` — the public read schema requires all
  five fields (`aiSessionId`, `intentId`, `version`, `purpose`, `contentHash`) and pins
  `pattern: ^[a-f0-9]{64}$`. An empty or missing hash is rejected at the schema layer; it is
  never "validated only when present".
* `internal/controlapi/confirmation.go:185-217` — **one** Core gate. `ReadConfirmation`
  refuses a malformed hash before any lookup, then refuses when
  `out.Version != in.Version || out.Purpose != in.Purpose || out.ContentHash != in.ContentHash`.
  `confirmation_show` (`internal/mcpserver/confirmation_tools.go:478-482`),
  `confirmation_options_get` (`ConfirmationOptions` → `:221`) and the commit-side re-read
  (`:617`) all funnel through it, so a post-loss read must reuse the original
  `intentId` + `version` + `contentHash` exactly, optionally with the `requestId` the Card
  itself submitted.
* The refusal is `CONFIRMATION_READ_IDENTITY_MISMATCH` at stage `INPUT_IDENTITY`
  (`internal/controlapi/confirmation_failure.go:106-115`) and is a **pure read miss**: it
  changes no intent state, no receipt, no request-ledger row, consumes no Decision and
  mounts no Card. A Task, Review, Decision identity or resource URI is not a substitute, and
  `sourceIntentId` is never current read identity.
* No pseudo-closure (§8): there is no hash-free public read that later gains commit
  authority, and the internal id-only `Confirmations.Get` used by prepare/diagnostic paths
  emits no Card metadata, capability or submission authority — asserted by
  `internal/mcpserver/confirmation_closed_input_schema_test.go`
  (`TestConfirmationPublicReadSchemaRequiresContentHashAndRejectsClosedFields`) and
  `internal/controlapi/confirmation_read_identity_test.go`
  (`TestConfirmationExactReadIdentityRequiresFrozenContentHash`,
  `TestConfirmationReceiptReadbackKeepsExactReadIdentity`).

## Defect 3 — replacement version semantics (§10)

`internal/storage/sqlite/confirmation_store.go:227` creates every intent at `Version: 1`,
including a replacement of a still-pending intent (which gets a **new** `intentId` linked by
`sourceIntentId`). That behaviour was correct but unfrozen, so the field read as an ordinal.
Now frozen in prose and in tests:

* `docs/interfaces.md` — "Replacing a still-pending intent creates a new `intentId` at
  `version: 1` through `sourceIntentId`", `version` is therefore **not** a replacement
  ordinal, a lineage generation, a Decision version or a Task Review ordinal, and the exact
  immutable identity is `intentId + version + contentHash` with no in-place revision at v1.
* `docs/architecture/purpose-scoped-confirmation.md` — same facts, plus "`version` is never
  incremented to express a successor".
* `internal/controlapi/confirmation_replacement_lineage_test.go` —
  `TestConfirmationReplacementLineageNeverIncrementsVersion` drives an A→B→C replacement
  chain and asserts all three stay at `version: 1` with distinct `intentId`s, that ordering
  is only recoverable through `sourceIntentId`, and that reading an older link with a newer
  hash is refused as a read miss.
* `internal/mcpserver/confirmation_canonical_docs_test.go` — `phase4CanonicalDocumentationFreeze`
  / `…Prohibitions` assert the canonical prose with the code, so a later comment rewrite
  cannot narrow the contract, and forbidden pseudo-closure phrases
  (`optional contentHash`, hash-free continuation string, `version increments to`, …) fail
  the build if they ever appear.

## §12 closed DTO

The public input schemas accept none of `consumed`, `consumptionDisposition`,
`retryDisposition`, `manualRetryAllowed`, `redispatchSameIntent`, `reprepareAllowed`, a
receipt outcome, a `receiptRequestId` override, the durable intent's own `contentHash`,
`sourceIntentId` as current identity, a replacement version or a lineage ordinal; there is
no `force`, `retryAnyway` or `assumeNotConsumed` anywhere in the contract. A supplied digest
is verified against stored content and is never trusted as an identity the caller defines.
`TestConfirmationCommitSchemaRejectsClientInjectedCoreFacts` and
`TestConfirmationPublicReadSchemaRequiresContentHashAndRejectsClosedFields` pin both ends.

## Card asset identity (§16)

| Asset | Value |
| --- | --- |
| `confirmation_ui.html` sha256 | `47631673a281cd1378ef76a57326a72dea1458a71478f4b24b3cc902fb2ab3af` |
| `<script id="confirmation-main">` region sha256 (advertised `mainScriptSHA256`) | `e090267f938c679afbfd0c0a6004772c402a6a7acee41d5d198927b9db080e6a` |
| Phase-4 opening digest of the same region | `34a2de03209f518128aad57cdb907794fdd4350b68a5e585c568885d36a76ff2` |
| Resource URI / component | `ui://devbridge/confirmation/v5` / `PURPOSE_CONFIRMATION_V5` (unchanged) |
| `scripts/build-chatgpt-ui.mjs` manifest | `chatgpt-ui-build/manifest.json` — 2 self-contained assets, neither of them the Confirmation Card |

The Card is embedded with `//go:embed confirmation_ui.html` and served byte-for-byte; no
build step transforms it, so the file digest above *is* the resource-bundle identity, and the
ChatGPT UI build manifest legitimately does not list it (it covers
`dev_bridge_ui.html` and `feature_area_details_ui.html`, both unchanged this round).
`TestConfirmationCardAssetIdentityMatchesDeployedResource` proves **source == served bytes ==
Host-recomputable hash of the served bytes == advertised tool meta == resource meta**, by
re-hashing the deployed text independently of `confirmationScriptSHA256()`.

**One re-record, and the guard that forced it.** After the Card changed,
`TestSharedCardInstanceFixtureEnvelopesStillMount` failed rather than replaying stale bytes
(`logs/roundB-readidentity-packages.log`: `fixture was captured from a different Card script
("34a2de03…" != embedded "e090267f…")`). Both shared fixtures were re-captured from the live
production MCP wire with the documented `DEVBRIDGE_…_FIXTURE_DIR` procedure — never
hand-edited — and the package went green (`logs/roundD-mcpserver-after-re-record.log`). Pre-
and post-record copies are kept under `fixtures-before/`. Nobody is asked to clear a cache:
a mismatched Card is refused, and the delivered Card and the advertised identity are one
value.

## Scenario gates (§14, all on test-owned `t.TempDir()` DataRoots)

The spec numbers its gates G1…G16; that per-gate wording arrived only as a message
attachment and is not recoverable from disk, so the gates are published **by substance** —
one named running test plus one named log per row — instead of guessing at numbering. No row
is a helper-only, compile-only, zero-match or skipped test (§15).

| # | Scenario the round had to close | Running proof | Log |
| --- | --- | --- | --- |
| 1 | Durable non-consumed rejection re-derives the full §3 conjunction on the same still-pending intent, and re-sending the original `requestId` only returns that rejection | `controlapi.TestConfirmationRejectedNotConsumedLeavesTheSameIntentEditable` | `round1-focused-go.log`, `round10-root-aggregate-final.log` |
| 2 | `retryDisposition` follows the frozen consumption table and never leaks an editable disposition from a consumed/unknown/unrecorded submission | `confirmation.TestRetryDispositionFollowsTheFrozenConsumptionTable`, `…FailsClosedForUnknownAndConsumed`, `…LeavesSubmissionlessIntentAlone` | same |
| 3 | A real click on the real embedded Card releases the held request and re-arms the control (§3/§4) | `confirmation_rejected_retry_test.js` — "an explicit REJECTED_NOT_CONSUMED with EDIT_SELECTION releases this Card's held request" | `round2-js-mcpserver.log`, `round11-js-final.log` |
| 4 | The retry is a new command: new `requestId`, new payload digest, one commit, no replay (§3/§4) | same file — "the retry is a new user command with a new request identity, not a replay" | same |
| 5 | A refresh over the durable refusal sends nothing and stays unlocked (§5) | same file — "a refresh over the durable refusal stays unlocked and sends nothing" | same |
| 6 | A Host transport error / timeout is never non-consumption evidence; the held request and its read identity survive and nothing re-fires (§5) | same file — "a Host transport error alone never proves non-consumption" | same |
| 7 | Expired content cannot be re-submitted from an unlocked Card; it re-derives to `REPREPARE_REQUIRED` (§5 clock negative) | same file — "expired content may not be re-submitted from an unlocked Card" | same |
| 8 | Four §5 negatives stay locked: an unrecorded read, a re-prepare refusal, a rejection carrying a consumption pointer, a refusal recorded against another request | same file, the four generated negative tests | same |
| 9 | The same durable bytes re-project through Go: fixture provenance, mount legality, re-derivation, predicate parity, and the Host mount gate refusing a broken retry claim | `mcpserver.TestSharedRejectedRetryFixtureIsFromThisEmbeddedCard`, `…EnvelopesStillMount`, `…RefusalReDerivesItsRetryFacts`, `…UnlockPredicateMatchesDurableBytes`, `…MountGateRefusesBrokenRetryClaims` | `roundD-mcpserver-after-re-record.log`, `round12-mcpserver-race-after-doc-edit.log` |
| 10 | `contentHash` is required on every public read surface and a wrong/absent hash is a pure read miss that mutates nothing; a foreign `version` or `purpose` under a real digest is the same miss (§7/§8) | `controlapi.TestConfirmationExactReadIdentityRequiresFrozenContentHash` (incl. 6 `off-identity version/purpose is refused` subtests), `mcpserver.TestConfirmationPublicReadSchemaRequiresContentHashAndRejectsClosedFields` | `round1-focused-go.log`, `roundF3-doc-pin-green-after-restore.log`, `roundG-read-identity-version-purpose-red.log` (red), `round13-controlapi-after-probe-addition.log` |
| 11 | The lost-response read-back reuses the original `intentId` + `version` + `contentHash` exactly, optionally with the submitted `requestId` (§7) | `controlapi.TestConfirmationReceiptReadbackKeepsExactReadIdentity` | `round1-focused-go.log` |
| 12 | Replacement lineage: A→B→C all stay at `version: 1`, ordering only through `sourceIntentId`, stale link + fresh hash refused as a read miss (§10) | `controlapi.TestConfirmationReplacementLineageNeverIncrementsVersion` | `round1-focused-go.log`, `round10` |
| 13 | Clients cannot inject any derived consumption/retry fact, the durable `contentHash`, a receipt pointer or a lineage ordinal (§12) | `mcpserver.TestConfirmationCommitSchemaRejectsClientInjectedCoreFacts`, `…PublicReadSchemaRequiresContentHashAndRejectsClosedFields` | `roundF3…log` |
| 14 | Card asset identity: source == served == Host-recomputable == advertised tool meta == resource meta (§16) | `mcpserver.TestConfirmationCardAssetIdentityMatchesDeployedResource` | `roundD…log`, `round12…log`, `final-card-identity.txt` |
| 15 | Canonical prose freezes §7/§10/§3 vocabulary and rejects the §8 pseudo-closure phrases | `mcpserver.TestPhase4CanonicalDocumentationFreezesReadIdentityRetryAndVersionSemantics` | `roundF…log` (green), `roundF2/F…red.log` (mutation), `roundF3…log` |
| 16 | No live object was touched: every Confirmation test in the three packages allocates its own DataRoot | `newUpperLifecycleFixtureScoped` → `t.TempDir()` (`multi_repository_upper_lifecycle_test.go:66-67`); the string `Application Support` appears **nowhere** in `internal/` (0 matches in any file, test or non-test), and the user's live DataRoot processes were left running and untouched | §2 isolation check, this document |

## Race gates (narrow, re-runnable, with RUN-count proof)

`-race` was run narrowly on the touched packages rather than across the whole repository:

| Gate | Result | Log |
| --- | --- | --- |
| targeted `-race` (controlapi / mcpserver / sqlite / confirmation) | ok 231.273s / 2.395s / 35.878s / 1.506s | `round3-targeted-races.log` |
| same run's `-v` RUN counts (9 / 5 / 23 matched tests — no zero-match run counted) | proof written | `round3b-race-run-counts.log` |
| `-race ./internal/mcpserver/ -run Confirmation` + `-race ./internal/storage/sqlite/ -run TestConfirmation` | ok 29.857s / 34.777s, 0 `DATA RACE` | `round7a-package-races-mcp-sqlite.log` |
| `-race ./internal/controlapi/ -run Confirmation` (22 matched tests) at `-timeout=8m` | **panicked at the 8m wall-clock timeout**, 0 `DATA RACE`, 0 `--- FAIL` | `round7b-package-races-controlapi.log`, `round7b-timeout-classification.txt` |
| the identical gate re-run at `-timeout=25m`, nothing narrowed or skipped | ok 838.899s, exit 0 | `round7c-package-races-controlapi-extended-timeout.log` |
| `-race ./internal/mcpserver/ -run Confirmation` after the canonical-doc test edit | ok 29.991s | `round12-mcpserver-race-after-doc-edit.log` |
| `-race ./internal/mcpserver/ -run Confirmation` after the vectors re-word — 21 tests + 11 subtests, 32 RUN / 32 PASS / 0 FAIL / 0 SKIP | ok 29.702s (and `ok 30.078s` under `-v`) | `round16-mcpserver-race-and-diff-hygiene.log` |

## Aggregates, vet, JavaScript, frontend, builds, diff hygiene

| Gate | Result | Log |
| --- | --- | --- |
| focused Go (confirmation / sqlite / controlapi / mcpserver) | ok ×4 | `round1-focused-go.log` |
| first-run red record after the contract change | 73 failing assertion lines in `mcpserver` + `controlapi` | `roundB-first-run.log` |
| root aggregate `go test ./... -count=1` | 65 packages ok, 0 FAIL, exit 0 | `round9-root-aggregate-after-doc-edits.log`, then **re-run green** `round10-root-aggregate-final.log` |
| intermediate aggregate after the read-identity probes | 65 packages ok, 0 FAIL, exit 0; vet exit 0; build exit 0 — started before the closing vectors edit, so **not** the final record | `round14-final-aggregate-vet-build.log` |
| **closing** root aggregate + vet + JS + build over the final tree | 65 packages ok, 0 FAIL, exit 0; vet exit 0; Card JS 195/191/0/4-skip exit 0; BridgeHTTP JS 116/116 exit 0; build exit 0 | `round15-final-closing-tree.log` |
| superseded aggregate | `round4-root-aggregate.log` (65 ok) ran **before** the closing doc edits and is retained, not presented as final | same |
| `go vet ./...` root / desktop | exit 0 / exit 0 | `round5-vet-desktop.log` |
| desktop aggregate `go test ./...` | 1 failure classified env-flaky, full re-run 8/8 ok | `round5-vet-desktop.log`, `round5b-desktop-rerun.log` |
| desktop frontend `npm test` / `npm run build` | 52/52 pass; Vite build ok | `round6-desktop-frontend.log` |
| `node --test internal/mcpserver/*_test.js` | 195 tests, 191 pass, 0 fail, 4 skipped | `round2-js-mcpserver.log`, re-verified `round11-js-final.log`, closing re-run `round15-final-closing-tree.log` |
| `node --test internal/bridgehttp/*_test.js` | 116/116 pass, 0 fail | same three logs |
| self-contained ChatGPT UI build | 2 assets validated, manifest recorded | `chatgpt-ui-build/manifest.json`, re-validated into `chatgpt-ui-build-round15/` |
| `git diff --check` / `git diff --cached --check` | both clean, 0 staged paths, HEAD unchanged | `round8-diff-check.log`, re-run on the closing tree `round16-mcpserver-race-and-diff-hygiene.log` |

### Desktop probe flake classification

`desktop/internal/codex.TestProbeAbsoluteNVMStyleLauncherFindsSiblingNode` failed once inside
the round5 chain with `Codex version probe timed out` (a 3s budget spent spawning a fake
nvm-style launcher while the same chain ran under load beside the user's live
`devbridge -mode server`, `devbridge-mcp`, `devbridge-local-mcp` and a `wails dev` process).
Phase 4 changed **zero** desktop files — all 59 tracked `desktop/` paths hash-identical to
`opening-manifest.sha256`. Isolation re-run `-count=2` → ok 1.377s; the desktop aggregate
re-run → 8/8 ok, exit 0 (`round5b-desktop-rerun.log`). Reported as flaky-by-environment, not
as a pass, and not counted toward any gate.

## Honest disclosures

1. **§13 Desktop/Console/Workbench read parity is vacuous.** Those surfaces contain no
   Confirmation read caller at all — the only desktop "confirmation" strings are
   `DesktopRepositoryUnlinkConfirmation` and the `projects.unlink` dialog, and no
   `confirmation_get` / `confirmation_options_get` / `confirmation_show` / `c.ReadInput` use
   exists anywhere under `desktop/` or `internal/consoleapi`. The requirement is therefore
   proven where callers do exist (public schema, the single Core gate, the Card's own
   `readInput()` / `receiptInput()`), and **no** Desktop/Console/Workbench test was added or
   counted for §13. Evidence: `logs/roundE-sec13-read-parity-vacuity.txt`.
2. **Four pre-existing `# SKIP` Card tests do not count as running acceptance (§15):**
   "production orphan Card transport payload preserves disposition in mounted DOM",
   "candidate authoritative Review and Implementation snapshots replace one mounted Card",
   "candidate Approval action continues into Implementation on the same mounted Card",
   "candidate exact durable Review completion sends one host follow-up while Approval
   remains pending". They predate this round, are unrelated to it, and the 191/195 pass line
   is quoted with the skips visible rather than folded into "all green".
3. **A canonical-doc value error introduced by this round's own editing was caught and
   fixed.** The first draft of the §3/§4 sentences in `docs/interfaces.md` and
   `docs/architecture/purpose-scoped-confirmation.md` wrote `businessOutcome = REJECTED`,
   but the public enum the contract actually emits is `REJECTED_NOT_CONSUMED`
   (`internal/confirmation/outcome.go:179`,
   `internal/mcpserver/confirmation_projection.go:32`, and the shared fixture's own invariant
   string). Both docs now carry the real value, and
   `confirmation_canonical_docs_test.go` was extended to freeze
   "`businessOutcome = REJECTED_NOT_CONSUMED`, `consumptionDisposition = NOT_CONSUMED`" in both
   files and to prohibit "`businessOutcome = REJECTED`". Red/green proven both ways:
   `roundF-doc-value-pin-red.log` / `roundF2-arch-doc-value-pin-red.log` (each mutation
   fails; both files restored byte-identically — `c925c65c…` and `e2e40964…`), and
   `roundF3-doc-pin-green-after-restore.log` (17 PASS, 0 FAIL). The root aggregate
   (`round10`) and both JS suites (`round11`) were re-run after the correction; `round9` is
   retained as the intermediate pass.
4. **Nothing was weakened to reach green.** The contract change first failed 73 assertion
   lines (`roundB-first-run.log`). In the closing tree: 0 added `t.Skip`/`test.skip` in any
   tracked diff, 0 skips in the new test files, 0 deleted `func Test` in tracked diffs, and
   `increment.json` `deletedByRoundCount = 0`.
5. **Manual Host acceptance is not automated.** Whether the real ChatGPT Host honours
   `devbridge/presentationInstanceId` for a replacement intent stays open
   (`CONFIRMATION_REPLACEMENT_INTENT_CARD_PRESENTATION_HOST_ACCEPTANCE_PENDING`,
   `CONFIRMATION_NEW_INTENT_CARD_INSTANCE_BINDING_HOST_ACCEPTANCE_PENDING`,
   `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`). This round proves the server-side and
   Card-side contracts; it does not claim a human accepted the UI.

## §17 Independent read-only review

A separate reviewer was given the working tree, the three defect claims and the frozen
invariant list, with instructions to stay read-only (no edits, no git writes, no test
execution) and to cite `file:line`. It independently re-derived the Card digest
(`e090267f…`, matching `_meta.mainScriptSHA256` in the shared rejected-retry fixture) and
returned **no BLOCKER and no MAJOR finding**. Its ten MINOR/NOTE findings and this round's
disposition of each:

| # | Finding | Disposition |
| --- | --- | --- |
| 1 | MINOR — the unlock clears `stale` (`confirmation_ui.html:517`) even though Core's reducer never inspects `failure.reasonCode`, so a hypothetical producer pairing `nextAction = EDIT_SELECTION` with a stale capability code would re-arm the control. Safe today only because `commit()` re-reads options as a preflight and Core revalidates. | **Documented limitation, not fixed.** No `PreparedConfirmation` path emits that pairing and no contract vector mutates `reasonCode` while keeping `EDIT_SELECTION`, so a guard could not be exercised without inventing a producer. Tightening the predicate changes the Card digest and forces a full asset re-record, which is outside this round's scope. Carried forward, with the reviewer's exact repro recipe. |
| 2 | MINOR — the canonical docs' "requires all of" lists omitted three conjuncts Core actually enforces (`state = PENDING`, no frozen `commit`, `expiresAt` in the future). | **Fixed this round.** Both docs now state them next to the disposition, `confirmation_canonical_docs_test.go` pins both spellings plus "re-derives to `REPREPARE_REQUIRED`", and the hand-authored contract-vector metadata (`testdata/confirmation_card_contract_v5.json → _meta.retryDispositionContract`) now carries the same full conjunction. The vectors file changed in exactly two metadata strings — still 379 lines, and its 106 vectors plus 6 captured envelopes were compared equal to the pre-edit copy (`scratch/contract-vectors.before.json`); Go harness ok and the three Card JS files replay 49/49 with 0 skips (`logs/roundH-contract-vectors-metadata.log`). The *captured* fixtures' `_meta` text was deliberately left as captured rather than hand-edited, per the never-hand-edit-a-captured-file convention. |
| 3 | MINOR — `CONFIRMATION_READ_IDENTITY_MISMATCH` also covers a foreign `version` or `purpose` (`internal/controlapi/confirmation.go:200`), but `docs/interfaces.md` described it as a digest-only refusal and no probe exercised the other two dimensions. | **Fixed this round.** The doc sentence now reads "whose digest, `version` or `purpose` differs from the durable frozen intent", and `TestConfirmationExactReadIdentityRequiresFrozenContentHash` gained `off-identity version is refused` / `off-identity purpose is refused` for all three read surfaces (6 new subtests, schema-valid inputs, asserting the code, the stage, absence of business facts and no Card mount, inside the existing "durable row byte-identical / ledger unchanged" proof). Red-run `logs/roundG-read-identity-version-purpose-red.log`: narrowing that comparison to `contentHash` alone fails exactly those 6 subtests and nothing else, which also confirms the reviewer's point that the behaviour was previously unpinned. Restored byte-identical (`3af8bc70…`). |
| 4 | NOTE — `confirmation_ui.html:507` (`heldRequest.requestId !== request.requestId`) can never fire, because `:513` assigns `pendingCommit` from that very submission before the predicate sees it; the comment at `:505-506` over-claims. | **Disclosed, unchanged.** The clause is inert defence-in-depth; the real anti-forgery tie is `SUBMISSION_REQUEST_CONFLICT` in `validateSubmission` (`:480`), which runs before reassignment and is exercised by the `foreignRequest` negative and vector `retry_editable_submission_request_conflict`. Editing the comment would move the Card digest for a wording-only gain. |
| 5 | NOTE — the Card predicate does not itself test expiry (Core does). | **Disclosed, unchanged.** Expiry reaches the Card through the re-derived disposition, through `choiceError()` at submit, and is pinned by the JS clock negative ("expired content may not be re-submitted from an unlocked Card"). |
| 6 | NOTE — `EDITABLE_RETRY_READY` is a documentation/vocabulary name, not an observable field: `submissionState()` reports `REJECTED` and the observable Card fact is `editableRetry === true`. | **Disclosed.** The state's *observable* consequences are what the tests assert (held request released, frozen copy rendered, commit re-armed, one new command), and the doc-pin keeps the name from drifting. It is not claimed as a wire or DOM field. |
| 7 | NOTE — frozen-content *composition* changed although the hash algorithm did not: `ReviewBody` carries `preparedTopologyRevision` and `preparedDevelopmentContextHash` (`internal/confirmation/contract.go:53-54`), which fold into `contentHash` for newly prepared Review intents. | **Verified not this round, and disclosed.** `InputHash`/`ValueHash` bodies are untouched; digests already frozen in the captured fixtures still match, and every digest-parity, lineage and asset-identity test is green. Those two fields exist nowhere at HEAD, so they belong to the earlier uncommitted phases of this same tree. |
| 8 | NOTE — an options-read availability regression: `validatePreparedConfirmation`'s `projectSnapshot()` re-raises raw git/context errors, so one transient git failure now hard-fails a previously degrading options read. | **Outside this round.** It lives in `internal/controlapi/confirmation_validation.go`, which is byte-identical to this round's opening manifest and is not in `increment.json` — Phase 4 neither introduced nor fixed it. Handed to the next round with the reviewer's description. |
| 9 | NOTE — the working tree is broader than "exactly three defects" (commit-fence transactions, prepare read-back, display fields, eligibility, presentation registry, `ClassifyPreparationState`). | **Attributed, not disputed.** The whole four-phase chain is one uncommitted tree, so a diff against HEAD cannot separate rounds. The round-scoped record is `increment.json`, computed against this round's own opening manifest: 10 created, 34 modified, 0 deleted, 1,949 unchanged. None of the listed broader items is in that 44-file set except where §7/§3 forced a contract-shape update, and every fence the reviewer names emits `PREPARE_NEW_VERSION` — never `EDIT_SELECTION` — so no fence can reach the unlock. |
| 10 | NOTE — `eligibility.preparedGeneration` (a raw `contentHash`) and `currentGeneration` (a `ValueHash` over content hash + topology revision + decision version) are not commensurable. | **Outside this round, disclosed.** Nothing compares them today; recorded here so the next round does not start from zero. |

### What the reviewer tried to break and could not (its own "verified clean" list)

* `RetryDisposition` is assigned at exactly one site and `deriveRetryDisposition` has no
  second caller; `redispatchSameIntent` is forced `false` in Core and rejected if not
  `false` on the Card, in the Go mirror and in the schema `allOf`.
* Unlock needs all ten predicate checks **plus** `RETRY_DISPOSITION_CONFLICT` re-validation
  **plus** the enclosing `recorded && REJECTED_NOT_CONSUMED && PENDING && !commit` branch.
  Unrecorded, foreign receipt pointer, `OUTCOME_UNKNOWN`, `STALE`, non-PENDING, committed and
  laundered Host errors are each separately refused.
* Host error / timeout / refresh cannot unlock: `caught()` never sets `confirmedOutcome`, and
  the two JS negatives assert exactly one commit and a still-held request.
* New-command proof holds on both halves — Card (`requestId` differs, payload digest differs,
  frozen identity fields identical) and Core (re-sending the original `requestId` returns the
  original payload and keeps `confirmation_commit_requests` at one row; a changed payload
  under the original id yields `CONFIRMATION_REQUEST_CONFLICT`).
* Read identity: one shared schema with `additionalProperties: false` used by get/show/options
  and inherited by commit; one Core gate ahead of any write; no other public surface resolves
  an intent by id/version; `Confirmations.Get` is reachable only through the gated facade.
* Closed DTO: 17 forbidden names rejected on all four input schemas;
  `manualRetryAllowed` exists nowhere in the repository; `Options` no longer echoes the input
  identity.
* Version semantics: `Version: 1` intact, no `ORDER BY version` / `MAX(version)` anywhere in
  confirmation code, and the A→B→C lineage test fails if the field becomes an ordinal.
* The "Card half" really is the shipped HTML: the JS harness evaluates the embedded script
  body with only DOM/postMessage/timers shimmed, and asserts it is a single strict IIFE.
* UNKNOWN is not relaxed anywhere, and no read or unlock materializes a successor Review
  (`reviews` / `runs` row counts asserted unchanged across every probe).

Reviewer's own change-of-mind conditions for #1/#5 (a contract vector that keeps
`EDIT_SELECTION` while setting a stale `reasonCode` or a past `expiresAt`, plus an assertion
that the Card then refuses) are recorded verbatim above so the next round can act on them.

## Post-review re-verification (roundG – round16)

Findings #2 and #3 changed files *after* the §16/review tables above were drafted, so every
gate they touch was re-run rather than re-asserted. Nothing in this section narrows an
earlier gate; each row states what was re-run and why.

| Round | What ran | Result | Why it was needed |
| --- | --- | --- | --- |
| roundG | Mutation red run: `internal/controlapi/confirmation.go` read-identity comparison narrowed to `contentHash` alone | exactly **6** `--- FAIL` subtests (`confirmation_get` / `confirmation_options_get` / `confirmation_show` × `off-identity version is refused` / `off-identity purpose is refused`) and **nothing else**; mutated sha `93cd508e…`, correct sha `3af8bc70…` | Proves finding #3's behaviour was genuinely unpinned before this round, and that the 6 new probes in `internal/controlapi/confirmation_read_identity_test.go` are the ones holding it. File restored byte-identically. |
| roundH | `go test ./internal/mcpserver/ -run Confirmation` + the three Card JS files that replay the shared vectors | Go `ok … 0.437s` (6 PASS lines); Card **49 tests / 49 pass / 0 fail / 0 skipped** | The re-worded `_meta.retryDispositionContract` strings are *read* by both harnesses; this proves the 379-line vectors file still parses, still carries 106 vectors + 6 captured envelopes equal to `scratch/contract-vectors.before.json`, and replays identically. |
| round13 | `go test ./internal/controlapi/ -run Confirmation` focused, then scoped `-race` on the extended read-identity test | `ok 36.175s`, then `ok 86.613s`; **46 RUN / 46 PASS / 0 FAIL+SKIP** | The 6 probes were added after round7c's controlapi race, and the race had already cost 14 minutes there, so the extended surface was raced narrowly instead of re-running the whole 22-test package race. |
| round14 | root aggregate + vet + self-contained UI build | 65 packages `ok`, 0 FAIL, exit 0 / vet exit 0 / build exit 0 | Retained as an **intermediate**: it started at `11:53:31Z`, before the 11:55Z vectors edit, so it cannot be the closing record. |
| round15 | **Definitive closing tree**: `go test ./... -count=1 -timeout=25m`, `go vet ./...`, `node --test` over `internal/mcpserver/*_test.js` and `internal/bridgehttp/*_test.js`, `node scripts/build-chatgpt-ui.mjs …/chatgpt-ui-build-round15` | **65 packages ok, 0 FAIL, aggregate exit=0**; vet exit=0; Card JS **195 tests / 191 pass / 0 fail / 4 skipped** exit=0; BridgeHTTP JS **116/116** exit=0; build exit=0 (2 self-contained assets) | Re-runs every gate over the tree after the last content edit. Only `docs/validation/` (this report) changed afterwards, and no Go, JS or script reader references that directory (repo-wide grep for `docs/validation`: 0 matches), so no gate depends on this file. |
| round16 | `-race ./internal/mcpserver/ -run Confirmation` over the closing bytes, then diff hygiene | race `ok 29.702s` and `-v` re-run `ok 30.078s`; **21 top-level tests + 11 subtests = 32 RUN / 32 PASS / 0 FAIL / 0 SKIP / 0 `DATA RACE`**; `git diff --check` exit 0, `git diff --cached --check` exit 0, **0 staged paths**, HEAD `3f3309d1…`, 62 porcelain lines | round12's mcpserver race predates the vectors re-word, and that file is an input to this package's harness. Production bytes did not change (see the digest table below), so this closes the race surface for what actually changed. |

### Which bytes each closing gate stands on

| File | Closing-tree sha256 | Note |
| --- | --- | --- |
| `internal/controlapi/confirmation.go` | `3af8bc7049f67f67acc529feec32780de65a39ce821cd0cbd94b9cbf2ac0da27` | Production. Identical to the digest recorded in the round13 header, so round3/3b, round7a/7b/7c and round13's races all stand on the closing bytes. |
| `internal/mcpserver/confirmation_ui.html` | `47631673a281cd1378ef76a57326a72dea1458a71478f4b24b3cc902fb2ab3af` | Unchanged since the §16 re-record, so the delivered-asset identity table above is the closing value and `mainScriptSHA256` `e090267f…` was not disturbed by the review fixes. |
| `internal/mcpserver/testdata/confirmation_card_contract_v5.json` | `90af3d1aeec871b66b7dce15d6b8598bfb82a078993161684c71705685ed05af` | 379 lines; two `_meta` strings only (finding #2). Driven green by roundH, round14, round15 (Go + Card JS) and round16 (race). |
| `internal/controlapi/confirmation_read_identity_test.go` | `f2d1e6cf325a4b9592e4889df12c3e528a0381a14956baf7470d3daf4dd9e17c` | 265 lines; test-only (finding #3). Green in round13/14/15, raced in round13 and covered by round16's package race. |
| `internal/mcpserver/confirmation_canonical_docs_test.go` | `bed06b3241a78a72fd316810e3e98d0521fed943a205e04612b43a8bc2338726` | Test-only; pins the new doc sentences plus the `businessOutcome = REJECTED` prohibition. Green in round14/15 and raced in round12/16. |
| `docs/interfaces.md` | `381d0da3d728c8bc411ed7187697935e28f7fe01ca1bd95e4471afa72611c14e` | 795 lines. |
| `docs/architecture/purpose-scoped-confirmation.md` | `7140f9cf6c3eccff3d70441e0e73f3f221f02b03d4b7c5e0b803293ddc06a3cf` | 870 lines. |

One clarification so no digest in this report is read out of context: the roundF/roundF2/roundF3
mutation cycle above restored both canonical docs to `c925c65c…` and `e2e40964…` *at that
moment*, and that is what disclosure 3 quotes. Both files were then legitimately edited again
for review findings #2 and #3, so the closing values are `381d0da3…` and `7140f9cf…`. The
freeze/prohibition test that governs them is green in round14 and round15, and no captured
fixture was rewritten in between.

## Safety confirmations

- **No git writes.** Nothing was staged, committed, merged, rebased, pushed, reset, cleaned or
  checked out at any point: `git diff --cached --name-only | wc -l` = **0**,
  `git diff --check` and `git diff --cached --check` both exit 0, and HEAD is still
  `3f3309d15a8b06a973db90ea213a79972330ed3e` on branch `dev_20260908_1913` (round16 log).
- **No live objects touched.** SQLite schema head remains 87; every gate allocated its own
  `t.TempDir()` DataRoot, intent, Task, Review, Decision and Confirmation; no running user
  process was signalled, restarted or inspected for writes. The user's live
  `devbridge -mode server`, `devbridge-mcp`, `devbridge-local-mcp` and `wails dev` processes
  were left exactly as found — they are also why the round7b wall-clock timeout was
  classified as environment pressure rather than narrowed away.
- **No forbidden contract weakening.** No `force`, `retryAnyway` or `assumeNotConsumed` option
  added anywhere; `UNKNOWN` not relaxed; no automatic redispatch, no automatic re-prepare, no
  successor Review materialized; the `contentHash` algorithm untouched; no version increment
  replacing a new `intentId`; `sourceIntentId` never used as current read identity; Card
  silence never treated as non-consumption evidence. Each of these is asserted positively in
  the named tests of the §14 table, and §17's "verified clean" list is the reviewer's own
  independent confirmation.
- **Round attribution.** The increment is computed against this round's own opening byte
  manifest, not against HEAD's cumulative diff (§1).

## §19 Publication markers

Every §14 scenario gate, every race gate, the root aggregate, root and desktop vet, both
JavaScript suites, the desktop frontend suite and build, the self-contained ChatGPT UI build
and the diff-hygiene checks are green on the closing tree (round15 and round16 above), the
byte manifest and protection manifest were recomputed as the last repo-touching observation,
and the independent review returned no BLOCKER and no MAJOR finding with all ten
MINOR/NOTE items either fixed or disclosed with a reason. Only on that basis are the round's
markers published:

```text
CONFIRMATION_REJECTED_NOT_CONSUMED_MANUAL_RETRY_CLOSED
CONFIRMATION_PENDING_COMMIT_UNLOCK_CLOSED
CONFIRMATION_EXACT_READ_IDENTITY_CONTENT_HASH_CLOSED
CONFIRMATION_REPLACEMENT_INTENT_VERSION_SEMANTICS_CLOSED
CONFIRMATION_REJECTED_NOT_CONSUMED_RETRY_AND_READ_IDENTITY_CLOSED
```

Carried forward, still closed by the tests that remain in the tree (§19 retained — 11):

```text
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

Still open, by definition and not by omission (§19 stay open — 7):

```text
CONFIRMATION_REPLACEMENT_INTENT_CARD_PRESENTATION_HOST_ACCEPTANCE_PENDING
CONFIRMATION_NEW_INTENT_CARD_INSTANCE_BINDING_HOST_ACCEPTANCE_PENDING
PARTIAL_IMPLEMENTATION
REAL_CODING_AGENT_E2E_NOT_RUN
MANUAL_DESKTOP_ACCEPTANCE_PENDING
MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING
CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED
```

Explicitly **not** reported, because none of them happened (§19 不得报告 — 8):

```text
UNKNOWN_RETRY_ENABLED
AUTOMATIC_REDISPATCH_ENABLED
SOURCE_INTENT_USED_AS_CURRENT_IDENTITY
REPLACEMENT_VERSION_INCREMENTED
DUPLICATE_CONFIRMATION_CREATED
LIVE_CONFIRMATION_MUTATED
MANUAL_CHATGPT_CARD_ACCEPTED
GLOBAL_FINAL_ACCEPTANCE_PASSED
```

The two `…_HOST_ACCEPTANCE_PENDING` markers and `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING` are
the honest edge of this round: an automated test can prove the server projects
`devbridge/presentationInstanceId` and that a Card refuses another intent, and it can prove
that a human-accepted UI was **not** observed. No automated run was relabelled as acceptance.

## Closing state

Round artifacts live outside the tracked tree under
`/private/tmp/devbridge-confirmation-rejected-not-consumed-retry-and-read-identity-audit/`
(`opening-manifest.sha256`, `final-manifest.sha256`, `increment.json`,
`protection-manifest.json`, `logs/`, `chatgpt-ui-build-round15/`, `audit-index.json`), and
this report deliberately does not quote its own digest.

结束时不暂存、不提交、不合并、不 push。
