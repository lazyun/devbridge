# Project-centric multi-repository execution report

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

Current user-decision increment: **PARTIAL_IMPLEMENTATION**, not
`CODEX_REQUEST_USER_DECISION_CLOSED`. Live request/answer/cancel and successor
fixtures pass; the pinned protocol cannot restore an old connection's pending
request after Core restart. See the [increment execution report](codex-request-user-decision-execution-report.md).
Earlier Coding v1 closure and the historical evidence below remain unchanged.

## Current policy: Trusted Executor Model v1

The Coding runtime uses **TRUSTED_EXECUTOR_MODEL_V1**. The offline sandbox
investigation below remains an accurate historical diagnostic record, but its
current product status is **CODEX_SANDBOX_HARDENING_DEFERRED**. Its 72-case
matrix, source candidates, and independent-terminal evidence are not Coding v1
admission conditions and do not claim a hostile OS security boundary.

Coding admission still requires an exact durable RuntimeWorkset, an explicit
primary member for multi-repository work, managed-worktree provenance, the
pinned Client start/resume/turn wire, per-member terminal observations,
Verification, Acceptance, and delivery fencing. Missing Client capability,
incomplete historical runtime layout, backend/config mismatch, and invalid
member evidence remain production blockers. Historical
`MULTI_REPOSITORY_CODING_RUNTIME_BLOCKED` statements below describe the state at
the time of those increments; they do not make sandbox hardening a current gate.

## Current increment: Coding Execution v1 closed

Schema v77 now persists the single authoritative RuntimeWorkset on each Run and
extends `run_repository_bindings` to an exclusive Review-or-Execution owner.
Explicit `primaryMemberId` selects cwd only. Production preparation materializes
the exact approved members, reuses their managed worktrees, records start
HEAD/snapshot/root identity, and binds the RuntimeWorkset before backend handoff.
The existing Client sends exact roots and writable roots on thread start, resume,
and turn; Verification and Coding Review use a read-only runtime binding. End
observation records each member as OBSERVED, NO_CHANGE, SCOPE_CONFLICT, or UNKNOWN.
Acceptance requires successful Implementation and Verification Runs with the
same workset hash and complete terminal observations.

The production-facade fixture now passes the complete Upper Review → v2
NO_START → explicit Coding start → dual-member Implementation → required
Verification → Acceptance → v76 Task commit → separately confirmed FeatureArea
merge path. The production Client fake App Server receives exact cwd/roots,
model/effort, sandbox policy, structured schemas, and unmodified prompt bytes.
It edits both managed worktrees in one Implementation, executes app, sdk, and
cross-repository checks in Verification, and excludes the registered backend
repository. A response lost after the second member's Git commit is recovered
after closing and reopening the control plane; each member has exactly one Task
commit and is merged to its frozen development ref without a Direct File result.

CODING_AGENT Review uses the same adapter with two frozen read-only members and
an explicit primary member. Its production structured v2 APPROVAL_READY result
enters the existing latest-only Review approval writer and remains NO_START.
A second production fixture mutates a Review member and is rejected as
SCOPE_CONFLICT without an approval. Existing FURTHER/findings-only, stale Review/
Decision, and Upper Review regressions pass. Observer tests cover legal changes,
NO_CHANGE, read-only mutation, PATHS escape, changed HEAD, and root substitution.

The v77 acceptance path projects authoritative Implementation and Verification
Run observations into the existing v76 member delivery ledger. It records Coding
run evidence directly and never fabricates a Direct File result, mutation receipt,
or file journal. Current release status is
**MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED** under
**TRUSTED_EXECUTOR_MODEL_V1**. `MULTI_REPOSITORY_CODING_RUNTIME_BLOCKED` is no
longer a current status. Repository-wide failures that reproduce against the
starting byte snapshot remain baseline defects and are not counted as this
increment's acceptance.

## Current increment: fixed platform-defaults policy causal verification

**CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED** remains in effect, with a precise
source-level finding: fixed Codex `:minimal` appends tmp READ/WRITE grants outside
the dynamic allows narrowed by path DENY. The installed native is byte-identical
to the official 0.149.0 Darwin arm64 release asset. Complete installed SBPL has
**not** been observed; the new A probe was startup-blocked (rc71, 0 executed
operations), so B/A′ were not run. No host permission escalation or retry occurred.

The [current diagnosis](codex-offline-isolation-diagnosis.md) contains the full
input/permission/rule mapping, complete source segment audit, raw evidence,
observation patch, smallest tmp-grant removal candidate, and new-factor manual
continuation. Source patches are prepared and application-checked but **unbuilt**:
there is no existing Rust toolchain. No installed binary was changed. Candidate
status is **SOURCE_CANDIDATE_UNBUILT**, and candidate 72-case matrix is **NOT_RUN**.
Original 72-case result remains **46 matched / 26 unexpected successes**.

Independent-terminal raw report hash was reverified: **10 actual reads, 6 matches,
4 unexpected successes**. User-reported origin is distinguished from automated
launch-origin proof; old raw EVIDENCE_PENDING remains untouched. Named tar was not
reacquired, so its stated hash was not reverified. No old experiment was rerun.

Start branch `dev_20260908_1913`, HEAD
`fd8a6afeb213972e8aed00ff23f090ae058b8530`, schema v76; **191 unstaged dirty files**,
empty index; 1,754 starting byte copies at
`/private/tmp/devbridge-platform-baseline-s6h091e5`.
Final: **202 dirty files**, all unstaged, empty index; branch/HEAD/schema unchanged.
**15 files changed against starting bytes: 4 existing and 11 new**, no removals.
[Final byte increment/identity](evidence/codex-platform-defaults/increment.json)
compares that baseline, not historical HEAD. Eleven Python controller regressions
pass; patches pass application dry-run; `git diff --check` passes. These do not
constitute a runtime sandbox pass. No Go or schema change means no Go test/vet/
compile rerun. Seven repaired execution fixtures remain inherited PASS, and two
other old failures remain separately inherited and untouched.

Production stays **MULTI_REPOSITORY_CODING_RUNTIME_BLOCKED**. Retained statuses:
**PARTIAL_IMPLEMENTATION**, **REAL_CODING_AGENT_E2E_NOT_RUN**,
**SAFE_REPOSITORY_UNLINK_CLOSED**, **MANUAL_DESKTOP_ACCEPTANCE_PENDING**,
**MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING**. Upper Review/v2 Approval/Direct File/
delivery changes are preserved. No Client binding/support flag, live data or
config mutation, real Thread/Turn, UI operation, staging, commit, merge or push.

## Previous increment: offline isolation causal diagnosis and legacy fixture repair


**CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED** and
**LEGACY_EXECUTION_FIXTURES_REPAIRED** are separate outcomes.
The corrected offline matrix still has **26 failures out of 72**. All **seven**
specified execution tests now run their assertions and pass using fake backends.
Neither result enables multi-repository Coding or establishes real-model E2E.

### Baseline, bounded changes and accounting

- Branch `dev_20260908_1913`; HEAD `fd8a6afeb213972e8aed00ff23f090ae058b8530`.
- Start: **184 dirty files**, all unstaged; index empty; schema **v76**.
- New starting snapshot: `/private/tmp/devbridge-isolation-baseline-xpbzc920/`,
  1,750 file byte copies plus SHA-256 manifest and Git identity.
- No production Go source or migration changed in this increment. Existing Upper
  Review, v2 Approval, Direct File, delivery and Coding admission guards are retained.
- Changes: `scripts/probe_codex_workset.py` and its new Python regression test;
  five execution test files (`coordinator_test.go`, `conversation_runtime_test.go`,
  `runtime_supervisor_test.go`, `work_verification_plan_test.go`,
  `runtime_supervisor_sqlite_integration_test.go`); three existing boundary documents;
  the new diagnosis document and two portable evidence bundles.
- Final manifest: `/private/tmp/devbridge-isolation-final-increment.json`.
  **13 files changed relative to starting bytes**: 6 already-dirty files,
  3 previously clean test files, and 4 new files. Final **191 dirty files**,
  all unstaged; index empty; no removals. Branch/HEAD/schema remain unchanged.

No official repository stage/commit/merge/push/reset/checkout/clean/rebase;
no live SQLite/DataRoot/runtime, credentials/global config, binary upgrade, real
model, actual app-server Thread/Turn, UI/device/screenshot or migration operation.
The bounded official offline helper required approved sandbox escalation after
nested Seatbelt startup failed. It retained the same denial profile and only used
new test-owned directories. This is not an independent terminal context.

### Isolation: exact outcome and remaining causal question

The [diagnosis report](codex-offline-isolation-diagnosis.md) contains every original
failure group, the 72-ID correspondence, configuration/path/command evidence,
probe defects repaired, representative controls and the independent-terminal
command. Its [raw bundle](evidence/codex-offline-isolation-v2.json) preserves actual
original and final report/config/payload bytes with hashes.

| Layout / policy | Matched / total | Failed reads | Failed writes |
| --- | ---: | ---: | ---: |
| workspace-local / both-write | 11 / 13 | 1 | 1 |
| workspace-local / mixed-paths | 11 / 13 | 1 | 1 |
| workspace-local / read-only | 11 / 13 | 1 | 1 |
| private-tmp / both-write | 6 / 11 | 2 | 3 |
| private-tmp / mixed-paths | 4 / 11 | 2 | 5 |
| private-tmp / read-only | 3 / 11 | 2 | 6 |
| **Total** | **46 / 72** | **9** | **17** |

All 26 are now confirmed actual access successes against DENY expectations, with
unique read bytes or external write effects. No final matrix case is a missing
fixture, startup error, command-not-executed or parsing error. Positive controls
succeed in every group. Six Python regressions reject the old generic
nonzero-means-denied interpretation and mismatched content/effects.

The minimal read reproduction holds config/profile/payload/cwd and supplied child
environment fixed. Repeating access to the excluded `/private/tmp` object succeeds;
using its `/tmp` alias (same inode) succeeds; changing only TMPDIR still succeeds;
addressing an excluded normal-directory object with identical bytes is denied.
The effect is narrowed to target location/object class in this launch context,
not established as a single cause for all reads/writes. Helper policy generation,
other configuration/default semantics and inherited host permissions remain
unseparated. Exact native-binary/source correspondence is unverified.

**EVIDENCE_PENDING**: run the supplied self-contained minimal controller in an
independently opened terminal, after its documented `--dry-run`, and return its
report. No independent context was simulated by repeated agent launches, and no
additional matrix or alternate sandbox/backend was introduced. The probe fixes
improve evidence validity; they did not remove the 26 isolation failures.

### Execution fixture changes and restored assertions

| Exact top-level test | Final result and asserted behavior |
| --- | --- |
| `TestCoordinatorBindsAuthorizationCeilingByOperation` | PASS: implementation WORKSPACE_WRITE; Review READ_ONLY through backend dispatch |
| `TestReservedExecutionDispatchUsesFrozenRuntimeConfigAndPrompt` | PASS: all four frozen model/effort combinations, service tier/ceiling and exact authored prompt bytes/hash |
| `TestAuthorizedPlanSerializesImplementationAndVerificationWithContinuity` | PASS: implementation precedes verification; distinct Runs/Turns, same Workspace/Thread, exact phase prompts, 10/20-token observations |
| `TestImplementationFailurePreventsVerification` | PASS: failed implementation produces failed plan and no verification reservation |
| `TestVerificationFailureFailsPlanWithoutRepair` | PASS: preserves failed-command evidence/no automatic repair; new runtime-failure subcase truly fails the plan and verification Run, with no third Run |
| `TestVerificationMutationFailsPlan` | PASS: readonly verification mutation rejected and plan failed |
| `TestVerificationReportMutationPreservesSQLiteTaskAndReleasesClaim` | PASS: Task remains ACTIVE, execution fails, implementation succeeds, verification mutation is INFRASTRUCTURE_FAILED, claim released, no acceptance receipt |

The shared memory plan fixture explicitly freezes the Task definition revision,
backend selection/fingerprint and runtime configuration before binding phase Runs.
Handoff records must be registered before claim and are bound to Project/Run;
claim does not invent a receipt. The conversation fake now persists backend source
Run/Thread provenance and attaches the exact Thread to the Run store, so the real
coordinator's atomic readback and continuation checks execute. Timeouts bound
backend creation, prompt delivery and completion. These unit doubles are limited
protocol fixtures, not durable-production or isolation evidence.

The SQLite fixture initializes Session/caller scope and binds the Project through
current APIs. A read-only fake backend supplies the prerequisite Review response;
**the real Coordinator** prepares/executes/finalizes that Review and attaches its
result. The fixture asserts READ_ONLY, exact report and one turn before using
formal APPROVE/START_AUTHORIZED and reservation APIs. It then uses the real SQLite
handoff store and RuntimeConfigProvider for the measured implementation/verification
path. No direct terminal-success seed remains in this prerequisite helper.

One inherited name needed clarification: failed required-command observations are
report evidence, whereas transport/report completion advances toward acceptance;
it does not accept the Task (existing task-execution contract). Its original
SUCCEEDED/report-failed assertions were preserved. A separate runtime
TaskExecutionError subcase now asserts FAILED/EXECUTION_FAILED, verification Run
FAILED and no repair. Production semantics were not changed to conflate these layers.

The frozen-config test directly measures Coordinator dispatch after an immutable
consumed reservation; supervisor handoff is covered by the plan and SQLite tests.
It is not reported as a standalone end-to-end reservation/handoff test.

### Actual validation and retained failures

| Gate | Final observed result |
| --- | --- |
| All seven exact execution tests, one anchored regex | **7 top-level PASS**, no SKIP / no FAIL |
| Related shared memory fixture, Review and admission consumers | **13 top-level PASS** |
| Shared SQLite panic/finalization/restart consumers | **2 top-level PASS** |
| Upper Review / Direct File / v1 facade / Coding rejection | **36 top-level PASS** |
| SQLite v75/v76 / delivery / history | **17 top-level PASS** |
| Python probe result interpretation | **6 PASS** |
| Root + Desktop compilation | Both PASS; compilation only |
| Targeted vet + git diff --check | PASS |
| Actual offline matrix | **FAIL: 26 / 72**, six groups retained |
| Independent terminal / real model E2E | EVIDENCE_PENDING / NOT_RUN |

Portable logs and file identities:
[execution verification bundle](evidence/execution-fixture-repair-results.json).
Primary final logs are `/private/tmp/devbridge-isolation-seven-final.jsonl`,
`...-consumers-final.jsonl`, `...-sqlite-fixture-consumers.jsonl`,
`...-upper-direct.jsonl`, `...-delivery.jsonl`, `...-probe-tests.log`,
`...-root-compile-final.log`, `...-desktop-compile-final.log`, `...-vet-final.log`.
Initial seven failures remain in `...-legacy-execution-initial.log`.

Commands use isolated `GOCACHE=/private/tmp/devbridge-isolation-gocache` and
new test-owned DataRoots. Exact seven-test command:

```sh
go test -json ./internal/execution -run '^(TestCoordinatorBindsAuthorizationCeilingByOperation|TestReservedExecutionDispatchUsesFrozenRuntimeConfigAndPrompt|TestAuthorizedPlanSerializesImplementationAndVerificationWithContinuity|TestImplementationFailurePreventsVerification|TestVerificationFailureFailsPlanWithoutRepair|TestVerificationMutationFailsPlan|TestVerificationReportMutationPreservesSQLiteTaskAndReleasesClaim)$' -count=1 -timeout=45s
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_probe_codex_workset.py' -v
```

A broader intermediate consumer run exposed incomplete new fixture wiring; that
was repaired before final acceptance. It also encountered two out-of-scope old
FeatureArea direct fixtures: `TestFeatureAreaRunUsesConversationThreadAndDurableBoundary`
and `TestFeatureAreaConversationFailureBoundaries`. Both were independently rerun
against the **new starting byte snapshot** and current files; both fail at missing
frozen backend selection before their intended backend assertions. Raw logs are
`...-two-baseline.jsonl` and `...-two-current.jsonl`. They remain inherited failures;
no full execution-package PASS is claimed. The original seven all pass separately.
No zero-match, SKIP or compile-only command is counted as runtime acceptance.
An additional reviewer-started full execution-package run was interrupted and is
**NOT_ACCEPTANCE**; it supplies no full-suite success claim. No captured output
from that interrupted attempt is used as evidence.

### Retained boundaries and next implementation

**PARTIAL_IMPLEMENTATION**, **MULTI_REPOSITORY_CODING_RUNTIME_BLOCKED**,
**REAL_CODING_AGENT_E2E_NOT_RUN**, **SAFE_REPOSITORY_UNLINK_CLOSED**,
**MANUAL_DESKTOP_ACCEPTANCE_PENDING**, **MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING**.

The experimental planner is not wired to Client; evidence identity is not durable
Execution runtime binding. Same-Thread permission narrowing and every file/patch/
external channel remain unverified. The confirmation pre-reservation rejection and
coordinator pre-dispatch checks remain intact. Dual-member Coding output, required
Verification.RUN, Coding Review, acceptance-to-delivery and recovery still require
implementation after complete host/channel isolation evidence. No production
support flag, waiver, mode substitution, single-repo fallback or fake Run was added.


## Previous increment: Coding runtime capability investigation and guarded protocol preparation

Requested closure **MULTI_REPOSITORY_CODING_PROTOCOL_AND_VERIFICATION_CLOSED is NOT achieved**.
Project status remains **PARTIAL_IMPLEMENTATION**. Actual multi-repository Coding
Implementation / required Verification.RUN / Coding Review / acceptance-to-delivery
remain **BLOCKED**. No positive Coding production chain is claimed. The prior Direct
File, Upper Review and resource/Workspace completions remain intact below.

### Baseline and retained state

- Start/end branch: `dev_20260908_1913`.
- Start/end HEAD: `fd8a6afeb213972e8aed00ff23f090ae058b8530`.
- Start: **172 dirty files**, all unstaged; index empty; schema **v76**.
- Initial byte manifest and copies: `/private/tmp/devbridge-coding-v2-baseline-92n68nhh/`
  (1,738 files). This increment is compared to that snapshot, never reconstructed
  from the unchanged historical HEAD.
- Schema stays **v76**; no migration or live database change was necessary for
  these evidence/diagnostic modules. No historical definition, receipt or migration
  was rewritten. No stage, official-repository commit/merge/push/reset/checkout/
  clean/rebase, live runtime operation, credentials/global configuration change,
  real Coding session/model, paid service, Desktop UI, device or screenshot.

### G0: actual binary, wire and isolation evidence

The default resolver candidate really reports `codex-cli 0.149.0` on Darwin
25.6.0 arm64. Its experimental generated schemas contain named `permissions` and
`runtimeWorkspaceRoots` on start/resume/turn. Therefore the old blanket explanation
that the binary has no possible exact-read profile path was incomplete. However,
the existing production client uses the legacy policy subset and has no validated
end-to-end mapping, immutable profile-content binding or all-channel narrowing proof.

The official offline helper was tested with isolated environment/configuration and
three independent test-owned Git repositories. It successfully denied disallowed
normal-directory member/path/metadata access. It **failed** to deny an excluded
`/private/tmp` repository even with absolute and special tmp deny rules. Full probe:
**72 expectations, 46 matched, 26 failed**; all six full policy/layout suites
correctly return nonzero. The root cause may involve the helper, platform or inherited
host extensions and is not established. No failure is reclassified as support.

The inspected launcher/native paths and hashes, stable/experimental schema manifest
hashes, per-policy results, actual argv/config digests, source-version uncertainty,
production caller map and official evolution references are in
[coding-runtime-capability-evidence.md](coding-runtime-capability-evidence.md).
Default resolver inspection is not a claim to know an uninspected live database's
configured backend. There is no official capability negotiation record to invent.

### Concrete implementation completed

- Replace obsolete one-line binary claims with a static, machine-inspectable
  production admission matrix. The stable `MULTI_REPOSITORY_RUNTIME_UNSUPPORTED`
  code and errors.Is contract remain. Exact Coding confirmation options reject
  before Execution/claim/handoff reservation; coordinator checks remain before
  backend dispatch and validate the frozen target shape.
- Add bounded experimental permission DTOs and a pure frozen-workset plan for
  start/resume/turn, using actual generated schema evidence and mutually exclusive
  named/legacy permission paths. This module is **not connected to Client runtime**,
  does not install profiles, and does not prove root identity or permission effects.
- Add an evidence-only identity format binding configuration fingerprint and bytes,
  resolved launcher/native binaries and digests, version, schema, platform, exact
  profile content, and source-tagged independent evidence dimensions. Frozen digest
  checks reject drift. Checksums and caller-supplied PROVEN facts do not authorize
  production, and this is not yet a durable Execution runtime binding.
- Add `scripts/probe_codex_workset.py`, which reproducibly generates installed
  schemas and runs the official offline helper against disposable test fixtures.
  It launches no app-server session/model and fails when a denial expectation fails.
- Update canonical documents to distinguish protocol preparation, actual offline
  effects and real-model testing. No fake supported flag, mutation journal, Run,
  Thread, waiver, Verification PASS or v76 Coding delivery receipt was introduced.

### Gate results and evidence levels

| Gate | Actual result |
| --- | --- |
| G0 | Investigation and schema extraction complete; complete runtime capability gate FAILED/UNPROVEN. Admission remains blocked with precise production gaps. |
| G1 | BLOCKED: no admitted single Implementation Run with dual-member Coding modification. Existing Direct File dual-write success is not Coding evidence. |
| G2 | Partial offline normal-directory checks pass; temporary-directory denials FAIL. Built-in patch/file tools, external channels and runtime permission escalation remain UNVERIFIED. |
| G3 | NOT_RUN: separate helper invocations do not test canonical Thread permission narrowing or revocation of previous session grants. |
| G4 | BLOCKED: required multi-repository RUN Verification was not executed. No waiver substitutes for it; pure report tests do not establish this gate. |
| G5 | BLOCKED: multi-member Coding Review not dispatched. Upper Review regressions pass independently. |
| G6 | BLOCKED for Coding-owned result/acceptance to v76 delivery. Existing Direct File delivery and Git recovery regressions pass. No fabricated Direct File receipts. |
| G7 | No positive Coding fault/restart chain admitted. Negative pre-reservation/handoff fences tested; all requested Coding response-loss windows remain pending. |
| G8 | Prior Upper/Direct File/delivery and v75/v76 history regressions pass. Relevant legacy execution fixture failures are retained and reproduced against the starting snapshot; no complete v1 Coding regression pass claimed. |

Evidence **A** (Core/local fake protocol): new schema/serializer/identity/admission
checks are exercised; full multi-repository Coding/Verification production loop is
not implemented or passed. Evidence **B** (actual binary offline sandbox): observed
mixed results above, full isolation FAILED. Evidence **C** (actual model E2E):
**REAL_CODING_AGENT_E2E_NOT_RUN**, as required by the user's boundary.

### Verification and failures

| Test group | Actual matched result |
| --- | --- |
| Codex wire/profile/runtime-evidence/launcher + Git packages | 136 top-level PASS; 1 real-model opt-in SKIP; 0 failed |
| Execution admission, recovery fence and bounded report checks | 10 top-level PASS; 0 failed |
| Formal Coding rejection + prior Upper/Direct File/v1 facade chain | 36 top-level PASS; 0 failed (78.413s) |
| SQLite v75/v76 migration/history/delivery regressions | 17 top-level PASS; 0 failed |
| Related frontend contracts | 16 PASS; 0 failed/skipped |
| Root and Desktop compilation | Both PASS; compilation only |
| Targeted vet and git diff --check | PASS |
| Actual offline isolation helper | FAIL: 26 of 72 expectations failed; not replaced by fake-backend tests |
| Additional inherited execution fixtures | 7 top-level FAIL, individually reproduced on the starting snapshot |

Commands (Go cache `/private/tmp/devbridge-direct-gocache`, reused only as an isolated
build cache; tests create their own DataRoots/repositories):

```sh
DEVBRIDGE_REAL_CODEX= go test -json ./internal/codex ./internal/codexruntime ./internal/codexlaunch ./internal/git -count=1 -timeout=90s
go test -json ./internal/execution -run '^Test(Exact.*|RecoveredPreparedRun.*|VerificationReportSeparatesObservedAndReportedFacts|VerificationReportMissingCommandIsEvidenceNotReportFailure|VerificationReportRejectsProseAndShapeMismatch|VerificationCustomUsesOnlyStructuredReportedFact|VerificationObservedEvidenceIsBounded|CoordinatorSuccessfulProgressionAndBoundary|ReservedExecutionRequiresConsumedExactOwnerBeforeDispatch)$' -count=1 -timeout=90s
go test -json ./internal/controlapi -run '^Test(MultiRepositoryDirect|MultiRepositoryUpper|UpperV1Facade|MultiRepositoryCoding)' -count=1 -timeout=120s
go test -json ./internal/storage/sqlite -run 'Test(V7[56]|MigrationLifecycle|CanonicalDeliveryPaths|CommitPresentation|InventoryCommit|TaskAutoCommit|NewFeatureAreaCommit|TaskCommitHistory|ReviewOnlyDoesNotChangeCommit|TwoFeatureAreasSerialize|RepositoryCommandLock|LegacyDeliveryUsesExactTaskMember|AggregateDeliveryCannotReport|DeliveryBlocksFailed|WorkspaceMember)' -count=1 -timeout=90s
go test ./... -run '^$' -timeout=90s
# Also in desktop/: go test ./... -run '^$' -timeout=90s
go vet ./internal/codex ./internal/codex/wire ./internal/codexruntime ./internal/codexlaunch ./internal/execution ./internal/controlapi
node --test internal/bridgehttp/product_git_commit_test.js internal/mcpserver/confirmation_project_ui_test.js desktop/frontend/test/project-registration.test.js
git diff --check
```

Final JSON logs are `/private/tmp/devbridge-coding-{modules-final,execution-final,production-final,sqlite-regression}.jsonl`.
Compilation/vet logs are `/private/tmp/devbridge-coding-{root-compile,desktop-compile,vet}-final.log`;
frontend log is `/private/tmp/devbridge-coding-frontend.log`.
Read-only cross-review of the integrated A/B changes and offline runner/docs passed
with no remaining finding after the repairs below. Root reviewed the separately
owned evidence-identity module. Review acceptance is limited to these G0 modules;
it is not full Coding runtime closure.

Relevant Go runs use isolated `/private/tmp` caches. The real-model opt-in test was
not enabled and is reported as skipped/not run, not a passing capability test.
Root/Desktop `-run '^$'` commands are compilation only. Zero-match probes are not
counted as acceptance. Offline helper failures are substantive remaining failures;
no assertion was skipped or relaxed to make them green.

A broader bounded execution slice found these inherited failures, reproduced on a
copy of the **starting byte snapshot**, with the same reasons:

- `TestCoordinatorBindsAuthorizationCeilingByOperation`: Review has no frozen backend selection.
- `TestReservedExecutionDispatchUsesFrozenRuntimeConfigAndPrompt`: Execution has no frozen backend selection.
- `TestAuthorizedPlanSerializesImplementationAndVerificationWithContinuity`: runtime handoff store unavailable.
- `TestImplementationFailurePreventsVerification`: runtime handoff store unavailable.
- `TestVerificationFailureFailsPlanWithoutRepair`: runtime handoff store unavailable.
- `TestVerificationMutationFailsPlan`: runtime handoff store unavailable.
- `TestVerificationReportMutationPreservesSQLiteTaskAndReleasesClaim`: SESSION_INITIALIZATION_REQUIRED.

Logs: `/private/tmp/devbridge-coding-execution-focused.jsonl`,
`...-execution-baseline.jsonl`, `...-admission-final.jsonl`,
`...-verification-baseline.jsonl`. The old broad execution timeout, older Session/
caller SQLite/controlapi fixtures and broad MCP presentation failures remain in the
prior reports; those entire suites were not rerun or declared fixed.

During integration, the experimental policy's boolean networkAccess variants and a
host-temporary schema-test dependency were identified and repaired. Concurrent-edit
intermediate failures are retained in `...-modules.jsonl`; final tests use portable
fixtures. Cross-review also required an immutable planning seal and an earlier
fallback workspace fence. These checks are not proof of production runtime support.

### Remaining work and final accounting

Complete official isolation for the selected runtime/host, all enabled channels and
same-Thread narrowing remains prerequisite to production activation. Then implement
frozen per-Run member preparation/observations, quiescent Coding evidence, required
repository-qualified Verification.RUN and Coding Review output, canonical acceptance
into existing v76 member delivery, and the requested fault/recovery gates. No alternate
sandbox/backend, environment upgrade, hidden Thread reset or permission relaxation
was used.

Still retained: **PARTIAL_IMPLEMENTATION**, **REAL_CODING_AGENT_E2E_NOT_RUN**,
**SAFE_REPOSITORY_UNLINK_CLOSED**, **MANUAL_DESKTOP_ACCEPTANCE_PENDING**,
**MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING**.

Final status: **184 dirty files**, all unstaged; all **172** inherited paths
remain and the index is empty. Against the initial byte snapshot, this increment
changes **22 files**: **10** inherited dirty files, **0** previously clean
tracked files, and **12** new files. No baseline file was removed.
The exact increment (not the whole inherited diff) is:

```text
docs/architecture/coding-runtime-capability-evidence.md
docs/architecture/feature-area-ownership.md
docs/architecture/project-centric-multi-repository-closure.md
docs/architecture/project-centric-multi-repository-execution-report.md
docs/architecture/purpose-scoped-confirmation.md
docs/architecture/task-execution-v0.1.md
internal/codex/experimental_permission_profile.go
internal/codex/experimental_permission_profile_test.go
internal/codex/wire/experimental_permissions.go
internal/codex/wire/testdata/experimental-0.149.0-permission-contract.json
internal/codex/wire/testdata/generated-0.149.0/ThreadResumeParams.json
internal/codex/wire/testdata/generated-0.149.0/ThreadStartParams.json
internal/codex/wire/testdata/generated-0.149.0/TurnStartParams.json
internal/codex/workset_capability.go
internal/codex/workset_capability_test.go
internal/codexruntime/evidence_identity.go
internal/codexruntime/evidence_identity_test.go
internal/controlapi/confirmation.go
internal/controlapi/multi_repository_coding_admission_test.go
internal/execution/coordinator.go
internal/execution/workset_admission_test.go
scripts/probe_codex_workset.py
```

---

## Prior completed increment: Direct File implementation and delivery

Closure: **MULTI_REPOSITORY_DIRECT_FILE_IMPLEMENTATION_AND_DELIVERY_CLOSED**.
Project status: **PARTIAL_IMPLEMENTATION**. This is the narrow Direct File closure;
it does not claim Coding Agent multi-root support or whole-project completion.
The two prior completed stages are retained below as historical increments.

### Baseline and operational boundary

- Start/end branch: `dev_20260908_1913`.
- Start/end HEAD: `fd8a6afeb213972e8aed00ff23f090ae058b8530`.
- Starting schema **v75**; this increment adds **v76** only.
- Starting dirty files: **142**, all unstaged; starting/final index: **empty**.
- Initial content baseline: `/private/tmp/devbridge-direct-v2-baseline-0p3tfl53/`
  (`manifest.json`, `files/`, `status.txt`). Increment counts compare bytes against
  this 1,722-file snapshot, not the combined diff against HEAD.
- No official-repository stage, commit, merge, push, reset, clean, checkout or
  rebase; Git write effects occurred only in test-owned temporary repositories.
  No live SQLite/DataRoot/runtime, real Coding backend, paid/external service,
  Desktop UI, device, simulator or screenshot was used. Cache/logs are isolated
  under `/private/tmp`. Test IDs belong to disposable fixtures, not live objects.

### Production chain and frozen authority

The production facade/confirmation/file/result/acceptance/Git path now supports:

`formal multi-member DIRECT_FILE proposal → explicit APPROVE (NO_START) → exact
START_DIRECT_FILE confirmation → app+sdk controlled file operations → formal
member result → canonical business acceptance + all Task delivery intents →
per-repository Task commits → separate frozen FeatureArea merge confirmation →
per-repository development-branch merges`.

The revision, approval and complete target set remain immutable. Preparing or
reading a confirmation, approving NO_START, opening a result or restarting Core
does not start writing. START/RECOVER/CANCEL require a consumed CONFIRMING intent
and the exact persisted command request. Generic enable/disable and
workflowComplete cannot control v2 Task grants. Replaying an old start after
closure cannot reactivate it. No fake Coding Execution/Run/Thread is created.

Each required member pins repository/member/version/worktree/root identity, origin,
start HEAD and snapshot, READ permission and WRITE(NONE/PATHS/REPOSITORY). ACTIVE
requires all required members; an absent unrelated repository is irrelevant. Every
v2 file operation carries exact member and epoch/generation. Adding membership does
not expand authorization. Readonly mutations, absent members, cross-repository MOVE,
wrong identity, out-of-scope paths, symlink escape and root replacement are refused.

Mutation intent is durable before effect. Request/payload replay is exact, including
precondition digest, epoch and generation. A synced effect journal proves the
filesystem-success/SQLite-receipt gap. Recovery without proof remains unresolved;
existence/absence alone cannot establish an original effect. Historical replay
never reapplies a completed mutation over later legal bytes. Known pre-effect CAS
failures finish FAILED without poisoning the grant. Completed member work is kept;
there is no cross-file or cross-repository atomicity promise or automatic rollback.

Result finalization shares the mutation/cancel exclusion boundary. Core derives all
member snapshots and changed paths, requires unchanged HEAD and receipt-backed
final bytes, then atomically closes authorization and enters ACCEPTANCE_PENDING.
Exact result replay returns the original persisted result. Acceptance uses the
canonical Task authority and stores every member delivery intent in the same
transaction as business success. Accepted and Git-complete remain distinct facts.

Required RUN checks are rejected before start. Positive fixtures explicitly choose
USER_WAIVED through user confirmation and retain the original RUN proposal; no
silent verification rewrite or new verification executor is introduced. This fixture
choice is not a waiver or execution authorization for the user's actual projects.

### Delivery and schema v76

Task delivery freezes exact accepted result/revision/member/root/branch/HEAD and
snapshot. Private-index selective commits include only verified paths, retaining
unrelated dirty bytes and the real index. Readonly and unchanged members remain in
the aggregate as NO_CHANGE without fabricated commits. Lost commit responses require
exact original parent, provenance and expected tree proof, including deletions.

FeatureArea plans cover actual accepted, committed, undelivered member work across
Tasks. A separate confirmation freezes each source/branch, target developmentRef,
expected target HEAD, DevelopmentCycle and Task provenance. Unowned source commits
are rejected. Original plans survive restart and changed branch mappings. Git
common-directory/ref locks and DataRoot writer fencing serialize actual shared
resources; Git I/O is outside long SQLite write transactions. Completed children
are retained, unknown effects are proven before recording, and the parent completes
only when the full frozen set is terminal. Conflicting writes remain fenced while
delivery is unresolved. GET/projection never executes Git or repairs receipts.

V76 adds member/start/mutation and Task/FeatureArea member-delivery receipts and the
minimum nullable legacy scalar columns for protocol 2. Immutable history guards and
complete-parent checks retain v1 rows, IDs, original payload bytes/hash, grants,
results and commit/merge receipts. Historical migrations are unchanged. Migration
acceptance starts at v75, verifies history and foreign keys, and reapplies migration
idempotently; no old NO_START/closed record acquires new authority. Pure v2 responses
use member facts, never a fabricated aggregate root or commit SHA.

MCP/Core confirmation and file/result schemas expose the exact fences, optional
explicit waiver choice and frozen merge plan. Task/FeatureArea and product text
project grant state, allowed repositories, member commits/merges and aggregate
completion from Core. Manual Desktop/Card visual interaction remains pending.

### Isolated production acceptance: G1–G8

All fixtures use at least three independent temporary Git repositories for the
multi-repository path, temporary SQLite/DataRoots, and real local Git commands.
They create/approve Tasks through production protocols; no approved definition or
SUCCEEDED Task is inserted to manufacture the positive chain. Fault injections alter
only test-owned effect/receipt windows or preparation state.

| Gate | Actual acceptance and outcome |
| --- | --- |
| G1 | PASS: formal DIRECT_FILE review/NO_START produces no write grant or Coding execution; explicit start modifies same-named allowed files in app and sdk; excluded backend is unavailable without blocking either member. |
| G2 | PASS: app writes, sdk reads only; all readonly mutations, absent/wrong/new members, wrong member version, out-of-scope paths, cross-member MOVE, traversal/symlink and replaced roots are rejected. |
| G3 | PASS: CREATE/REPLACE/DELETE/MOVE effect-before-receipt faults reopen Core and converge via original proof. Exact replay returns the old result without overwriting newer legal bytes. Changed content/digest/epoch/generation conflicts. Missing proof blocks replay/finalization/cancel; known pre-effect failure remains safe. |
| G4 | PASS: complete dual-member formal result/read/replay, payload conflict and late writes; all four mutation kinds produce accepted selective commits. External/unreceipted/overwritten bytes and HEAD drift cannot become accepted evidence. Required RUN is blocked before activation. |
| G5 | PASS: full member intent accompanies acceptance; first Git effect with lost receipt leaves partial delivery; restart verifies original commit and finishes remaining members without duplication. Readonly/NO_CHANGE have no fake SHA; unrelated dirty bytes/index remain intact. |
| G6 | PASS: two separate development targets merge; effect-before-receipt and second dirty target leave incomplete original plans. Restart preserves first completion and original mappings. Two FeatureAreas freeze the same target HEADs; after first delivery the stale second confirmation is refused without receipts or ref changes, including exact replay. |
| G7 | PASS: mutation/finalization exclusion, consumed confirmation fences, formal cancellation, UNKNOWN, old epochs/generations, restart, preserved partial/FAILED preparation and explicit recovery. Generic hidden enable/disable and stale startup replay cannot bypass authority. |
| G8 | PASS: v1 Direct File/result/accept/commit/merge and raw hash compatibility, inherited v2 Upper Review/NO_START suite, historical v75→v76 migration and foreign_key_check. Pinned exact-workset Coding admission still blocks before Run/backend dispatch; unlink remains blocked. |

Independent integration review inspected the final production code, not only agent
reports, and returned PASS with no unresolved blocker. It independently reran the
G3/G4/G7 production slice (30.070s), selective Task/frozen merge Git slice (2.180s),
and diff whitespace checks. The additional competing-FeatureAreas case ran with the
other three delivery fault tests: four passed in 17.800s.

### Verification commands and actual matched counts

All Go commands below use `GOCACHE=/private/tmp/devbridge-direct-gocache`.
Top-level counts exclude subtests and repeated runs; zero-match commands are not
acceptance. Full-suite compilation intentionally uses no-test mode and is labelled
separately. No broad-suite pass is claimed.

```sh
go test -json ./internal/controlapi -run '^Test(MultiRepositoryDirect|MultiRepositoryUpper|UpperV1Facade)' -count=1 -timeout=120s
go test -json ./internal/directfile ./internal/git ./internal/delivery ./internal/task ./internal/workspace ./internal/workset ./internal/review ./internal/reviewgrant ./internal/run -count=1 -timeout=90s
go test -json ./internal/storage/sqlite -run 'Test(V7[56]|MigrationLifecycle|CanonicalDeliveryPaths|CommitPresentation|InventoryCommit|TaskAutoCommit|NewFeatureAreaCommit|TaskCommitHistory|ReviewOnlyDoesNotChangeCommit|TwoFeatureAreasSerialize|RepositoryCommandLock|LegacyDeliveryUsesExactTaskMember|AggregateDeliveryCannotReport|DeliveryBlocksFailed|WorkspaceMember|ProjectCentricMigrationPreserves|ReviewTargetsReject)' -count=1 -timeout=90s
go test -json ./internal/mcpserver ./internal/bridgehttp ./internal/execution ./internal/workflowpresentation -run 'Test(DirectFile|FileImplementation|FeatureAreaMerge|ExecutionConfirmation|ConfirmationCommitSchema|SurfaceFileWrite|ExactWorkset|.*Upper|Commit|InventoryCommit)' -count=1 -timeout=90s
go test ./... -run '^$' -timeout=90s
# Also run in desktop/: go test ./... -run '^$' -timeout=90s
go vet ./internal/controlapi ./internal/directfile ./internal/delivery ./internal/git ./internal/storage/sqlite ./internal/mcpserver ./internal/execution ./internal/workflowpresentation
node --test internal/bridgehttp/product_git_commit_test.js internal/mcpserver/confirmation_project_ui_test.js desktop/frontend/test/project-registration.test.js
git diff --check
```

| Gate set | Actual result |
| --- | --- |
| Production Direct File + inherited Upper/v1 facade | 35 top-level tests PASS; 0 failed (74.458s). |
| Domain packages including real temporary Git | 103 top-level tests PASS; 0 failed. |
| SQLite focused migration/authority/delivery | 19 top-level tests PASS; 0 failed. |
| MCP/HTTP/execution/presentation focused | 13 top-level tests PASS; 0 failed. |
| Related JS contracts | 16 tests PASS; 0 skipped/failed. |
| Root + Desktop compilation | Both PASS (compile gates; not runtime acceptance). |
| Targeted vet / diff --check | PASS. |

JSON logs: `/private/tmp/devbridge-direct-{production-final,domain-tests,sqlite-focused,protocol-focused}.jsonl`.
Final compile/vet/JS logs: `/private/tmp/devbridge-direct-{root-compile,desktop-compile,vet,frontend}-final.log`.
Protocol HTTP tests used approved sandbox escalation only for test-owned loopback
listeners; no live service or Desktop UI was involved.

### Repaired failures, historical failures and remaining scope

During this increment focused tests/review exposed and corrected: missing exact
confirmation consumption; v2 generic disable bypass; lost file/Git response proofs;
unresolved pre-effect failures; v2 member FAILED recovery; stale operation hash
fences; MOVE digest/HEAD/uncontrolled-byte result checks; and deterministic Task and
merge tree proof. The earlier production log had two generic-disable failures;
both are retained in `/private/tmp/devbridge-direct-production-gates.jsonl`, with
passing final reruns. Some early compile/schema probes matched no tests and are not
counted here. No permission assertion was weakened to obtain a pass.

Historical broad-suite failures remain recorded in the two prior reports below:

- Older SQLite/controlapi fixtures using disabled pre-initialization Session or
  missing caller/local-ingress contracts: not a claim that every broad failure was
  diagnosed or fixed by this increment; those broad suites were not rerun wholesale.
- Full execution fake-backend missing frozen runtime/handoff facts and ten-minute
  timeout: not rerun wholesale; bounded relevant execution cases passed.
- Broad MCP presentation missing-map panic: not rerun wholesale. The specific old
  visible-enable catalog assertion was updated to the current hidden-tool contract;
  `TestDirectFileToolsExposeBoundedSchemasAndStructuredErrors` now passes.
- Historical Desktop HTTP sandbox-listen failure is retained as historical; this
  increment's bounded protocol listener tests passed under approved isolation.

Remaining: Coding Agent multi-root/mixed permissions **UNSUPPORTED / NOT_RUN**;
real Coding Agent **NOT_RUN**; the unlink limitation recorded by this historical
increment was superseded by the schema-v84 safe unlink closure;
`MANUAL_DESKTOP_ACCEPTANCE_PENDING`; `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`.
No real project verification waiver or Git delivery was performed.

### Current increment file accounting

Final porcelain status contains **172 files**, all unstaged: all **142**
inherited dirty paths remain, plus **30** newly dirty paths. Relative to the
initial byte manifest, this increment changed **55 files**: **25** inherited
dirty files, **14** previously clean tracked files, and **16** new files.
No baseline file or inherited dirty entry was removed. Index remains empty.

Exact increment paths (not the whole inherited worktree diff):

```text
docs/architecture/direct-file-capability.md
docs/architecture/project-centric-multi-repository-closure.md
docs/architecture/project-centric-multi-repository-execution-report.md
docs/architecture/purpose-scoped-confirmation.md
docs/architecture/task-execution-v0.1.md
internal/bridgehttp/ui/product-app.js
internal/confirmation/contract.go
internal/controlapi/confirmation.go
internal/controlapi/confirmation_delivery.go
internal/controlapi/confirmation_display.go
internal/controlapi/confirmation_validation.go
internal/controlapi/conversation_model_default.go
internal/controlapi/multi_repository_direct_authorization_test.go
internal/controlapi/multi_repository_direct_delivery_test.go
internal/controlapi/multi_repository_direct_evidence_test.go
internal/controlapi/multi_repository_direct_fault_delivery_test.go
internal/controlapi/multi_repository_direct_independent_test.go
internal/controlapi/multi_repository_direct_mutation_fault_test.go
internal/controlapi/multi_repository_direct_nochange_test.go
internal/controlapi/multi_repository_direct_v1_test.go
internal/delivery/contract.go
internal/delivery/contract_test.go
internal/directfile/contract.go
internal/directfile/implementation_result.go
internal/directfile/multi_contract.go
internal/directfile/service.go
internal/execution/direct_file_workspace.go
internal/featurearea/commit_facts.go
internal/git/commit.go
internal/git/commit_test.go
internal/mcpserver/confirmation_project_schema_test.go
internal/mcpserver/confirmation_schema.go
internal/mcpserver/confirmation_ui.html
internal/mcpserver/direct_file_members_test.go
internal/mcpserver/direct_file_tools.go
internal/mcpserver/direct_file_tools_test.go
internal/mcpserver/external_schema.go
internal/mcpserver/file_implementation_results.go
internal/storage/sqlite/commit_presentation.go
internal/storage/sqlite/confirmation_approval.go
internal/storage/sqlite/confirmation_recovery.go
internal/storage/sqlite/direct_file_authority.go
internal/storage/sqlite/direct_file_confirmation.go
internal/storage/sqlite/direct_file_result_store.go
internal/storage/sqlite/direct_file_task_authority.go
internal/storage/sqlite/git_commit_store.go
internal/storage/sqlite/migrate.go
internal/storage/sqlite/multi_delivery.go
internal/storage/sqlite/multi_delivery_test.go
internal/storage/sqlite/multi_repository_direct_migration.go
internal/storage/sqlite/multi_repository_upper_v75_historical_test.go
internal/storage/sqlite/review_approval_store.go
internal/storage/sqlite/runtime_authority.go
internal/storage/sqlite/task_review_request_store.go
internal/workflowpresentation/text.go
```

---

## Prior completed increment: Upper Review and v2 approval

Closure: **MULTI_REPOSITORY_UPPER_REVIEW_APPROVAL_V2_CLOSED**.
Project status: **PARTIAL_IMPLEMENTATION**. Final acceptance of this increment
is recorded below; the archived first-stage report is historical, not the current
Review/approval capability boundary.

### Baseline and operational boundary

- Start/end branch: `dev_20260908_1913`.
- Start/end HEAD: `fd8a6afeb213972e8aed00ff23f090ae058b8530`.
- This increment started with **102 dirty modified/new files, all unstaged**,
  inherited from the resource/Workspace stage. The starting schema was **v74**.
- The original dirty contents were snapshotted before edits at
  `/private/tmp/devbridge-review-v2-baseline-m6lqb45s`; incremental file counts
  below compare bytes against that manifest, not against HEAD.
- Index remained empty. No official-repository staging, commit, merge, push,
  reset, clean, checkout or rebase was performed.
- No live runtime/DataRoot/SQLite, external backend, paid service, Desktop UI,
  device or simulator was operated. Real Git operations use test-owned temporary
  repositories only; Go caches are under `/private/tmp`.

### Production change

The existing confirmation, Review admission, canonical Workspace, Run, Upper
file grant, result submission and approval writers now form this chain:

`repository-free Task → confirmed explicit app+sdk readonly targets → one
UPPER_LLM Review / one Run / two members → exact managed-worktree reads → formal
schema-v2 report → canonical APPROVE (NO_START) → immutable v2 definition`.

No Project registration or Desktop project-entry subsystem was reimplemented.
Necessary integration fixes included repository-free Task creation, avoiding a
first-repository FeatureArea baseline, v1 nil/empty scope canonicalization,
REVISE/REJECT confirmation routing, and preserving file source IDs in MCP JSON.

Review authorization, proposed implementation permissions and approved definition
remain distinct. Upper grants authorize only list/read. Generic repository
read/search is denied during the formal grant; there is no new grant-bound search
API. A v2 approval creates neither a Coding Run nor Direct File write authority.
The backend remains irrelevant to Upper Review/v2 NO_START approval.

### Migration and durability

Only **v75** was added this increment. Historical migrations were not rewritten.
It rebuilds `worktrees`, `workspaces` and `review_grants` while preserving rows,
indexes and triggers, removes the creator `worktrees.run_id` UNIQUE constraint,
adds immutable Review targets/grant protocol version, and adds explicit
`run_repository_bindings`. Legacy scalar roots remain v1-only authority.

Bindings persist each repository/member/version/origin and preparation state;
ready bindings pin worktree ID, canonical root identity and observed snapshot.
Creation provenance remains on the original worktree. Migration acceptance starts
at actual v74, with constraints/triggers enabled, and verifies v1 raw payload/hash,
worktree creator, scalar Workspace/grant state and generation, protocol default 1,
removed run uniqueness, idempotent reapplication and `foreign_key_check`.

### Isolated acceptance

The production facade test registers three independent temporary Git repositories.
app/sdk have distinct development branches, origins and same-named files; backend
is not selected and is made unavailable. Confirmation preparation creates no
Review, Run, grant, claim, Workspace or worktree. Confirmation produces exactly
one Task/Review/Run, two member bindings and two physical worktrees. Both original
origins survive admission replay, results, approval and database reopen.

Acceptance includes:

- Exact file source identity and evidence, wrong member/repository denial,
  traversal, child symlink escape, root symlink/inode replacement, third-member
  non-expansion and all four file mutation denials.
- Original admission/approval receipt recovery after simulated lost responses;
  MCP result submission discards the first response and replays the same request.
  Altered payloads conflict; object/report/revision counts stay unchanged.
- Second-member failure before physical creation and after physical creation but
  before metadata; retry retains original IDs and first-member dirty bytes with
  exactly two roots and no duplicate creation. A third fault case restarts the Core
  in that metadata gap: formal recovery authorizes the same Run's new epoch and
  generation, old admission replay does not activate it, and exact
  `review_access_prepare` resumes original preparation. A second injected fault
  proves generation-2 diagnostics persist and stale generation-1 calls cannot
  overwrite them.
- Explicit cancellation, old epoch/generation rejection, exact-set restart
  recovery, late reports, completed-grant non-reactivation and readonly finalization.
- Findings-only FURTHER without implementation targets/specs; BLOCKED without
  cited evidence after source loss; REVISE/REJECT and stale competing decisions.
- Cross-member evidence substitution and changed formal approval specs rejected
  transactionally with no report/revision side effects.
- v1 production facade Review/read/result/approval replay and original definition
  bytes/hash; historical migration and existing worktree reuse regressions.
- Public Coding start, Direct File authorization, Task commit retry and FeatureArea
  delivery remain blocked after v2 approval; zero execution proposals, runtime
  handoffs and Git delivery receipts persist before and after reopen.

### Recorded production closure identities

The final bounded production gate passed in **17.610s**. Its test-owned temporary
DataRoot was removed by the test harness; these are observed durable identities,
not IDs injected to bypass the production admission/approval chain.

- task: `task_57b17f9e3d6108384adeb106d23c0c84`.
- review: `review_801949afbb9a2f2eef5140fce163ee61`.
- run: `run_8b36e31bfb74dc5a595129eb7dcdef14`.
- workspace: `workspace_plan_d6411ccb26aea4bfe597a0b2ee2479f4`.
- revision: `task_definition_task_57b17f9e3d6108384adeb106d23c0c84_1_8a2a2edfac2d63a8`.
- hash: `8a2a2edfac2d63a806f1319f0949e4991dd8282c3037647e291210755bca98b0`.

| Repository | Member / version | Worktree | Origin | State |
|---|---|---|---|---|
| `repository_6a077e65-1ea2-43f2-8663-a5c0c0b14871` | `member_0218f17aeeb171811a26a9f4efb8782b` / 1 | `wt_member_ea9fa7454cc7b0cf0e294b5e530e8db5` | `faa809307da68457fc3b7b0dbaf7b4a78bf8bb8b` | READY |
| `repository_b5e9b274-e702-4aac-8a73-2dc7fca4b044` | `member_4dd8ee33caa925e177975ca9d3a3442c` / 1 | `wt_member_c00c1946b3a6c093b8ae691cbc56bb98` | `2d8da4bb3390a5878153d66a5e0ac6ab42155c00` | READY |

Both approved targets have `read=true`, `write=PATHS:[same.txt]`, and a required
repository-qualified verification check. The Review grant itself remained
`write=NONE`. Public revision decoding returned version 2, both complete targets,
WorkSpec and VerificationSpec; hashing persisted original bytes matched the hash
above. The same bytes/hash and exact binding identities were verified after reopen.
The Task phase is IMPLEMENTATION_READY, with zero Coding Execution or write grant.

### Commands and results

All listed commands use an isolated `GOCACHE=/private/tmp/...`.

```sh
go test ./internal/controlapi -run '^Test(MultiRepositoryUpper|UpperV1Facade)' -count=1 -v -timeout=90s
go test ./internal/storage/sqlite -run 'TestV75Preserves|TestReviewTargetsReject|TestUpperV2Root|TestWorkSpecCodec|TestWorkspace(Member|StoreMaterializes)|TestProjectCentricMigrationPreserves' -count=1 -timeout=90s
go test ./internal/review ./internal/reviewgrant ./internal/workset ./internal/workspace ./internal/worktree ./internal/run ./internal/task ./internal/directfile -count=1 -timeout=90s
go test ./internal/execution -run 'Test.*(Upper|ExactWorkset)' -count=1 -timeout=90s
go test ./internal/mcpserver ./internal/execution -run 'TestDirectFileMemberIdentityThroughMCPTransport|TestSurfaceFileWritePreservesExactMemberMachineFields|TestExactWorksetBlockedBeforeAnyRunOrRuntimeStarts|TestConfirmationProject' -count=1 -timeout=90s
go test ./... -run '^$' -timeout=90s
# In desktop/:
go test ./... -run '^$' -timeout=90s
# In repository root:
go vet ./internal/controlapi ./internal/review ./internal/run ./internal/workspace ./internal/storage/sqlite ./internal/mcpserver
node --test internal/mcpserver/confirmation_project_ui_test.js
git diff --check
```

These focused tests, both compilation gates, vet and the 3 JS tests passed.
Independent integration review also reran the affected production and migration
slices; its acceptance covers this narrow closure only.

Failures encountered and corrected this increment: v2 result validation still
using v1 scope; missing no-repository Task admission; missing source IDs in MCP
JSON; malformed legacy historical fixtures; v1 empty-scope persistence mismatch;
non-APPROVE decision routing; missing proposal equality and result source fences.
These were repaired and their focused tests rerun.

Not an all-suite pass: broad MCP tests encountered old presentation metadata
panic/catalog assertions and sandbox listener restrictions. Older SQLite Upper
fixtures still call pre-initialization Session contracts. No blanket attribution
of every older failure is claimed. The full execution suite with the previously
reported long-running fake backend was **not rerun**; bounded relevant execution
checks passed. The first `-run '^TestConfirmation'` MCP probe matched no tests and
is not counted as acceptance; later named schema/transport cases ran.

### Remaining project work / not run

- Actual multi-root/mixed-permission Coding Agent: **NOT_RUN / UNSUPPORTED** by
  the pinned adapter; no real backend dispatch was used as evidence.
- Multi-repository Direct File writes and aggregate commit/merge delivery:
  **BLOCKED**, including partial delivery receipts/recovery and multi-resource locks.
- Safe repository unlink: **CLOSED** by the later schema-v84 lifecycle and
  centralized authority checks; this line supersedes the earlier snapshot.
- `MANUAL_DESKTOP_ACCEPTANCE_PENDING`.
- `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`.

### Current increment file accounting

The final `git status --porcelain --untracked-files=all` contains **142 files**,
all unstaged: the 102 inherited dirty files are retained, and 40 more files are
now dirty. Relative to the byte snapshot, this increment changed **61 files**:
21 inherited dirty files, 25 previously clean tracked files, and 15 new files.
No inherited dirty entry was removed. Source changes are limited to the existing
Review/confirmation/Run/Workspace/grant/result/approval chain and its protocol,
migration, tests and these canonical documents.

Exact increment paths (not the inherited 102-file scope):

- `docs/architecture/project-centric-multi-repository-closure.md`
- `docs/architecture/project-centric-multi-repository-execution-report.md`
- `internal/confirmation/contract.go`
- `internal/controlapi/api.go`
- `internal/controlapi/confirmation.go`
- `internal/controlapi/confirmation_validation.go`
- `internal/controlapi/multi_repository_review.go`
- `internal/controlapi/multi_repository_upper_active_access_test.go`
- `internal/controlapi/multi_repository_upper_independent_review_test.go`
- `internal/controlapi/multi_repository_upper_integration_test.go`
- `internal/controlapi/multi_repository_upper_lifecycle_test.go`
- `internal/controlapi/multi_repository_upper_preparation_test.go`
- `internal/controlapi/multi_repository_upper_v1_compatibility_test.go`
- `internal/controlapi/upper_review_access.go`
- `internal/directfile/contract.go`
- `internal/execution/coordinator.go`
- `internal/execution/coordinator_test.go`
- `internal/featurearea/service.go`
- `internal/mcpserver/adapter_core.go`
- `internal/mcpserver/confirmation_schema.go`
- `internal/mcpserver/confirmation_ui.html`
- `internal/mcpserver/context_tools.go`
- `internal/mcpserver/direct_file_tools.go`
- `internal/mcpserver/external_schema.go`
- `internal/mcpserver/spec_schemas.go`
- `internal/mcpserver/upper_review_tools.go`
- `internal/mcpserver/workset_schema.go`
- `internal/review/implementation_result.go`
- `internal/review/report.go`
- `internal/review/report_test.go`
- `internal/review/review.go`
- `internal/review/upper_evidence.go`
- `internal/run/repository_binding.go`
- `internal/run/service.go`
- `internal/scopereview/service.go`
- `internal/storage/sqlite/agent_recovery_store.go`
- `internal/storage/sqlite/confirmation_approval.go`
- `internal/storage/sqlite/confirmation_recovery.go`
- `internal/storage/sqlite/confirmation_upper_read.go`
- `internal/storage/sqlite/feature_area_store.go`
- `internal/storage/sqlite/migrate.go`
- `internal/storage/sqlite/multi_repository_upper_migration.go`
- `internal/storage/sqlite/multi_repository_upper_v75_historical_test.go`
- `internal/storage/sqlite/review_approval_store.go`
- `internal/storage/sqlite/review_store.go`
- `internal/storage/sqlite/run_repository_binding_store.go`
- `internal/storage/sqlite/specs.go`
- `internal/storage/sqlite/specs_test.go`
- `internal/storage/sqlite/task_project_intent_test.go`
- `internal/storage/sqlite/upper_review_access.go`
- `internal/storage/sqlite/upper_review_access_v2.go`
- `internal/storage/sqlite/upper_review_access_v2_test.go`
- `internal/storage/sqlite/upper_review_cancel_eligibility.go`
- `internal/storage/sqlite/upper_review_lifecycle.go`
- `internal/storage/sqlite/upper_review_recovery_eligibility.go`
- `internal/storage/sqlite/upper_review_result.go`
- `internal/storage/sqlite/workspace_members_test.go`
- `internal/storage/sqlite/workspace_store.go`
- `internal/workspace/members_test.go`
- `internal/workspace/memory_store.go`
- `internal/workspace/service.go`

## Archived first-stage report

Everything below describes the preceding resource/Workspace implementation and
its original clean baseline. Its Review/approval blockers and schema-v74 endpoint
are superseded by the current increment above; its unfinished implementation,
Git delivery and manual acceptance boundaries remain applicable.


**Result: PARTIAL_IMPLEMENTATION. Full requested closure is not implemented.**

## Repository and operational boundaries

- Starting / final branch: `dev_20260908_1913` (unchanged).
- Starting / final HEAD: `fd8a6afeb213972e8aed00ff23f090ae058b8530` (unchanged).
- Initial working tree and index: clean. No pre-existing dirty changes existed.
- Final index: unchanged and empty. All implementation changes remain unstaged.
- No commit, merge, rebase, reset, clean, checkout or push in the official repository.
- No formal runtime, live SQLite, user DataRoot or external service configuration was changed.
- No real Coding Agent, paid external service, Desktop UI, device or simulator was run.
- SQLite, Git mutations and fake protocol tests used test-owned temporary resources.
- Starting schema v72; added migrations v73 and v74. Historical schema files were not edited.
- Runtime contract reviewed: pinned Codex app-server 0.149.0 wire adapter. No runtime capability is inferred from prompts.

## Module results

| Module | Implemented | Remaining |
|---|---|---|
| Project resource registration | Empty/single/multiple repositories; append; canonical Git validation; stable aliases; atomic SQLite business writes/events/receipt; exact durable replay and payload conflict | Safe unlink lifecycle is blocked, not implemented |
| Desktop / project selection | Repository inventory; frozen retry target; Project-only confirmation; independent per-repository branch configuration; repository-qualified command receipts/recovery | Manual UI acceptance; pagination UI beyond 100 Projects (explicitly unavailable rather than truncated success) |
| Task intent | Create planning intent without repository/baseline; exact creation receipt; zero authorization side effects | Later repository-target attachment through a complete approved Review workflow |
| Definitions / verification contracts | v2 targets and repository-qualified checks; no conflicting scalar authority; v1 raw bytes/hash preservation; target clone and mutation guards | New production approvals still write v1. Multi-repository Review admission/grants/approval/successor/provenance/result schema are not closed |
| Workspace / worktree | Existing canonical Workspace gains immutable members; exact member lookup; reuse; stable nonoverlapping worktree identities; physical-creation/metadata-loss recovery; accurate partial preparation state | Legacy scalar columns remain; historical `worktrees.run_id UNIQUE` still blocks multi-worktree metadata for one logical Run; not all consumers converted |
| Execution / Direct File | New unsupported targets blocked before unsafe fallback; explicit repository/member fields traverse file schemas/protocol; old grant identity checks; cross-member atomic MOVE rejected | Exact multi-root/mixed runtime permissions, durable multi-member grants, per-repository Run snapshots and evidence are not implemented |
| Git delivery | Legacy Task delivery selects exact member; target readiness enforced; unrelated pending members do not block; multi-Task cross-repository delivery explicitly blocked | Aggregate per-repository commit/merge receipts, partial-success recovery, frozen targets and multi-resource locking are not implemented |
| Canonical docs | Eight requested documents link the accepted Project-centric model and precise current implementation boundary | Full workflow documentation must evolve with the unfinished production paths |

The frozen architecture was not replaced. Blocked new paths do not select the first
repository, expand to all Workspace members, split into extra product Tasks, or
claim one repository's result as an aggregate success.

See [detailed contracts and limitations](project-centric-multi-repository-closure.md).

## Actual verification

PASS:

- Root `go test ./... -run '^$'`: every package and test package compiles.
- Desktop `go test ./... -run '^$'`: every Desktop package and test package compiles.
- Whole packages: projectregistration, repository, developmentcycle, task, review,
  reviewgrant, scopeexpansion, workset, workspace, worktree, directfile and codex.
- Focused production HTTP/bootstrap registration with three independent Git
  repositories; empty Project; adding repositories; invalid-batch rollback;
  duplicate/symlink alias and cross-Project conflicts; unavailable HEAD classification.
- Durable registration replay after database reopen and source unavailability;
  eight simultaneous retries produce one Project/repository/receipt; injected
  repository-write failure leaves no partial Project/repository/event/receipt.
- Initialized AISession to empty Project via SELECT_PROJECT; read-only options;
  three-repository development context with two configured and one unavailable;
  exact branch receipts survive branch removal and UNKNOWN metadata recovery.
- Real v72 temporary database migrated to v74 twice; Workspace identity and origin
  unchanged; PENDING and UNKNOWN member facts retained explicitly.
- Pure planning Task with no repository creates no Review, Run, definition or grant.
  V1 original bytes/hash remain verifiable; v2 verifies exact identities, permission
  scopes and full required verification coverage.
- Three independent temporary Git member worktrees with different HEADs; restart
  after physical creation; metadata persistence failure/retry; dirty work retained;
  one failed member does not clean others; new roots cannot be read through old root.
- SQLite member append, immutable origin, unregistered-member rollback, member-ID
  collision rollback and retained membership evidence.
- Real in-memory MCP transport passes file repository/member/destination fields
  through JSON schemas to the Core adapter. Unsupported execution exits before
  backend start. These tests do not establish actual backend multi-root isolation.
- Legacy Git Task acceptance/commit and FeatureArea merge, snapshot drift,
  response-loss retry, subsequent Task history, re-verification, command locks and
  different-FeatureArea merges.
- A real single-repository accepted Task commits/merges while two unrelated
  Workspace members remain PENDING; both unrelated repositories' HEADs are unchanged.
- A FAILED exact member blocks commit/retry/merge while retaining accepted Task
  facts and pending receipt. Reattaching the same member resumes the original receipt.
- Legacy cross-repository FeatureArea delivery is rejected before creating a
  delivery receipt; this is an unsupported-path test, not partial-delivery success.
- Desktop focused Go tests; project registration/card JS tests (13 tests in the
  final root rerun), related project view tests and Vite build to a temporary output.
- Focused `go vet` across changed Core packages and `git diff --check`.

Representative final commands (all used isolated Go caches under `/private/tmp`):

```sh
go test ./internal/projectregistration ./internal/repository ./internal/developmentcycle ./internal/task ./internal/review ./internal/reviewgrant ./internal/scopeexpansion ./internal/workset ./internal/workspace ./internal/worktree ./internal/directfile ./internal/codex -count=1 -timeout=90s
go test ./internal/storage/sqlite -run 'TestProjectCentricRegistration|TestProjectRegistrationReceipt|TestProjectPlanningIntent|TestReviewTargetsReject|TestTaskOwnedSchemaFresh|TestWorkspaceMember|TestLegacyDeliveryUsesExactTaskMember|TestAggregateDeliveryCannotReport|TestDeliveryBlocksFailed|TestTaskAutoCommit|TestNewFeatureAreaCommit|TestTaskCommitHistory|TestReviewOnlyDoesNotChangeCommit|TestTwoFeatureAreasSerialize|TestRepositoryCommandLock|TestTaskAcceptance' -count=1 -timeout=90s
go test ./internal/storage/sqlite -run TestProjectCentricMigrationPreserves -count=1
go test ./internal/controlapi ./internal/bridgehttp ./internal/mcpserver ./internal/execution -run 'TestProjectOnlyConfirmation|TestConfigureDevelopmentContext|TestProjectSelectionInventory|TestProjectSelectionDoesNotRequire|TestAggregateProjectRegistrationHTTP|TestProductionProjectRegistration|TestDirectFileMemberIdentityThroughMCPTransport|TestSurfaceFileWrite|TestExactWorksetBlocked|TestConfirmationProject' -count=1 -timeout=90s
node --test desktop/frontend/test/project-registration.test.js internal/mcpserver/confirmation_project_ui_test.js
```

FAIL / not an all-suite pass:

- Broader SQLite/controlapi suites contain older fixtures calling disabled
  project-first Session creation or missing caller/local-ingress identity. Relevant
  shared Task/Git fixtures were updated to the existing initialization/backend/mode
  protocols; unrelated failures were not masked.
- The full execution suite has older fake backends missing frozen backend/runtime
  handoff facts and timed out after ten minutes while waiting for runs.
- Existing broad MCP presentation test panics on a missing map;
  `TestDirectFileToolsExposeBoundedSchemasAndStructuredErrors` expects the now-hidden
  `direct_file_enable` tool. The added current-protocol transport tests pass.
- One Desktop real-HTTP integration attempt was blocked by sandbox loopback-listen
  restrictions. An isolated MCP HTTP rerun with elevated sandbox permission could
  listen but encountered the old catalog assertion above.

NOT_RUN / PENDING:

- One approved Task modifying app + sdk, with backend excluded/read-only: PENDING.
- Actual mixed write/read/absent permissions and resume narrowing in a real Coding
  Agent: NOT_RUN; current pinned adapter explicitly UNSUPPORTED.
- Aggregate app success + sdk failure/response-loss commit/merge recovery: PENDING.
- Complete multi-repository cancellation, UNKNOWN, epoch/late-callback and atomic
  finalization lifecycle: PENDING. Existing relevant single-repository regressions
  cannot substitute for that verification.
- MANUAL_DESKTOP_ACCEPTANCE_PENDING
- MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING

## Changed files

102 tracked-modified or newly created source/document/test files are listed
below. Generated Wails bindings were refreshed locally but are gitignored; their
source Go DTOs and bridge methods are tracked.

- `desktop/frontend/src/i18n.js`
- `desktop/frontend/src/main.js`
- `desktop/frontend/src/project-registration.js`
- `desktop/frontend/src/views.js`
- `desktop/frontend/test/project-registration.test.js`
- `desktop/frontend/test/views.test.js`
- `desktop/project_repositories.go`
- `desktop/projects.go`
- `desktop/projects_test.go`
- `docs/architecture/development-cycle-work-model.md`
- `docs/architecture/direct-file-capability.md`
- `docs/architecture/feature-area-continuation.md`
- `docs/architecture/feature-area-ownership.md`
- `docs/architecture/project-centric-multi-repository-closure.md`
- `docs/architecture/project-centric-multi-repository-execution-report.md`
- `docs/architecture/purpose-scoped-confirmation.md`
- `docs/architecture/task-execution-v0.1.md`
- `docs/interfaces.md`
- `docs/user-guide/getting-started.md`
- `internal/aicontext/session_initialization.go`
- `internal/bridgehttp/project_registration.go`
- `internal/bridgehttp/project_registration_test.go`
- `internal/codex/workset_capability.go`
- `internal/codex/workset_capability_test.go`
- `internal/confirmation/contract.go`
- `internal/controlapi/confirmation.go`
- `internal/controlapi/confirmation_creation.go`
- `internal/controlapi/confirmation_project_integration_test.go`
- `internal/controlapi/confirmation_project_selection.go`
- `internal/controlapi/confirmation_project_selection_test.go`
- `internal/controlapi/confirmation_validation.go`
- `internal/controlapi/errors.go`
- `internal/controlplane/bootstrap.go`
- `internal/developmentcycle/service.go`
- `internal/directfile/contract.go`
- `internal/directfile/service.go`
- `internal/execution/conversation_failure_test.go`
- `internal/execution/conversation_runtime_test.go`
- `internal/execution/coordinator.go`
- `internal/execution/direct_file_workspace.go`
- `internal/execution/workset_admission_test.go`
- `internal/mcpserver/confirmation_project_schema_test.go`
- `internal/mcpserver/confirmation_project_ui_test.js`
- `internal/mcpserver/confirmation_schema.go`
- `internal/mcpserver/confirmation_tools.go`
- `internal/mcpserver/confirmation_ui.html`
- `internal/mcpserver/dev_bridge_ui.html`
- `internal/mcpserver/direct_file_members_test.go`
- `internal/mcpserver/direct_file_tools.go`
- `internal/mcpserver/external_projection.go`
- `internal/mcpserver/session_initialize.go`
- `internal/projectregistration/aggregate.go`
- `internal/projectregistration/service.go`
- `internal/repository/memory_store.go`
- `internal/repository/service.go`
- `internal/repository/service_test.go`
- `internal/review/depth_contract_test.go`
- `internal/review/review.go`
- `internal/review/review_test.go`
- `internal/storage/sqlite/confirmation_recovery.go`
- `internal/storage/sqlite/delivery_repository.go`
- `internal/storage/sqlite/delivery_repository_test.go`
- `internal/storage/sqlite/direct_file_authority.go`
- `internal/storage/sqlite/direct_file_authority_test.go`
- `internal/storage/sqlite/direct_file_task_authority.go`
- `internal/storage/sqlite/git_commit_store.go`
- `internal/storage/sqlite/migrate.go`
- `internal/storage/sqlite/project_centric_migration.go`
- `internal/storage/sqlite/project_centric_migration_test.go`
- `internal/storage/sqlite/project_registration_store.go`
- `internal/storage/sqlite/project_registration_store_test.go`
- `internal/storage/sqlite/review_approval_store_test.go`
- `internal/storage/sqlite/review_store.go`
- `internal/storage/sqlite/task_acceptance_store.go`
- `internal/storage/sqlite/task_acceptance_store_test.go`
- `internal/storage/sqlite/task_definition_revision_store.go`
- `internal/storage/sqlite/task_owned_workflow_test.go`
- `internal/storage/sqlite/task_project_intent_migration.go`
- `internal/storage/sqlite/task_project_intent_test.go`
- `internal/storage/sqlite/task_store.go`
- `internal/storage/sqlite/upper_review_access.go`
- `internal/storage/sqlite/workspace_members_test.go`
- `internal/storage/sqlite/workspace_store.go`
- `internal/task/memory_store.go`
- `internal/task/revision.go`
- `internal/task/service.go`
- `internal/task/spec.go`
- `internal/task/targets.go`
- `internal/task/targets_test.go`
- `internal/task/task.go`
- `internal/workset/contract.go`
- `internal/workset/contract_test.go`
- `internal/workspace/member.go`
- `internal/workspace/members.go`
- `internal/workspace/members_test.go`
- `internal/workspace/memory_store.go`
- `internal/workspace/service.go`
- `internal/workspace/workspace.go`
- `internal/worktree/durable_manager.go`
- `internal/worktree/manager.go`
- `internal/worktree/multi_member_test.go`
- `internal/worktree/worktree.go`

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.


## 2026-09-17：App Server continuity 后续增量

当前 Coding v1 已闭合的既有结论保留。新 schema v79 的跨 Core 接线只在固定 App Server exact pending replay 证据后恢复原操作；不重建 RuntimeWorkset、Run 或 Turn。完整范围、实际测试与 G5 未闭合边界见 [continuity 执行记录](codex-request-cross-core-continuity.md)。本增量不是整体 workspace closure，也不改变 sandbox deferred / manual acceptance pending。
