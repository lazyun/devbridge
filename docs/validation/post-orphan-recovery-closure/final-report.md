# Post-Orphan Recovery Acceptance + Git Delivery Closure v46

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/post-orphan-recovery-closure.md)中。

**POST_ORPHAN_RECOVERY_CLOSURE_COMPLETE**

1. **Starting branch / HEAD.** `dev_20260908_1913` / `398a6d2bb71afcef2871a03b605ecca5eb13fdde`; tree `4b43487af7d55d42dcc2808da488308a068b25f2`. Fresh initial working tree was clean. See [baseline](../archive/post-orphan-recovery-closure.md#file-4ae9342c29f0f330). The baseline HEAD and other worktrees remain unchanged.

2. **Schema v46 confirmation.** `DefaultMigrations` remains v44 → v45 → v46; Desktop coding agent configuration remains v2. This repository uses `schema_v45.sql` plus the existing v46 migration, not a separate `schema_v46.sql`.

3. **Fresh P1 confirmation.** On the actual v46 baseline, Acceptance still scanned every historical Run under the Task for UNKNOWN/CONFLICT recovery and unsafe finalization; `commitBusy` did the same across the FeatureArea. Both omitted permanent orphan disposition. The defect was statically reconfirmed before implementation; it was not already closed by the baseline.

4. **v46 overlap.** The committed v46 changes add immutable runtime config to Approval materialization/Execution and widen model reasoning selection. Fresh diff/audit confirmed that orphan recovery, cancellation, finalization and epoch authority contracts were unchanged. The new gates preserve runtime selection, immutable persistence and replay order.

5. **Durable revocation facts.** The exemption binds exact Run, Task, Area, owner kind/ID, original epoch, Thread/Turn, workspace, UNKNOWN boundary and conversation. It requires permanent `UNKNOWN_ORPHANED`, terminal child operation, ended exact conversation, quarantined/detached workspace when non-null, and no old Run claim. Existing immutable schema triggers and `checkRunTx` permanently fence old callbacks. Same-epoch cancellation and null workspace remain legitimate; the observed epoch need not remain current after restart.

6. **Unified predicate.** [runtime_authority.go](../../../internal/storage/sqlite/runtime_authority.go) provides `readRuntimeAuthority`: one durable query returns `Busy` and `RecoveryRequired`. Acceptance, Review fresh decisions, Git commit/submission/capabilities, Direct File admission and workflow guidance consume it. Process-local Direct File grants are checked separately to preserve lock ordering. Blocked guidance also clears actionable Approval/Decision payloads. Safe exact Implementation→Verification claim handoff keeps `START_VERIFICATION` while still blocking Acceptance/Git.

7. **Still blocks.** Active claims and writable Runs (including COLLECTING), unresolved UNKNOWN/CONFLICT, unsafe finalization/boundary without complete orphan proof, pending cancellation and stranded unsafe current context. Direct File grants still exclude relevant writers. Five injected recovery transaction failure windows remain fail-closed. Snapshot drift, lifecycle, unfinished Tasks, pending Git receipts and merge conflicts keep their existing guards.

8. **History no longer blocks.** Exact permanently orphaned operations remain UNKNOWN/UNKNOWN_ORPHANED evidence but no longer count as current unresolved authority. No Run, recovery or finalization history was deleted or rewritten; no quiescence or physical backend death was inferred.

9. **Task Acceptance change.** The existing exact current Task/Execution/revision, phase, terminal outcome and action-specific gates remain. Only the runtime readiness query is shared at Area scope, with a fresh Direct File check. Receipt replay still precedes current readiness/default lookup; same-Area unresolved history from another Task cannot be bypassed.

10. **ACCEPT.** Formal R1 UNKNOWN → restart → explicit orphan recovery → another restart → same-Task R2 → APPROVE → Implementation/Verification → ACCEPT succeeds. Git integration separately proves automatic commit of the verified tree.

11. **REJECT.** An independent recovered-history fixture reaches current Acceptance and cancels the same Task through the original authoritative action.

12. **REQUEST_CHANGES.** Same Task, exactly one new immutable revision and successor Execution. Explicit model/reasoningEffort/codexFastMode and derived service tier survive restart/replay. Authored WorkSpec/VerificationSpec prompt bytes remain exact; runtime config stays outside the semantic revision.

13. **REQUEST_REVERIFICATION.** Same Task and immutable revision, exactly one verification-only successor. The test actually consumes it as a VERIFICATION Run. Frozen next-run config survives restart; repeated requests do not duplicate the successor.

14. **Terminal failed Execution recovery.** After explicit orphan recovery, current guidance exposes lawful disposition actions. REJECT and REQUEST_CHANGES execute; ACCEPT remains unavailable for the tested failed Implementation without acceptable verification. Existing successful-Implementation lineage rules for reverification remain intact.

15. **Automatic Task commit.** Recovered history permits ACCEPT's automatic commit using the original exact verified snapshot and durable receipt. Commit-checkpoint response loss replays without creating another commit.

16. **FeatureArea submission.** Real temporary Git repositories demonstrate successful delivery to selected development, durable merge/checkpoint replay and no duplicate delivery. Existing fast-forward/ordinary merge/conflict behavior is preserved and covered by the full Git package suite.

17. **Cross-Task same-Area.** Task A orphan history does not block Task B Acceptance or auto commit. The initial recovery race retains the existing requirement that all Area Tasks complete before submit: while A is unfinished, submission legitimately returns `TASK_COMMIT_PENDING`; after A also completes and commits, actual submission succeeds. Unrecovered A still blocks B delivery.

18. **Restart/cold resume.** Tests close/reopen actual temporary SQLite stores around recovery, successor decisions, commit and submission receipts. No new in-memory recovered flag or schema field exists.

19. **Late callbacks.** Old epoch/Run callbacks are rejected before changing current Task, Review, workspace or Git result. Tests also prove a caller using the current epoch cannot revive an already orphaned Run; exactly one current claim remains after racing fresh Review admission.

20. **Response loss/replay.** Recovery returns the same receipt after repeat/reopen; Acceptance concurrent replay preserves exact result/config and successor counts; changed payload conflicts. Git checkpoint failures retain durable retry identity and produce no duplicate commit/merge. Dispatch-loss fixtures retain an empty unknown Turn ID rather than fabricating one.

21. **Direct File.** Actual grants block all four fresh Acceptance actions, Review fresh decisions and Git capability/write paths; disabling restores lawful progression. Direct File enable uses the shared durable query and keeps separate pending Git receipt checks. No grant locking is introduced inside that query.

22. **ReviewDepth / Fast Mode.** FAST/DEEP remains the Task review floor, independent from CodexFastMode. Recovered Review APPROVE/REVISE/REJECT, per-model reasoning selection, operation override and immutable next-run config regression pass.

23. **Schema/migration status.** No schema, migration, runtime-epoch or cancellation/finalization implementation changes. Existing fresh DB, previous version→v46, ReviewDepth, Task-owned compatibility and conservative legacy handling tests pass. No v47.

24. **Canonical docs closure.** Fourteen current documentation files corrected; historical closure/validation reports retained. Task precedes Review, Review [0:N] and 1:1 Run, APPROVE creates initial revision on the existing Task, Changes keeps Task, child cancellation/orphan does not automatically terminalize semantic Task, and no current operation means `selectedWork = null`. See [canonical audit](../archive/post-orphan-recovery-closure.md#file-9c71578ece8267c9).

25. **UNKNOWN consumer audit.** [static-audit.txt](../archive/post-orphan-recovery-closure.md#file-aa7da620209d7afa) classifies every matched production occurrence across Go/SQL/JS/HTML as current authority, historical evidence or display/diagnostic. All broad Acceptance/Git/guidance historical-UNKNOWN busy queries now use the shared predicate. Exact live callback/Host Tool/cancellation/usage fences remain unchanged.

26. **Modified files.** Eight implementation/test files, fourteen current documentation files and this independent validation bundle. See [complete list](../archive/post-orphan-recovery-closure.md#file-30db3179bab782dd). Production changes are confined to SQLite progression gates/projections; no adapter or UI implementation was changed.

27. **Focused tests.** [focused-recovery-acceptance.log](../archive/post-orphan-recovery-closure.md#file-8eede4f07c50075d): 16 top-level tests, 51 including subtests; zero failures/skips.

28. **Affected Go packages.** [go-tests.log](../archive/post-orphan-recovery-closure.md#file-48d331f79b055895): 22 packages selected, 18 with tests and 4 with no test files; 830 top-level passes, 1884 including subtests, zero failures, 6 opt-in skips. These counts describe main-module affected packages, not all repository tests.

29. **Race.** [race-tests.log](../archive/post-orphan-recovery-closure.md#file-19dee3c1681fd9f5): 19 top-level tests / 64 including subtests, zero failures. Includes initial recovery vs old callback/new Review/Acceptance/automatic commit/submission, unresolved authority, concurrent receipt replay and transaction rollback windows.

30. **Migration.** [migration-tests.log](../archive/post-orphan-recovery-closure.md#file-4eb036b801ec0f71): 23 top-level passes / 45 including subtests, zero failures.

31. **Runtime config.** [runtime-config-regression.log](../archive/post-orphan-recovery-closure.md#file-16feadb3506c692f): 24 top-level passes / 54 including subtests. [Desktop config regression](../archive/post-orphan-recovery-closure.md#file-72fa8ec9d8b42170): another 15 top-level passes / 38 including subtests in the separate Desktop Go module.

32. **Acceptance/Git regressions.** [acceptance-parity.log](../archive/post-orphan-recovery-closure.md#file-73ff8f12991282cf): 5 top-level passes / 17 including subtests across production adapters. [git-delivery.log](../archive/post-orphan-recovery-closure.md#file-ef488547204a8d59): 8 SQLite delivery/receipt tests / 24 including subtests; the complete internal/git package also passed in the full affected run. The filtered internal/git invocation in this focused log had no matching tests and is not counted as extra coverage.

33. **Node.** [node-tests.log](../archive/post-orphan-recovery-closure.md#file-3ff1cf811edb7ecf): 443 tests, 439 pass, zero fail, 4 opt-in skips. Covers Card/Web and Desktop tests without real host or browser profile access.

34. **go vet.** PASS for the 22 affected main-module packages, no diagnostics. See [go-vet.txt](../archive/post-orphan-recovery-closure.md#file-103914e637a90fc8).

35. **git diff --check.** PASS. New source/test/document files also checked for trailing whitespace; raw validation logs are retained as evidence. See [diff-check.txt](../archive/post-orphan-recovery-closure.md#file-88e58ae554a1bcf6).

36. **Final git status.** Expected uncommitted implementation/docs/evidence only, nothing staged. See [final status](../archive/post-orphan-recovery-closure.md#file-c95ef8e79fa3f9b5). HEAD remains `398a6d2bb71afcef2871a03b605ecca5eb13fdde`; existing other worktree heads remain unchanged. The working tree is intentionally not clean after implementation.

37. **SCOPE_CONFLICT.** None. Existing v46 durable facts are sufficient; no schema exception is needed.

38. **Task-owned v46.** Preserved: semantic Task owns Review/Execution progression; four Acceptance action meanings, immutable revisions and exactly-one current claim remain authoritative.

39. **Prompt Neutrality.** [prompt-byte-identity.log](../archive/post-orphan-recovery-closure.md#file-e1409af14e7a4bf0): 13 top-level passes / 61 including subtests. Exact instruction bytes/SHA and separate output schema remain unchanged.

40. **Runtime Config/Card/Worktree baseline.** Existing runtime envelope, single-current Card decision consolidation and worktree inventory implementation retained. Their affected Go/Node regressions pass. Approval projection suppression now agrees with the same current-authority write gate.

41. **Direct File/Retire/Card recall.** Preserved; no redesign or production UI edits. Same-Area writable exclusion and retirement/cancellation regression pass. UNKNOWN_ORPHANED still does not imply physical backend death.

42. **Production recall.** `EXTERNAL_INTEGRATION_BLOCKED` / `HOST_INTEGRATION_PENDING` unchanged. No production parent lifecycle, visibility authority or native recall work was attempted.

43. **Delivery boundary.** No repository commit, push, tag or release; no other worktree mutation, external network, real Codex, live DB, user device or browser profile access. Temporary Git fixture commits are test-local only. Tests ran against this uncommitted working-tree implementation based on `398a6d2bb71afcef2871a03b605ecca5eb13fdde`, not against a newly created commit.

Validation identity and limits

- Final test commands, environment and counts are recorded in [validation-manifest.md](validation-manifest.md) and [test-summary.json](../archive/post-orphan-recovery-closure.md#file-612d06b9769cde10). [tested-source-sha256.json](../archive/post-orphan-recovery-closure.md#file-7ac1b50666f5ce8b) identifies the eight changed source/test files at validation completion.
- Six Go skips are the existing real-review smoke, four candidate/runtime opt-ins, and real authorization-ceiling smoke. Four Node skips require external orphan/Card candidate snapshots. Skips are not counted as passes or production integration evidence.
- The initial sandbox invocation could not bind loopback MCP servers and also addressed the separate Desktop module from the root module. Its output is retained only under `diagnostics/initial-sandbox-go-tests.log`. The final affected run uses the authorized temporary loopback environment; Desktop tests ran in their own module. All final validation gates listed above passed.
- Local temporary durable fixtures and fake backend dispatch evidence validate Bridge authority behavior. They do not prove an orphan backend physically died or establish production recall-host integration.
