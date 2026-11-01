# Assistant-authored multi-Task MCP isolation across restart

## Result and scope

On 2026-11-01, clean production Core, GPT MCP and Local MCP binaries from
`cf442373ec35c9c6a94ed022803de7b5f30ee25e` exercised two disposable Tasks in one
private DataRoot. Both used the same Project and source repository, but different
FeatureAreas, admitted members and managed worktrees. The bounded acceptance
passed **64/64 retained-evidence assertions**; no production defect was found.

The coordinating assistant authored requests through two GPT-side business
Sessions. Two separate assistant workers read real Local MCP responses, authored
independent Reviews and wrote task-specific tests; worker B also implemented and
reported its result. Their engineering output was not a deterministic callback.
Four SDK connections used the real HTTP adapters. The guarded mailbox transport
made no engineering or confirmation choices and never redispatched a label.

This is **interleaved lifecycle isolation**, not simultaneous-request concurrency
or multi-principal security qualification: transport dispatch is serialized, and
SDK connections / worker names are not independently authenticated identities.
The tested boundary is the application's exact Session/Task/workflow/member
binding. Assistant authorship also relies on the retained collaboration history.

All confirmations were explicitly labeled **fixture-user** decisions. Reviews
proposed RUN verification; separate approval confirmations explicitly selected
USER_WAIVED/NONE for this isolated test only. They do not authorize real user
work. The run does not qualify a production dot, real confirmation UI, native
model/backend, host worker launcher, remote connector, tunnel or OS sandbox.

## Two incompatible requirements, one source baseline

The source fixture contains `tags.py`, a placeholder `test_tags.py`, and a
README that must never change. The baseline `format_tag(value)` simply returns
its argument. Both Tasks permit changes only to the two Python files:

- **A:** reject non-strings with TypeError; strip surrounding whitespace;
  return `A:` plus Python Unicode `upper()`. Preserve internal whitespace.
- **B:** the same type and whitespace rules, but return `B:` plus Unicode
  `casefold()`. These outputs deliberately cannot share an implementation.

Different FeatureAreas are essential. Direct File ownership is exclusive within
one FeatureArea; this acceptance does not assume separate worktrees for two
Tasks in the same FeatureArea.

## What happened over the actual protocol

1. Each assistant claimed its exact Review, read all three source files through
   MCP and submitted a source-grounded report with actual evidence IDs/digests.
   Both reports read back unchanged. Neither assistant approved its own work.
2. Fixture-user confirmations approved both plans. A's separate START was
   confirmed; B's START remained PENDING. Worker A claimed its file target,
   wrote only its tests, read back the files, and paused. The independent shell
   observer measured **15 tests / 21 failure events, exit 1** in A's actual
   managed worktree. A's implementation still had its baseline bytes.
3. Core, both adapters and the transport were gracefully stopped and restarted.
   Every dispatched response had already been retained. A's original worker
   read back OUTCOME_UNKNOWN with no executable payload; its grant projected
   RECOVERY_REQUIRED / FILE_WORKER_CONTINUATION_UNSUPPORTED. This is a truthful
   unsupported continuation boundary, **not successful worker resumption**.
4. B's original START intent retained its identity, version, content hash and
   content, remained PENDING, and granted no authority. A deliberately mixed
   Session confirmation was rejected with CONFIRMATION_FACTS_UNAVAILABLE and
   did not consume B's intent. A fresh write using A's stale authority and a
   valid current digest was also rejected. Both trees and saved business tables
   remained identical.
5. Only a subsequent explicit fixture-user confirmation consumed B's original
   START. B received a new-runtime grant and a distinct worktree. After worker B
   claimed and read its files, eight mixed-target negative calls were rejected:
   six writes swapping A's worker claim, member or Session into B's operation
   (both fresh and already-used-in-A request IDs), plus two mixed worker reads.
   File snapshots and every saved business-table row stayed unchanged.
6. B wrote its own tests using the exact business request text
   `shared-test-first-write`, already used by A. The two workflows retained
   distinct COMPLETED mutation receipts, payload hashes and results. Independent
   tests measured **14 tests / 20 failure events, exit 1**. After B implemented
   its actual requirement through MCP, the same command measured **14 passing,
   exit 0**. A remained byte-for-byte unchanged and still failed its 15 tests.
7. B submitted exactly one formal result and read it back: NOT_RUN,
   `accepted=false`, no Execution or verification Run. The worker became
   RESULT_RECORDED without another executable payload. Shell red/green evidence
   is not a Core verification Run.
8. A's explicit cancellation intent was prepared but left PENDING. After a
   second graceful full restart, its exact intent still read PENDING; A's worker
   remained UNKNOWN, while B's original result read back identically through
   both adapters. A's exact no-result target and a mixed-Session B-result read
   could not borrow B's result.
9. Explicitly consuming A's original cancellation intent closed only A's file
   authority. It preserved A's partial files and left B's result, authority,
   members, worker receipt, mutations, Task and action head unchanged.

Final state is intentionally not “both Tasks completed”: A is
**ACTIVE / IMPLEMENTING**, with CLOSED authority, preserved partial tests and no
formal result. B is **ACTIVE / ACCEPTANCE_PENDING**, with CLOSED authority and
its original unaccepted NOT_RUN result. No Task acceptance, delivery commit,
merge, deployment or native launch occurred. Cancellation does not prove that
an external worker process has terminated.

All source bytes/status, README bytes, and Git HEADs remained unchanged. Only A's
test file and B's two approved Python files changed in their respective managed
worktrees. Across three generations, all **12 owned process instances** joined
with exit 0. The native-launch tripwire stayed absent.

## Repeatable repository regression

`internal/controlapi/file_worker_multitask_isolation_test.go` complements the
assistant run with deterministic callers and genuine typed Core admission:

- One in-process Core/SQLite, two FeatureAreas/Tasks, two shared source repos,
  distinct managed roots, and two actual Local MCP HTTP/SDK sessions.
- Both Tasks deliberately use the **same business Session**. A separate real
  Session is reserved for negative calls. This catches a faulty implementation
  that scopes mutation replay by Session rather than workflow; the external
  assistant run used two different business Sessions.
- Same-named writes create one distinct COMPLETED receipt per workflow.
- Twelve mixed worker/member/Session writes are rejected. Fresh attempts use
  the current digest so stale-digest rejection cannot hide a missing identity
  gate. Two mixed-Task worker reads are also rejected.
- Separate explicit exact replays preserve each result and all four managed
  members' inode, mtime, size and digest. Source trees, untouched members and
  all HEADs stay unchanged. No result, acceptance or Execution is created.

```sh
go test ./internal/controlapi \
  -run '^TestFileWorkerRealCoreMultiTaskIsolation$' -count=3 -v
go test -race ./internal/controlapi \
  -run '^TestFileWorkerRealCoreMultiTaskIsolation$' -count=1 -v
go vet ./internal/controlapi
```

Final frozen source passed focused-times-three (9.583s), race (149.92s test /
151.956s package) and vet. A Go-overlay negative control deliberately replaced
B's write with A's original request; it failed at the distinct-result assertion,
not at compilation. Earlier two-Session runs and an attempted same-claim-ID
collision are retained separately, not substituted for the final results.

Root `go build ./...` passed. A combined selection of this regression, the real
TCP-response-loss regression and all four existing restart-projection cases
passed in 8.752s; controlapi vet also passed again. These are focused checks,
not a full repository-suite result.

Request IDs do **not** share one universal namespace: file writes are keyed by
workflow + request ID; worker claims have a callerScope + Session + request ID
uniqueness constraint. Result submissions and confirmation commits also have
Session-scoped request identities. Reusing a claim ID across same-Session Tasks
is therefore not a valid isolation-positive test.

## Retained evidence and limits

The private `devbridge-multitask-restart-evidence` bundle retains the 64-check
verifier/report, **125 unique dispatch records** (120 tools/call attempts, four
tools/list calls and one public HTTP registration), exact authored payloads,
responses, guarded journal, independent file/ledger snapshots, red/green output,
confirmation identities, clean binary build metadata and process exit records.
The separate `devbridge-multitask-regression-evidence` bundle holds frozen source,
focused/race/vet logs and the expected failing overlay control. These private
bundles contain disposable runtime state and are not published wholesale.

Acceptance-author mistakes are retained: initial local envelope lookups and an
observer table name were corrected from saved evidence without redispatch;
a cold session-initialize call supplied mutually exclusive fields and was
schema-refused before a corrected read; two worker payload schema checks failed
locally before dispatch. The helper binary was built outside the product checkout
with VCS stamping disabled after a metadata-lookup failure; all three product binaries retain clean base
revision metadata. None of these setup issues is labeled a product defect.

The repository regression is independently rerunnable from repository
source. The 64 historical external evidence assertions require their private
bundle and are not a claim that cloning the repository reruns assistant
collaboration. This increment injects no SIGKILL, response loss or unknown
mailbox window, does not prove positive native continuation, and does not claim
a full repository-suite pass.
