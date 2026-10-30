# New Confirmation intent gets its own Card instance — verification and closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-ea9369e96c2828e8)。

This round fixes exactly one live-proven problem: a **new, complete, PENDING,
committable** Confirmation intent `B` was created, while the displayed Purpose
Confirmation Card stayed bound to the **old SUPERSEDED** intent `A` and reported
`原确认已过期` with no re-review action. The frozen goal is that `A` remains
history (SUPERSEDED, uncommittable), `B` receives its **own independent Card
instance** (PENDING, offering its own confirmation action), a successor Review for
the same Task appears **only after commit**, and `B` never reuses `A`'s identity,
business state, capability or receipt.

## Baseline

| Field | Opening | Final |
| --- | --- | --- |
| branch | `dev_20260908_1913` | `dev_20260908_1913` (unchanged) |
| HEAD | `3f3309d15a8b06a973db90ea213a79972330ed3e` | unchanged |
| schema | v87 (`invalidReviewResolutionV87SQL` is the last migration) | v87 — this round adds **no** migration |
| Card contract | `PURPOSE_CONFIRMATION_V5` / `ui://devbridge/confirmation/v5` | unchanged (V5) |
| index (staged) | 0 entries | 0 entries |
| `git status --porcelain` | 32 entries | 41 entries (+9 new paths, 0 lost) |
| non-ignored byte manifest | 1,966 files (Phase-2 final) | **1,983** files |

Audit directory: `/private/tmp/devbridge-confirmation-new-intent-card-instance-identity-audit/`.

Artifact digests live in `audit-index.json` there, computed **after** the final
repository edit (this document cannot quote its own closing digest). Each entry records
`{path, sha256, bytes}` for: `final-manifest.sha256` (1,983 tracked+untracked,
non-ignored files), `increment.json`, `protection-manifest.json` (45 protected Card/JS
files, **0 missing**, 2 legitimately changed: `confirmation_ui.html` and
`confirmation_project_ui_test.js`), and every gate log named below.

### Inherited dirty vs this round's actual increment

No byte manifest was captured at this round's opening, so the round's own scope is
attributed from the opening porcelain snapshot plus file mtimes inside the round's
window. Against that attribution this round created **8** paths, modified **6**, and
deleted **0**:

Created: `internal/mcpserver/confirmation_instance.go`,
`internal/mcpserver/confirmation_instance_test.go`,
`internal/mcpserver/confirmation_card_instance_fixture_test.go`,
`internal/mcpserver/confirmation_card_copy_guidance_test.go`,
`internal/mcpserver/confirmation_card_instance_mount_test.js`,
`internal/controlapi/confirmation_card_instance_lifecycle_test.go`,
`internal/mcpserver/testdata/confirmation_card_instance_lifecycle_v5.json`,
and this closure document.

Modified: `internal/mcpserver/confirmation_ui.html`,
`internal/mcpserver/confirmation_tools.go`,
`internal/mcpserver/external_workflow.go`,
`internal/mcpserver/confirmation_project_ui_test.js`,
`internal/controlapi/multi_repository_upper_lifecycle_test.go`, `docs/interfaces.md`.

`internal/mcpserver/external_workflow.go` is tracked at HEAD and was clean when this
round opened, so it is a modification, not a creation — an earlier draft of this
section and of `increment.json` listed it under *created*; both are corrected here and
the manifest was regenerated after the correction.

`internal/confirmation/outcome.go` and `internal/mcpserver/confirmation_schema.go`
also differ from the older round manifests but were last written **before** this
round opened (01:27 and 22:47 the previous day); they are inherited, not this
round's work, and `increment.json` records them separately, together with the other
18 paths that were already dirty at opening and that this round did not touch. Nothing
inherited was reverted, cleaned or committed.

## §3 Root-cause classification (A–E), evidence before fix

| Layer | Identity actually carried | Verdict |
| --- | --- | --- |
| prepare result | `B.IntentID`; `A` flipped SUPERSEDED in the same transaction (`internal/storage/sqlite/confirmation_store.go`) | correct |
| `confirmation_show` payload | `provider.ReadConfirmation(in.IntentID=B)` returns `B.Intent` | correct |
| `confirmation_show` result **meta** | only the constant `ui://devbridge/confirmation/v5` + `componentVersion` + `mainScriptSHA256` — **no intent identity** | **root cause** |
| presentation routing | `confirmation_show` bypasses the lease layer | no per-instance key |
| Card bound entry | `acceptEntry` first-bind is sticky (`internal/mcpserver/confirmation_ui.html:755`) | compounding |
| `sourceIntentId` | lineage field only (`contract.go`, store, schema, Card renderer) | ruled out |

- **A — Host reuses the mounted Card instance: CONFIRMED (primary).** The only key
  the Host had was the constant resource URI, so it pushed `B`'s tool-result into the
  already-mounted `A` iframe.
- **C — Card sticky first-bind: CONFIRMED (compounding)** — and it is *required*
  isolation, so it was kept and the server side was fixed instead.
- **B — adapter returns the old entry: RULED OUT.** **D — `sourceIntentId` used as
  identity: RULED OUT.** **E — premature outer copy: a guidance concern**, handled in
  §10 below, not a hardcoded server claim.

## The Host/Card instance key

`internal/mcpserver/confirmation_instance.go:25-27`:

```go
func confirmationPresentationInstanceId(intent c.Intent) string {
	return c.ValueHash([]any{intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash})
}
```

- Derived by Core from the **full frozen identity only** — no Task/Review/Decision,
  no purpose, and explicitly **no `sourceIntentId`**, so two intents that share every
  business object still get distinct instances.
- Emitted on a passing `confirmation_show` at
  `internal/mcpserver/confirmation_tools.go:533` → `decorateConfirmationInstanceMeta`
  (`confirmation_instance.go:136-145`), which adds
  `devbridge/aiSessionId|intentId|version|contentHash|presentationInstanceId`.
- The static `resourceUri` / `outputTemplate` stay constant on purpose: they are the
  shared HTML/JS cache key, never a business-instance key.
- **§2 boundary held:** these are ordinary namespaced MCP result `_meta` fields
  (declared in `docs/interfaces.md`), carrying no signature and asserting no
  unsupported capability. The field is a *render request*, not proof of mounting, and
  no guarantee depends on the Host honouring it: the pre-mount identity validator and
  the Card's own sticky bind still hold the invariant if a Host ignores it.

### Pre-mount gate and handle-reuse guard

1. `ValidateConfirmationCardInstanceIdentity` (`confirmation_instance.go:57-74`, wired
   at `confirmation_tools.go:509`) runs **before** any Card resource or meta is
   attached: incomplete frozen identity → `CONFIRMATION_NEW_CARD_INSTANCE_UNAVAILABLE`;
   intent ≠ requested identity → `CONFIRMATION_CARD_INSTANCE_IDENTITY_MISMATCH`. Both
   return an `IsError` result with **no Card resource**.
2. `confirmationInstanceRegistry.checkOrBind` (`:111-129`, wired at `:525`) binds one
   frozen identity per presentation handle (`aiSessionId\x00requestId`, `:100`). Same
   identity = idempotent refresh; a different intent on the same handle =
   `CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED`, typed and content-free
   (`confirmationInstanceFailure`, `:32-43`). The registry is process-local and makes
   **no** host-mounting claim of its own.
3. The Card refuses a foreign entry instead of rebinding
   (`confirmation_ui.html:755`): `entryDisposition='ENTRY_INSTANCE_IDENTITY_MISMATCH'`,
   note, re-render, return — keeping its own `entry`, `intent` and request identity.

## Old vs new Card, per layer (final state)

| Layer | Old (superseded `A`) | New (replacement `B`) |
| --- | --- | --- |
| durable row | `A` SUPERSEDED, byte-identical after the replacement prepare (§12 pin) | new `B` PENDING row; pair count stays 2 |
| lineage | — | `B.content.sourceIntentId = A` (data only) |
| show payload | `A.Intent` | `B.Intent` |
| `presentationInstanceId` | `H(aiSession, A, v, hashA)` | `H(aiSession, B, v, hashB)` — distinct |
| static resource | `ui://devbridge/confirmation/v5` | same URI (one shared asset, two instances) |
| Card DOM | `已被新版本替代`, only `查看审查内容 / 刷新 / 复制继续引用` | `待确认` + `重新审查` eyebrow + enabled `确认要求修订` |
| options capability | `eligibility.available=false`, no committable variant | `available=true`, own REVISE variant |
| reads issued by the instance | only `A` | only `B` |
| receipt read | none (never submitted) | names only a request the Card itself submitted |
| successor Review/Run | never created by `A` | created **only** after commit of `B` |

## `sourceIntentId` is lineage only

`TestConfirmationSourceIntentIdIsLineageOnly` passes. Every current operation —
mount/binding, `confirmation_options_get`, `confirmation_get`, receipt readback and
commit — resolves on the intent's **own** `intentId + version + contentHash`; the
replacement pair (`prior.IntentID != replacement.IntentID`, distinct `ContentHash`)
shares the same Task/Review/Decision and still yields two different instances, which is
only possible because lineage is excluded from the key.

## Business-state isolation

The replacement Card is proven to carry no inherited state: `steps = []`,
`submission = nil`, `pendingCommit = null`, `pendingHash = ""`,
`confirmedOutcome = ""`, `unknown = false`, `flight = false`
(`controlapi …:404-407`, JS `§7` test), and the old Card never gains a submission from
the replacement flow. `TestConfirmationConcurrentCardInstancesStayIsolated` runs
8 concurrent mounts plus a shared handle and an in-flight commit; it is also covered by
`-race` (see below).

## Outer copy at the prepare/show stage (§10)

Pinned against the **live advertised tool contract and server instructions**, not a
copied constant: the five `*_prepare` tools must say "Persist only an immutable pending
confirmation intent" and "Until `confirmation_commit` succeeds, report only that this
confirmation intent is prepared", and must never assert a recreated Task/Review, an
existing successor Review, a started re-review, or that a Card was shown
(`TestPrepareToolCopyForbidsPreCommitSuccessorClaims`); `confirmation_show` must state
"Each intent is its own Card instance" and the `presentationInstanceId` derivation rule
(`TestConfirmationShowCopyDescribesItsOwnCardInstance`); the server instructions carry
the same replacement rules (`TestServerInstructionsCarryTheReplacementCardCopyRules`).

## Card asset identity (G14)

| Artifact | Opening | Final |
| --- | --- | --- |
| `confirmation_ui.html` (whole file) | `76dcf7f26c0801cbcd2577a1b8f86bc663bb7c22b0b92e4dd045424912b66dc1` | `39250093c8b34ec32300b3b3a29dcffb596dd7bd0a0b7b54c24b9625b1830fba` |
| `<script id="confirmation-main">` region | `8f770ae2…` (during round) | `34a2de03209f518128aad57cdb907794fdd4350b68a5e585c568885d36a76ff2` |

The script changed once this round (an intermediate `receiptInput()` variant that let a
receipt read borrow the tool-input's `requestId` was removed again, so the final line
matches HEAD byte-for-byte and is now pinned by a test), so the shared fixture was
**re-captured** rather than hand-edited, and the four-way consistency (source digest =
served digest = advertised `devbridge/mainScriptSHA256` = fixture
`meta.mainScriptSHA256`) is asserted by
`TestConfirmationCardAssetIdentityMatchesDeployedResource` plus the JS pin
`the shared Card instance fixture is captured from this exact embedded Card script`.
`node scripts/build-chatgpt-ui.mjs` validates 2 self-contained ChatGPT UI assets.

Because the whole Confirmation body of work is uncommitted, the Card's line-level diff
against HEAD spans several rounds; this round's own Card delta is established by the
digest chain above (`76dcf7f2…` → `39250093…`), not by `git diff`. The added lines that
matter for this invariant are the cross-intent refusals
(`ENTRY_INSTANCE_IDENTITY_MISMATCH`, `SHOW_ENTRY_CHANGED`), and every removed validator
line reappears in a strictly stronger form (e.g. `editable` at `:710` now additionally
requires `!stale`).

## §14 Shared fixture, two runtimes

`internal/mcpserver/testdata/confirmation_card_instance_lifecycle_v5.json` holds the
exact production `structuredContent` + `_meta` of `confirmation_show` and
`confirmation_options_get` for a synthetic superseded/replacement pair, captured over
the real Streamable-HTTP MCP wire, the production middleware stack and a real SQLite
store (`t.TempDir()`, ids `confirm_827ff606…` / `confirm_dcb28fda…` — **no live
identifiers**). It is loaded by BOTH the Go mount-gate tests and the Card JavaScript
instance-lifecycle replay, so the JS never calls Card internals by hand: it drives
`postMessage`-style `tool-input` / `tool-result` notifications through the actual
embedded script.

## §13 Scenario gates G1–G15

| Gate | Proof | Result |
| --- | --- | --- |
| G1 dual-intent base state + Core readback | `TestConfirmationReplacementIntentMountsItsOwnCardInstance` (`controlapi:373`) | PASS 2.17s |
| G2 one resource, two instances | same test `:409-414`; JS fixture pin; `TestSharedCardInstanceFixtureKeepsTwoIndependentInstances` | PASS |
| G3 old Card = `A`, SUPERSEDED, no confirm control | `:398,421,433-436`; JS `§9` test DOM checks (`已被新版本替代`, no `待确认`, no `确认要求修订`, only `复制继续引用`) | PASS |
| G4 new Card = `B`, own PENDING instance with its own action | `:401,424-430`; JS `§4/§11`: `entryBound`, `stale=false`, `readerContextValid=true`, rendered `待确认` + `重新审查` eyebrow + **enabled** `确认要求修订`, own variant label `同一任务重新审查` | PASS |
| G5 `sourceIntentId` lineage only | `TestConfirmationSourceIntentIdIsLineageOnly` | PASS 2.21s |
| G6 business-state isolation | `:404-407`; JS `§7` (`pendingCommit/confirmedOutcome/unknown/flight` all empty) | PASS |
| G7 Host mis-reuse rejected, `A` intact, `B` not lost | `TestConfirmationShowRefusesCrossIntentPresentationHandleReuse` (`:510`) — typed `CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED`, **no Card resource, no output template**, both intents still readable | PASS 2.02s |
| G8 a correct new instance first-binds `B` | `:465-470`; `TestSharedCardInstanceFixtureIdentityGateRejectsCrossIntentMounts`; JS `§4/§11` | PASS |
| G9 same-identity refresh idempotent | `:452-463` (same instance, same frozen state, no extra confirmation) | PASS |
| G10 prepare-stage copy says only "prepared" | 3 copy-guidance tests + pre-commit SQL counts (`reviews=1`, `runs=1`, `current_review_id` unmoved, no start/re-review chain) | PASS |
| G11 commit ⇒ one request, same Task, successor ordinal 2, one Run, one accepted start authority, worker auto-start | `TestConfirmationReplacementSuccessorReviewAppearsOnlyAfterCommit` (`:633`) | PASS 2.15s |
| G12 late `A` show/options/commit cannot disturb `B` or the successor | `:714-733` (late prior stays SUPERSEDED, not committable, commit refused, successor pointer unmoved) | PASS |
| G13 lost response ⇒ exact receipt readback, no duplicate submit | `:698-712` (replay returns the original `requestId`; Reviews/Runs/chain unchanged) + JS assertion that a never-submitted Card never sends a `requestId` | PASS |
| G14 Card asset identity | `TestConfirmationCardAssetIdentityMatchesDeployedResource` PASS 0.15s; JS fixture digest pin; Card build | PASS |
| G15 trunk regression preserved | full root + desktop + JS + frontend matrices below | PASS |

`TestSharedCardInstanceFixtureEnvelopesStillMount` and
`TestSharedCardInstanceFixtureCapabilityMatchesProductBehavior` additionally pin that
the captured envelopes still mount and that capability matches product behaviour.

## Race gates (narrow, re-runnable)

`DATA RACE` occurrences across all six runs: **0**.

| Run | Command | Result |
| --- | --- | --- |
| race-b | `go test -race ./internal/controlapi/ -run 'TestConfirmation(Replacement\|Show\|SourceIntent\|Concurrent)'` | `ok 217.222s` |
| race-c | `go test -race ./internal/storage/sqlite/ -run 'Confirmation'` | `ok 36.961s` |
| race-d | `go test -race ./internal/mcpserver/ -run 'Confirmation\|Card'` | `ok 46.547s` |
| race-a (broad, first attempt) | `go test -race ./internal/confirmation/ ./internal/mcpserver/ ./internal/storage/sqlite/` | `confirmation ok 1.679s`; mcpserver 2 deadline tests FAIL; sqlite package **timed out at the 8m budget** |
| race-e (classification) | `go test -race ./internal/storage/sqlite/ -run 'TestMigrationsUpgradeV3ThroughV9ToV14'` | `ok 97.665s` — the 8m line was the legacy migration suite's runtime, not a race |
| race-f (classification) | the two mcpserver tests isolated under `-race` | FAIL, reproduced **identically at HEAD** |

`race-f-baseline.log` is decisive: a clean `git archive HEAD` extract with **zero**
working-tree changes fails the same two tests the same way. Both files are unmodified by
this round, and both wrap a live Streamable-HTTP MCP round-trip in a hard
`context.WithTimeout(…, 5*time.Second)` (`internal/mcpserver/external_contract_test.go:408`,
`internal/mcpserver/reasoning_effort_parity_test.go:23`) — deterministic `-race` slowdown against a fixed
5s budget, not a defect and not a data race. Neither failure was silenced, skipped or
substituted; the broad run was replaced by the narrow selections above, which all pass.

## Aggregates, vet, JavaScript, frontend, builds

| Gate | Command | Result | Log SHA-256 |
| --- | --- | --- | --- |
| Root aggregate (final, re-run after the Card fix) | `go test ./... -count=1 -timeout=8m` | **65 ok / 17 no-test / 0 FAIL / 0 panic**, natural END | `b27e8bb2f6fcd073…` |
| Desktop aggregate (first run) | `go test ./... -count=1 -timeout=8m` | 7 ok + 1 known probe flake | `6642298184aa8ff4…` |
| Desktop aggregate (clean, idle machine) | same command, re-run | **8/8 ok, 0 FAIL** | `71295e5631e74d18…` |
| Desktop flake isolation | `-run TestProbeAbsoluteNVMStyleLauncherFindsSiblingNode -count=5`, then package `-count=3` | both `ok` (2.383s / 1.855s) | `b02b6cb253ff52ad…` |
| Vet | `go vet ./...` root and desktop | exit 0 / exit 0, no output | `e3b0c44298fc1c14…` (empty ×2) |
| Full JS matrix | `node --test internal/mcpserver/*_test.js internal/bridgehttp/*_test.js` (26 files) | **295 tests / 291 pass / 0 fail / 4 skipped** | `a159ba40…` |
| Confirmation Card suites | `node --test internal/mcpserver/confirmation_project_ui_test.js internal/mcpserver/confirmation_card_instance_mount_test.js` | 33/33 pass, 0 fail | included above |
| Frontend | `npm test` (desktop/frontend) | 52/52 pass | `15d6ca36c6ab2fdd…` |
| Vite build | `npm run build` | exit 0 | `3f8127b6e83cf129…` |
| Card build | `node scripts/build-chatgpt-ui.mjs <dir>` | exit 0, 2 assets validated | `c5039ba61da794cd…` |
| Whitespace | `git diff --check`, `git diff --cached --check` | exit 0 / exit 0, **0 bytes of output** | `e3b0c44298fc1c14…` |

Log digests above are 16-character prefixes; full values are in `audit-index.json`.

The two JS rows were re-run last (`a159ba40…`), because the only repository edits made
after the 13:06 Go aggregate were these two JavaScript test files plus this document —
verified by timestamp scan, so no Go result above is stale.

The 4 JS skips are pre-existing env-gated opt-ins on the Decision / `dev_bridge` Card
surfaces (`!DEVBRIDGE_ORPHAN_CARD_FIXTURE`, `!DEVBRIDGE_CARD_SNAPSHOTS`); none were added
this round and none were counted as PASS. Zero-match, compile-only, helper-only and SKIP
runs were not accepted as evidence anywhere above.

### Desktop probe flake classification

`TestProbeAbsoluteNVMStyleLauncherFindsSiblingNode`
(`desktop/internal/codex/probe_test.go:46`) failed once at exactly 3.00s — the
production `DefaultProbeTimeout` — while the 8-minute root aggregate was still draining.
Fresh evidence this round: the clean idle-machine aggregate passes 8/8, `-count=5`
isolated passes, and the package at `-count=3` passes twice more. No production timeout
or test contract was modified.

## §16 Independent read-only review

Two passes were run over the final state: a first pass during implementation, and a
second independent read-only agent pass after the gates (instructed to change nothing).
Every finding was then re-verified against the code before being accepted; the
adjudication column below is part of the record.

| # | Question | Verdict | Evidence |
| --- | --- | --- | --- |
| 1 | Is the Card/Host instance key the full frozen identity? | OK, with one field gap documented | `confirmation_instance.go:26` hashes `aiSessionId+intentId+version+contentHash`; `confirmation_instance_test.go:23` and `:33` pin contentHash-only and version-only drift. The key is complete; the *pre-mount comparison* cannot be, because `c.ReadInput` (`contract.go:198-204`) carries no `contentHash` field, so `ValidateConfirmationCardInstanceIdentity` compares the three caller-supplied fields and contentHash is enforced by `checkOrBind` plus the Card's frozen `entry.contentHash` |
| 2 | Is anything still keyed on the resource URI alone? | OK | `checkOrBind` has no URI parameter (`:111-129`); the constant URI survives only as the shared asset cache key, now paired with per-intent identity meta (`confirmation_tools.go:517-533`) |
| 3 | Does any current operation use `sourceIntentId` as identity? | OK | `SourceIntentID` is consumed **only at write time**, to flip the predecessor to `SUPERSEDED` inside the same prepare transaction (`confirmation_store.go:213-226`). No read/mount/options/commit path uses it: `Get` resolves by `(ai_session_id, intent_id)` (`:89`), `ReadInput`/`identity()` exclude lineage, and `TestConfirmationSourceIntentIdIsLineageOnly` passes |
| 4 | Can two intents share Card business state? | OK | registry keyed per session+handle; the Card's per-instance closure has no shared store; `TestConfirmationConcurrentCardInstancesStayIsolated` (also under `-race`) |
| 5 | Can a newer entry overwrite the old Card's identity? | OK | `confirmation_ui.html:755` refuses with `ENTRY_INSTANCE_IDENTITY_MISMATCH`, `:756` fails closed on a changed tool-input; JS `§9` tests assert both |
| 6 | Can the new Card read the old intent's options/receipt? | OK, one gap closed | each instance reads only its bound identity (JS `§4/§11` asserts every issued call names `B`); the review flagged that the `receiptInput` *committed-intent* branch had no behavioural pin — now pinned by `a receipt read names only a request this Card submitted or its intent recorded`, with a sensitivity check that reintroducing the tool-input alias fails it |
| 7 | Does outer copy still claim a successor before commit? | OK | three copy-guidance tests over the advertised contract + live server instructions; pre-commit SQL counts (`reviews=1`, `runs=1`, pointer unmoved) |
| 8 | Is a same-identity refresh idempotent? | OK | `checkOrBind` returns `""` on identical identity (`:125-127`); `:452-463` proves same instance, same frozen state, no third intent |
| 9 | Does commit create exactly one successor? | OK | `:664-692`: 2 Reviews, successor `ordinal=2` on the same Task, 2 Runs, exactly one accepted start authority, one re-review chain row |
| 10 | Can a lost response duplicate a submit? | OK | `:698-712` replays the original `requestId` with unchanged counts; `confirmationUnknownSubmission` returns `Recorded:false` + `ReadOriginalReceipt`; no `confirmation_commit` call exists on the readback path |
| 11 | Was the Card validator relaxed? | OK for this round; one adjacent change documented | no mount/identity gate was loosened: `decode()` and `validateAccess()` are byte-identical to HEAD, `validateIntent`'s conditions map 1:1, `validateOptions` only *gained* checks, and `editable` at `:710` gained `!stale`. The review separately spotted that `applyIntent`'s `REJECTED_NOT_CONSUMED` branch differs from HEAD `:482`; that is not part of this round's goal and is examined in the next section |
| 12 | Does `OUTCOME_UNKNOWN` still forbid redispatch? | OK | `contract.go` keeps `RedispatchAllowed=false` as a non-omitempty public field; `outcome.go` refuses `consumed⇒COMMITTED`; `result_contract_test.go` proves `REJECTED⇒redispatchSameIntent=false` |
| 13 | Are the live objects untouched? | OK, with a caveat accepted | per-id enumeration: `confirm_cb2d5174…` and `confirm_9b0ddf87…` appear only as inert string constants in three tests that open no store, socket or DataRoot (`confirmation_instance_test.go:19`, `confirmation_card_entry_projection_test.go`, `confirmation_project_ui_test.js`); `task_20402fc9…` appears nowhere in the tree; `review_bfcf593a…` and `ai_session_61cd0dd6…` appear only as prose in this document. Those packages run against `StaticService`/in-memory transports or `t.TempDir()` DataRoots (`multi_repository_upper_lifecycle_test.go:66-69`); no `*_test.go` calls `os.UserHomeDir`, and the JS harness sets `fetch: undefined` with a `https://host.invalid` location. The only writer, `captureCardInstanceFixtures`, is env-gated and writes nothing on a normal run (`confirmation_card_instance_lifecycle_test.go:311-314`). The reviewer separately read the live database to test the §17 tuple; that access was **not** reproduced here (the equivalent read was blocked and is out of policy for this round) and its conclusion is **not** adopted |
| 14 | Do the root/desktop aggregates complete naturally? | OK | 65 ok / 17 no-test / 0 FAIL / 0 panic; desktop 8/8 on a clean re-run |

### Reviewer findings that were rejected on verification

- "**`internal/bridgehttp/commit_delivery_test.js` pins `_meta` with a live id.**" That
  file does not exist anywhere in the repository, and no BridgeHTTP or desktop test
  references `ui://devbridge/confirmation/v5`.
- "**A fake returns the live `B` id with `version 1`/`purpose EXECUTION`.**"
  `confirmation_card_entry_projection_test.go` contains no `EXECUTION` and no
  `Version: 2`; every fake uses `confirm_9b0ddf87…`/`version 1`/`REVIEW`/`session`
  consistently (`liveShapeReviewIntent`, lines 22-40).
- "**`checkOrBind` runs after the Card meta is attached, so a rejection can leak a Card
  resource.**" `resultMeta` is only a local until the success return; both rejection
  paths return a *different* result object with no `Meta`, and
  `TestConfirmationShowRefusesCrossIntentPresentationHandleReuse` asserts the rejected
  result carries neither the Card resource URI nor the output template. The related
  suggestion — key the registry on `presentationInstanceId` — would make the guard
  unable to reject anything, since distinct intents always yield distinct instance ids;
  the handle is the caller-declared instance address and is therefore the only sound key.

### Verified findings that are real but outside this round's scope

These survived re-verification against the code. None of them is a Card-instance-identity
defect, and none of them is fixed here, because §2 fixes this round's scope to the one
live-proven problem and forbids widening it. They are recorded so they are not lost.

- **`applyIntent` no longer clears `pendingCommit` after a durable rejection.** HEAD
  `confirmation_ui.html:482` reads
  `…&&pendingCommit&&next.state==='PENDING'&&!next.commit{…if(nextAction==='EDIT_SELECTION'){pendingCommit=null;pendingHash='';unknown=false;…}}`;
  the working tree drops the `pendingCommit&&` precondition, always re-populates
  `pendingCommit=originalSubmission(submission.request)`, and contains no
  `pendingCommit=null` anywhere. Consequence: after `REJECTED_NOT_CONSUMED` the
  `commit()` guard (`!pendingCommit`) and `editable` stay closed, so the Card cannot offer
  the in-place edit-and-retry that the `confirmation_commit` contract text still advertises
  (`confirmation_tools.go:463`: "`REJECTED_NOT_CONSUMED` plus current pending intent and
  `EDIT_SELECTION` permits a new user-selected attempt"). No JS test covers that transition. This round's goal
  (a new intent getting its own Card instance) does not exercise a rejected submission, so
  G1–G15 are unaffected; `confirmation_ui.html` is a file this round did modify, so the
  difference cannot be attributed to a specific earlier round at line level and is reported
  as unresolved rather than assumed inherited. **Needs its own bounded round.**
- **The pre-mount gate sees three of the four identity fields** because `c.ReadInput` has
  no `contentHash`; closing that gap means changing the public `confirmation_show` input
  schema and re-capturing the shared contract vectors — out of scope here.
- **Replacement intents always carry `Version: 1`** (`confirmation_store.go:228`), so
  `version` adds no discriminating power between a predecessor and its replacement. The
  instance key still separates them because `intentId` is derived from
  `(aiSessionId, prepareRequestId)`; what a Host cannot do from identity alone is *order*
  the chain, which is exactly what `sourceIntentId` lineage is for (§5).
- **A handle-bearing `confirmation_show` doubles as a receipt read.** `in` (including
  `requestId`) goes into `ReadConfirmation`, so a never-committed request comes back as
  `submission{outcome:UNKNOWN, recorded:false}` (`confirmation.go:197-205`). That is the
  intended `READ_ORIGINAL_RECEIPT` shape: `recorded:false` short-circuits
  `validateSubmission` before any conflict check and leaves `unknown` untouched, and
  `pendingRequestId` stays `""` because the submission's `requestId` never becomes
  `pendingCommit` — which is the assertion the new receipt test pins.
- **The shared fixture is coupled to the Card script digest**
  (`mainScriptSHA256`, `confirmation_card_instance_fixture_test.go:143`), so every Card
  edit needs an env-gated re-capture that writes files. Deliberate: a stale digest pin is
  the failure mode this round had to rule out.

### Accepted limitations (documented, not silently fixed)

1. **Process-local reuse guard.** `confirmationInstanceRegistry` is one per `Server`
   (created at `confirmation_tools.go:314`), grows one small record per
   (session, handle) and is never evicted. It therefore cannot police reuse across
   server processes or after a restart — which is why the guarantees that must always
   hold are (a) the pre-mount identity validator, (b) the Card's own sticky bind, and
   (c) the `presentationInstanceId` the Host is asked to honour. A cache cap was
   deliberately **not** added: evicting a binding would silently re-open exactly the
   reuse hole the map exists to close.
2. **Handle-less shows are unguarded by design** (`confirmation_tools.go:521-524`); the
   server cannot observe Host iframe state, so it emits identity meta and lets the Card
   refuse. `G7` proves the guarded path.
3. **The server-side half is closed; the live symptom needs the Host to act on
   `devbridge/presentationInstanceId`.** Nothing in this repository consumes that key — it
   is an emission for the Host, and the only non-standard-but-honoured keys today are
   `ui.resourceUri` and `openai/outputTemplate`, which stay constant by design (§5). If
   ChatGPT keys widget instances on those alone, intent `B` still arrives at `A`'s mounted
   widget; what changes this round is that the Card now refuses the mismatched entry
   cleanly (`ENTRY_INSTANCE_IDENTITY_MISMATCH`, `:755`) instead of going fatal, and the
   outer copy never claims the Card was shown. So the *server contract* is closed and
   verified against a simulated instance-aware Host plus the real MCP/SDK/Card stack, but
   **the user-visible symptom can only be confirmed (or corrected) by §17 manual
   acceptance on the real Host**, which is why `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`
   stays open and no real-host claim is made here.

## §17 Post-deployment read-only runbook (live objects)

This round never accessed or modified the live DataRoot or the live diagnostic objects.
After deployment, re-run **only** the read-only call:

```
confirmation_show(intentId = confirm_9b0ddf87d658146faedc28cea9fc8b2f, version = 1,
                  purpose = REVIEW, aiSessionId = ai_session_61cd0dd6d5389edc4b38e3bd9c4b305b)
```

Expect a **new** Card instance for that intent (`devbridge/presentationInstanceId` ≠
the one derived for `confirm_cb2d5174e6e96b3573f8addbff6172bc`). Before clicking
anything, check technical diagnostics first:

1. `identity.intentId = confirm_9b0ddf87d658146faedc28cea9fc8b2f`
2. `entryBound = true`, `staleIntent = false`, `readerContextValid = true`
3. `pendingRequestId = ""` (nothing submitted)
4. then the action control (`确认要求修订`, titled `重新审查`) is offered and enabled

Confirm from the store that **before the user clicks** no request row exists, no
successor Review exists, and `tasks.current_review_id` still points at
`review_bfcf593ae6fb34725276a5793893496b`. Verify the successor chain only **after** the
user's own click. Note the honest wording limit: `同一任务重新审查` is caller-supplied
variant copy that V5 surfaces in the variant list/technical diagnostics; with a single
prepared variant the Card renders its own built-in action copy (`重新审查` /
`确认要求修订`), which is what "offers its own confirmation action" means in practice.

## Safety confirmations

- No stage, commit, merge, push, reset, clean, checkout or rebase. Index stayed empty;
  HEAD stayed `3f3309d`; the branch did not move. The temporary `git archive HEAD`
  extract for the race baseline was written to `/private/tmp` only and read nothing back.
- Live SQLite, the user DataRoot and every live object named in §1 were untouched; the
  live ids appear **only** as inert constants in three test files
  (`internal/mcpserver/confirmation_card_entry_projection_test.go`,
  `internal/mcpserver/confirmation_instance_test.go`,
  `internal/mcpserver/confirmation_project_ui_test.js`), none of which opens a store or
  a socket. Every storage test in scope uses `t.TempDir()`.
- No automatic commit of the new intent, no third Confirmation, no successor Review, no
  duplicate Task, no real model, no paid service, no Desktop UI/device/simulator run and
  no screenshot-based acceptance.
- The Card validator was **not** relaxed. The sticky first-bind, `SHOW_ENTRY_CHANGED`
  and the new `ENTRY_INSTANCE_IDENTITY_MISMATCH` refusals stay in force, `receiptInput`
  matches HEAD exactly, and the one validator line the working tree changes relative to
  HEAD tightens rather than loosens (`editable` additionally requires `!stale`).
- `OUTCOME_UNKNOWN` still means "answer recorded, never re-dispatch": an unrecorded
  request id reads back as `{Outcome: Unknown, Recorded: false,
  NextAction: ReadOriginalReceipt}` (`internal/storage/sqlite/confirmation_outcome_store.go`)
  and the Card keeps the original request identity instead of resubmitting.

## Closure

Publish condition, checked against the logs in
`/private/tmp/devbridge-confirmation-new-intent-card-instance-identity-audit/logs`
before writing this block: FINAL root aggregate exit 0 with `ok=65 fail=0 panic=0`; FINAL
desktop aggregate clean rerun `ok=8 fail=0` (the earlier `ok=7 fail=4` run is classified as
environment flake in `desktop-codex-flake-classification.log` /
`final-desktop-flake-isolated.log`); `go vet` exit 0 for root and desktop; JS matrix exit 0
with `tests 295 / pass 291 / fail 0 / skipped 4`; Card instance + Card suites exit 0
(33/33); frontend `pass 52 / fail 0`; Vite build exit 0; Card build exit 0; `git diff
--check` and `git diff --cached --check` both exit 0 and emit 0 bytes; every narrow `-race`
run reports 0 `DATA RACE`; the per-gate Go run reports `mcp_exit=0 pass=8 fail=0` and
`ctl_exit=0 pass=5 fail=0`.

Closed for this round's live-proven defect only:

```text
CONFIRMATION_NEW_INTENT_CARD_INSTANCE_IDENTITY_CLOSED
CONFIRMATION_SOURCE_INTENT_LINEAGE_ONLY_CLOSED
CONFIRMATION_CROSS_INTENT_CARD_STATE_ISOLATION_CLOSED
CONFIRMATION_REPLACEMENT_INTENT_CARD_PRESENTATION_CLOSED
CONFIRMATION_NEW_INTENT_CARD_INSTANCE_BINDING_CLOSED
```

Still open, and not closable from this repository state:

```text
PARTIAL_IMPLEMENTATION
REAL_CODING_AGENT_E2E_NOT_RUN
MANUAL_DESKTOP_ACCEPTANCE_PENDING
MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING
CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED
```

Explicitly NOT claimed (each is contradicted by the evidence above, and publishing any of
them would misreport the outcome):

```text
OLD_CARD_REBOUND_TO_NEW_INTENT        # G3: the old Card keeps identity A, staleIntent=true
SOURCE_INTENT_USED_AS_CURRENT_IDENTITY # G5: sourceIntentId is lineage-only, never in the key
DUPLICATE_CONFIRMATION_CREATED         # §2/§17: exactly the pre-existing A and B; no C
SUCCESSOR_REVIEW_CREATED_BEFORE_COMMIT # G11: reviews 1 -> 2 only across the commit boundary
LIVE_CONFIRMATION_MUTATED              # §17: live objects are read-only constants in tests
MANUAL_CHATGPT_CARD_ACCEPTED           # manual acceptance is the user's, not automated
GLOBAL_FINAL_ACCEPTANCE_PASSED         # this is one bounded defect closure, not final acceptance
```
