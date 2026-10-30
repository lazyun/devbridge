# Project Inspection Recovery Status G10 closure

Status: `CODEX_RECOVERY_STATUS_INSPECTION_G10_CLOSED` and
`CODEX_RECOVERY_STATUS_RECONCILIATION_V1_CLOSED`.

## Baseline and boundary

The work started and ended on branch `dev_20260908_1913` at HEAD
`fd8a6afeb213972e8aed00ff23f090ae058b8530`, with an empty index. The schema
advanced from v82 to v83 only in test-owned databases. No live database,
global Codex configuration, installed binary, registered checkout, or real
model was changed. The byte baseline is
`/private/tmp/devbridge-inspection-g10-baseline.ri0mrhgh`.

The baseline contained 1,903 non-ignored files and 415 per-file dirty entries;
the final tree contains 1,915 files and 427 dirty entries. Relative to the
saved baseline bytes, 46 files changed (34 modified, 12 new, 0 deleted), with
3,148 lines added, 495 removed and a net 143,499-byte increase. The branch and
HEAD stayed fixed and the index remained empty throughout.

The owner remains `PROJECT_INSPECTION:<inspectionOperationId>`. Its Task has
`scope=PROJECT` and `featureAreaId=NULL`; it has no FeatureArea,
TaskDefinitionRevision, Execution, or Review. The Recovery child is a new Run
and Turn under the same operation, canonical conversation, App Server instance,
Thread, managed Workspace, RuntimeWorkset and target set.

## Production contract

The owner variant extends the shared `RecoveryStatusService`,
`RecoveryStatusStore`, report, Assessment, restart logic and projection. It
does not introduce a second recovery workflow. Admission verifies the exact
owner Task and operation, source Run/Turn/epoch, immutable target receipt,
managed member/worktree identities, RuntimeWorkset hash, conversation, claim,
App Server instance, Thread idle/terminal facts, pending request and
cancellation state.

Project Inspection uses `RecoveryStatusReport v2`; its required
`inspectionResult` is parsed by the same canonical Inspection schema and
parser. Existing owner report v1 bytes and hashes are unchanged. Before/after
observations bind every managed member, root identity, HEAD, snapshot, branch,
index/diff/metadata digests and changed paths. Mutation or drift remains
visible and prevents success.

The immutable Assessment binds source and child Run/Turn identities, report
and Inspection-result hashes, RuntimeWorkset hash, owner and factual evidence,
old response disposition, reason codes and actions. Resolve consumes only that
Assessment. Complete, verified partial and revision-required outcomes enter the
same canonical Inspection reducer and create an immutable v83 provenance
receipt containing `RECOVERY_STATUS`, source and child identities,
Inspection-result/report/Assessment/owner-facts hashes and disposition.

A verified partial source attempt is finalized as BLOCKED. An explicit,
caller-authored continuation receipt creates a new Run and Turn on the same
operation, S1, Thread, Workspace, workset and target set. It reuses the
operation's immutable AgentExecution association and takes the single Project
Inspection claim. Changed replay payloads, actors and stale epochs fail closed.
Target expansion instead requires a fresh Project Inspection prepare/confirm
and produces a new owner, Workspace, conversation, Thread and workset.

An old `responseDisposition=UNKNOWN` remains unchanged even after engineering
recovery succeeds. A new child `CODEX_REQUEST` uses the mandatory user-decision
ledger, reattaches across Core restart to the same child Run/Thread/Turn, and is
never answered or resent by Core. Historical Inspection without v82 canonical
runtime facts remains readable and cannot prepare Recovery Status.

## G10 acceptance

The fixed local Codex App Server 0.149.0 was selected explicitly. The production
facade, SQLite stores, real Client serializer, Unix transport, managed
worktrees, canonical conversation/claim, structured report, factual observer,
Assessment and Inspection reducer were all exercised.

| Gate | Result |
| --- | --- |
| G10.1 complete | PASS: recovered result finalized by the canonical reducer; no replacement Inspection, FeatureArea or Execution. |
| G10.2 old UNKNOWN | PASS: engineering result recovered while the original request and UserDecision remained UNKNOWN and byte-stable. |
| G10.3 partial | PASS: verified BLOCKED source, explicit receipt-bound new Run/Turn, same S1/Thread/workspace/workset/targets, exact caller instruction. |
| G10.4 conflict | PASS: reported facts absent from observations produced conflict and no owner advance. |
| G10.5 mutation | PASS: actual managed-worktree mutation was retained and prevented success; no reset or clean. |
| G10.6 new request | PASS: real child CODEX_REQUEST, immutable UserDecision, Core reopen and exact reattach without redispatch. |
| G10.7 expansion | PASS: revision-required source closed; same-operation continuation rejected; three-repository successor required formal prepare/confirm. |
| G10.8 restart | PASS: seven dedicated persistence windows plus active request, failed restart and start uncertainty; exact replay and no duplicate model call. |
| G10.9 serialization | PASS: same owner contender and old source callbacks rejected; independent Inspection owner used distinct claim/conversation/workspace; FeatureArea namespace regression retained. |
| G10.10 legacy | PASS: prepare rejected with zero Recovery/conversation/workspace/workset side effects; historical row remained readable. |
| G10.11 clients | PASS: shared Core projection, Project scope and NULL FeatureArea; HTTP/MCP continuation DTOs reject outcome/facts/targets/force. Desktop frontend tests passed; manual Desktop/Card acceptance remains pending. |
| G10.12 loss/cancel | PASS: report, Assessment and resolution reply loss, child response loss, cancel/restart and stale source callback converged by original identities. |

Final acceptance log:
`/private/tmp/devbridge-g10-acceptance-final.jsonl` — 11 main subtests PASS,
7 persistence-window subtests PASS, legacy PASS, zero FAIL. The explicit real
model gate skipped once by default and is recorded as
`REAL_CODEX_INSPECTION_RECOVERY_E2E_NOT_RUN`.

The shared Recovery Status regression log is
`/private/tmp/devbridge-recovery-g1-g16-final.jsonl` — 40 test events PASS,
zero FAIL and one expected real-model SKIP. It covers Implementation complete
and partial, Verification, CODING_AGENT Review, conflicts, mutation, requests,
revision, admission, restart, serialization, prompt neutrality, projection and
the v77–v82 compatibility chain.

Additional final checks:

- storage Inspection/Recovery/migration selection: 24 PASS;
- Recovery domain and canonical Inspection runtime: 85 PASS;
- execution/HTTP/MCP Recovery surfaces: 29 PASS;
- root compile: 63 packages PASS, 17 packages with no tests;
- Desktop Go compile and vet: PASS;
- targeted race: PASS;
- Desktop frontend: 49 PASS and production build PASS;
- root vet and `git diff --check`: PASS.

The broad embedded JavaScript suite retains pre-existing UI assertion failures
outside G10; it is not reported as green. Manual Desktop and ChatGPT Card
acceptance remains pending. No real model or paid service ran.

## Retained status labels

The closure retains:

- `PROJECT_INSPECTION_CANONICAL_RUNTIME_V1_CLOSED`
- `CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`
- `CODEX_REQUEST_USER_DECISION_V1_CLOSED`
- `CODEX_PENDING_REQUEST_CROSS_CORE_REATTACH_CLOSED`
- `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`
- `MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`
- `TRUSTED_EXECUTOR_MODEL_V1`
- `CODEX_SANDBOX_HARDENING_DEFERRED`
- `PARTIAL_IMPLEMENTATION`
- `REAL_CODING_AGENT_E2E_NOT_RUN`
- `REAL_CODEX_INSPECTION_RECOVERY_E2E_NOT_RUN`
- `SAFE_REPOSITORY_UNLINK_CLOSED`
- `MANUAL_DESKTOP_ACCEPTANCE_PENDING`
- `MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`

It does not claim old answer consumption, exactly-once response delivery, a
runtime broker, hostile sandbox proof, or the forbidden project-centric
workspace implementation label.
