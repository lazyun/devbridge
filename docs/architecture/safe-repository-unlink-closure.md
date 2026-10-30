# Safe Repository Unlink closure

Status: `SAFE_REPOSITORY_UNLINK_CLOSED`
Schema: v84
Date: 2026-09-17

Repository unlink is a logical, irreversible transition from `ACTIVE` to
`UNLINKED`. The Repository row keeps its ID, original Project ID, canonical
path, stable alias and historical references. The path and alias are not
released for registration, relink or cross-Project transfer. The command never
opens Git, deletes local files, removes worktrees, rewrites refs or cleans
historical records.

The v84 authority consists of lifecycle fields on `repositories`, one monotonic
`project_repository_topologies` revision per Project, and immutable
`project_repository_unlink_receipts`. Exact replay uses the request payload hash.
The same request and payload returns the stored result; a changed repository,
reason, generation or topology conflicts. Response loss followed by Core reopen
does not repeat the event, generation increment or topology increment.

`RepositoryUnlinkStore` is the single eligibility and commit authority. It reads
durable facts for active Tasks and current definition targets; current Reviews;
nonterminal Runs and runtime requests; FeatureArea and Project Inspection
workspace members; Direct File grants, members and unresolved mutation receipts;
Recovery Status; and pending single- or multi-repository Task/FeatureArea Git
delivery. Terminal Task/Run/Review records, completed receipts and DECOMPOSED or
retired workspace provenance remain historical references rather than blockers.

Commit opens the canonical SQLite writer transaction, checks Project ownership,
ACTIVE state, registration generation and Project topology revision, and then
recomputes every blocker. A successful transaction writes the receipt, changes
the lifecycle and generation, closes the current DevelopmentCycle with
`REPOSITORY_UNLINKED`, increments topology, marks Project Context `REQUIRED`,
removes only current Module Map entries for that Repository, appends one
`project.repository_unlinked` event and commits. Any failure rolls the whole
transaction back.

All new-work writers check ACTIVE registration inside their final transaction.
This includes Task/definition targets, Workspace members, Reviews, Run bindings,
Direct File authorities and members, Recovery continuations, inspection runtime,
delivery intents and DevelopmentCycle selection. Database triggers provide a
second fence. Project Context and Module Map publication also compare the
captured topology revision, so work started before unlink cannot publish a stale
current projection afterward.

The Core prepare response owns the `REPOSITORY_UNLINK` confirmation identity,
expected generation, expected topology revision and fixed consequences. Prepare
is read-only. Commit accepts no force, outcome, blocker override, target set or
replacement state. HTTP rejects unknown JSON fields; MCP exposes closed input
schemas and valid output schemas. Desktop renders Core blockers, separates ACTIVE
and removed history, and returns the exact Core-frozen identity on commit.

The production acceptance covers:

- G1–G2: safe unlink, zero-active inventory, historical reads and terminal
  history preservation;
- G3–G7: live Workspace, Inspection/runtime, Task/Review/Run, Direct File and
  Git delivery authority, including effect-before-receipt and v2 member rows;
- G8–G11: DevelopmentCycle history, unavailable paths, topology stale writes and
  old writer rejection without side effects;
- G12–G15: SQLite concurrency, response-loss replay after restart, all request
  conflict variants, and canonical-path reuse rejection in either Project;
- G16–G17: HTTP/MCP/Desktop projections and closed DTOs, plus populated v83 →
  v84 migration, repeat, rollback and foreign-key checks;
- G18: root/Desktop compile, prior Recovery/Inspection/Coding contracts, targeted
  race, vet, JavaScript tests and `git diff --check`.

Test fixtures use temporary DataRoots and temporary Git repositories. The Git
safety test compares HEAD, refs and tracked bytes before and after unlink. Fault
injection pauses Direct File and multi-repository delivery between external Git
or file effects and durable final receipts; eligibility remains blocked until
the parent authority is complete.

The recorded start boundary was branch `dev_20260908_1913`, HEAD
`fd8a6afeb213972e8aed00ff23f090ae058b8530`, schema v83, 1,915 nonignored
files, 427 dirty entries and an empty index. The v84 closure changes 63 files
relative to that byte snapshot: 16 added, 47 modified and none deleted. The
aggregate delta is 152,672 bytes and 2,892 added / 81 removed lines. The final
working tree has 1,931 nonignored files and 450 dirty entries; branch, HEAD and
empty-index state are unchanged.

The final unlink-focused suite recorded 34 passing test and subtest events with
zero failures or skips. The SQLite/control API race run passed under `-race`;
root `go vet ./...`, Desktop compile, Desktop unlink HTTP tests, 52 frontend
tests, the frontend production build and `git diff --check` passed. The broader
deterministic offline App Server suite recorded 72 passing test/subtest events,
with no failures or skips and no real model. The broader root suite still
contains failures that reproduce from the opening byte
snapshot, principally legacy Session/operator fixture assumptions and old
runtime fixture contracts. They are inherited and are not counted as unlink
acceptance. One inherited Upper Review MCP target-schema mismatch was repaired
because Upper Review is a required G18 contract; its exact production round
trip now passes.

The closure does not add relink, Repository transfer, local deletion, managed
worktree cleanup, Project deletion or automatic cancellation/retirement. The
following broader statuses remain: `PARTIAL_IMPLEMENTATION`,
`PROJECT_INSPECTION_CANONICAL_RUNTIME_V1_CLOSED`,
`CODEX_RECOVERY_STATUS_INSPECTION_G10_CLOSED`,
`CODEX_RECOVERY_STATUS_RECONCILIATION_V1_CLOSED`,
`CODEX_REQUEST_UNKNOWN_DISPOSITION_V1_CLOSED`,
`CODEX_REQUEST_USER_DECISION_V1_CLOSED`,
`CODEX_PENDING_REQUEST_CROSS_CORE_REATTACH_CLOSED`,
`CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`,
`MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`, `TRUSTED_EXECUTOR_MODEL_V1`,
`CODEX_SANDBOX_HARDENING_DEFERRED`, `REAL_CODING_AGENT_E2E_NOT_RUN`,
`MANUAL_DESKTOP_ACCEPTANCE_PENDING` and
`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`.
