# From unsupported worker continuation to an explicitly authorized successor

## Result

On 2026-11-01, clean Core, GPT MCP and Local MCP binaries from
`60168fa616acfdff1534bf4f573d63c996cc7f97` exercised a fresh disposable Task in
one private DataRoot. The bounded assistant-authored acceptance passed
**73/73 retained-evidence assertions**. No production defect was found.

The important outcome is not that the old worker resumed. Its original receipt
continued to read OUTCOME_UNKNOWN, with neither executable input nor a borrowed
result. Instead, explicit cancellation, a same-Task supplemental Review, a
separate approval and a separate START established a **new workflow**. The new
worker completed the original goal using the old worker's preserved tests.

This establishes one supported user recovery route when Core offers the exact
REQUEST_TASK_REVIEW capability. It does not make re-review a universal
prerequisite for interrupted work or replace other eligible native Execution
recovery routes. It is not permission to cancel or revise user work implicitly.

## Actual isolated protocol journey

The fixture's `format_tag(value)` initially returned its argument. The goal was
to reject non-strings with TypeError, strip surrounding whitespace and return
`A:` plus Unicode `upper()`, preserving internal whitespace. Only `tags.py` and
`test_tags.py` could initially change; README and the source checkout were fixed.

1. An assistant claimed the initial read-only Review, read all three files
   through Local MCP and submitted an evidence-grounded DIRECT_FILE proposal
   with RUN verification. Explicit fixture-user choices approved the plan with
   a test-only USER_WAIVED/NONE selection, then separately confirmed START.
2. The first file worker claimed once, wrote only its actual tests and paused.
   Independent shell execution in the managed worktree recorded **15 tests /
   21 failure events, exit 1**. The implementation still had its original bytes.
3. Core, both adapters and the transport were gracefully stopped and restarted
   after every dispatched response had been retained. The old worker's exact
   receipt read UNKNOWN; the file grant reported RECOVERY_REQUIRED with
   FILE_WORKER_CONTINUATION_UNSUPPORTED.
4. A same-worker RECOVER prepare was rejected with that exact domain code.
   A supplemental Review prepare before cancellation was also rejected:
   failure code INVALID_ARGUMENT, message DIRECT_FILE_EXPLICIT_CANCEL_REQUIRED.
   Neither refusal created authority or changed any saved business-table row or
   physical file snapshot. These were business refusals, not schema errors.
5. A separate explicit fixture-user cancellation closed the old file grant.
   It did not declare the Task completed, remove partial files, or prove that
   an external worker process had terminated. The original Task remained.
6. GPT read the now-available REQUEST_TASK_REVIEW capability and copied its
   complete five-field expected target, including empty IDs as assertions of
   absence. A REQUEST_SUPPLEMENTAL_REVIEW prepare alone started no Review/Run.
   One subsequent explicit confirmation performed REQUEST_REVIEW and
   START_REVIEW on the original Task. The Review/Run identities were new;
   workspace, member and physical working tree were not replaced or reset.
7. A different assistant claimed the new Review and actually read the preserved
   **15-test file and its exact digest** through MCP, rather than reviewing the
   source baseline's placeholder test. It assessed those tests and proposed
   changing only `tags.py`, preserving tests and README. Its report proposed RUN
   verification and was read back exactly; the reviewer did not approve itself.
8. A distinct fixture-user approval explicitly selected the isolated NONE
   waiver and froze the new definition and approval decision. Approval did not
   create a new file grant or modify partial files. A pending separate START
   also granted nothing. Its explicit confirmation created the new workflow and
   file grant; a subsequent new worker claim first delivered executable input.
   The old authorization row remained CLOSED and unchanged.
9. The successor reused the same Task, FeatureArea, member, workspace, physical
   root and worktree. Its generation was **1**: generation is not a global
   successor counter. A new claim request ID was used because worker-claim
   identity is Session-scoped. Old and new workflow identities remain decisive.
10. Three explicit negative calls were refused without changing any file or
    saved business-table row: the old worker claim combined with the new
    workflow, a new-worker attempt to edit now-out-of-scope tests, and a fresh
    mutation using the old CLOSED grant. Current digests were supplied, so a
    stale-digest error could not hide a missing authorization check.
11. The new assistant changed only `tags.py`. Independent execution of the
    **original preserved 15 tests passed, exit 0**. Tests/README inode, mtime,
    size and bytes remained unchanged, as did source-checkout bytes/status and
    all Git HEADs.
    Old and new mutations deliberately shared the request text
    `generation-shared-mutation` but retained different workflow, payload and
    result identities. The old write targeted tests; the new write targeted
    the implementation.
12. The successor submitted one formal result: NOT_RUN, `accepted=false`, no
    Execution or verification Run. Independent shell tests are not Core Runs.
    An explicitly requested exact replay of the **known completed old mutation**
    returned its original result without changing files or ledger rows. It was
    not an automatic retry of an uncertain write. Old worker get/claim calls
    still exposed only the original UNKNOWN receipt, with no result/payload.
13. After another graceful full restart, both MCP adapters read the same exact
    new result. Both historical Reviews and the old approved definition stayed
    unchanged; the current definition separately recorded the narrower new
    scope. Cold old-worker reads could not borrow the successor's result.

Final state: **one original Task, ACTIVE / ACCEPTANCE_PENDING**; two CLOSED file
workflow authorizations; two SCOPE_REVIEW Runs; one unaccepted NOT_RUN file
result; no Execution. No Task acceptance, delivery commit, merge, deployment or
native launch occurred. All **12 owned process instances across three
runtime generations** joined with exit 0; the native-launch tripwire stayed
absent.

## Repeatable regression and stronger same-path replay oracle

`internal/controlapi/file_worker_successor_recovery_test.go` uses genuine typed
Core admission for the initial fixture, then real GPT MCP confirmation commands
and Local MCP SDK calls for the recovery/successor sequence. It actually closes
and reopens Core/SQLite; it does not synthesize a recovered row.

The callers and reports in this repository regression are deterministic test
fixtures, unlike the independent assistant-authored external run. It checks:

- A same-worker recovery business refusal leaves the original authority intact.
- Explicit cancellation precedes supplemental Review admission. The new Review
  reads actual dirty partial bytes/digest from the same worktree/workspace.
- Independent approval freezes a different revision and decision without
  creating authority. A separately confirmed START creates a new workflow;
  its later one-shot worker claim first delivers executable input.
- A mixed old claim with a current digest is refused with the actual business
  code and no durable-state changes. Old worker reads remain UNKNOWN, with
  neither executionPayload nor resultReceipt, before and after the new result.
- Both workflows can use `partial-write` without sharing a mutation receipt.
- **Both mutations target the same path.** After the successor changes its
  contents, exact replay of the old completed mutation must return historical
  proof without restoring old bytes: inode, mtime, size and digest remain the
  successor's. Source checkouts, unrelated members and all HEADs are preserved.

The same-path assertion is stronger than the external run's different-path
old-tests/new-implementation replay. They are complementary evidence, not the
same experiment. An overlay negative control deliberately performs an extra
actual authorized write restoring the old partial bytes; the physical-state
assertion must fail. That is an oracle control, not a reproduced product bug.

```sh
go test ./internal/controlapi \
  -run '^TestFileWorkerExplicitSuccessorAfterRestart$' -count=3 -v
go test -race ./internal/controlapi \
  -run '^TestFileWorkerExplicitSuccessorAfterRestart$' -count=1 -v
go vet ./internal/controlapi
```

The final frozen source passed focused-times-three (18.585s), race (170.78s test /
172.975s package), vet and the expected failing physical-overwrite control.
Root `go build ./...` passed. A combined selection covering this successor,
supplemental-depth and historical-partial guidance, existing HTTP restart, and
multi-Task isolation passed in 12.664s. Controlapi vet passed again. These focused
checks are not a full repository-suite result.

## Evidence and qualifications

The private `devbridge-worker-successor-evidence` bundle retains the 73-check
report/verifier, **95 unique dispatch records** (92 tools/call attempts, two
tools/list calls and one public HTTP registration), exact assistant-authored
inputs and responses, old/new grant identities, physical and ledger snapshots,
independent red/green output, clean binary build metadata and joined-process
records. `devbridge-worker-successor-regression-evidence` retains frozen source,
test logs and the expected failing overlay control. Do not publish these
runtime-state bundles wholesale.

The external run's transport serializes dispatch. This is a same-Task recovery
lifecycle test, not concurrent-request or multi-principal security qualification.
Worker/Session labels and SDK connections do not prove independent authenticated
identities; actual assistant authorship also relies on retained collaboration
history. Fixture-user confirmations are not evidence of a real human UI action
or authorization for a user's project.

The old completed file mutation was known; it was the unfinished worker delivery
that remained UNKNOWN. Neither external run nor repository regression injects
SIGKILL, socket loss or an unresolved physical-write journal window here. Those
are separate gates. No installed production dot, native agent, model/provider,
OS sandbox or host runtime is qualified.

Setup errors remain documented rather than counted as product failures. An
external verifier initially used the wrong retained response filename and
stopped before dispatch; correcting the local lookup did not retry a request.
Early regression drafts exposed differences between Go DTO serialization and
public MCP schemas, and an envelope decode into a nonempty fixture retained an
old ReviewTarget. The final test copies the new target from the actual response
and asserts its Review/Run identities; no production code was changed to hide
these test-author mistakes. Earlier frozen runs are retained separately from
final-source results.

The repository regression is rerunnable from source. The 73 historical external
assertions require their private evidence bundle and do not mean a repository
clone can replay the assistants' collaboration. The full repository suite was
not run for this increment.
