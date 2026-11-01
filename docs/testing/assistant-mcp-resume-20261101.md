# Assistant-authored local MCP: confirmation and cold-read continuation

## Result and scope

On 2026-11-01, production Core, GPT MCP and Local MCP binaries built from
`115a58bf4d241c622b04556c655c0e12b5c0f548` were exercised in a fresh private
DataRoot with a disposable Git repository. The parent assistant authored the
GPT-side requests; a separate assistant worker authored its review, tests,
implementation and final report after reading actual Local MCP responses.
The transport did not contain engineering callbacks or prewritten model replies.

The bounded result is **passed**: 45 post-run evidence assertions, plus a real
external Python red/green check. This is new assistant-driven protocol evidence,
not a rerun or replacement of the 22 deterministic Core-simulation scenarios.
No production code was changed during this acceptance. At the time of the run,
the new test-harness code and documentation were local and uncommitted;
publishing those artifacts later does not change the acceptance scope.

It does **not** qualify an installed production dot, persistent host worker,
remote ChatGPT connector, tunnel, actual confirmation UI, native agent sandbox,
provider/model availability, or a complete repository test suite. MCP actor and
worker names are correlation labels, not proof of a model or process identity;
assistant authorship also depends on the retained collaboration transcript.

## Exercised workflow

### 1. Pending Task creation survives cold restart

A create-only Task confirmation was prepared, but not consumed. Bounded Task
lists contained no Tasks both before and after all three owned services were
stopped and restarted. Reading the same `aiSessionId` preserved its binding;
`confirmation_get` returned the original PENDING intent, version, content hash
and content. Only a later, explicitly labeled fixture-user confirmation created
one Task. It remained `ACTIVE / REVIEW_REQUIRED`, with no Review or Run.

This was a **single-repository admission probe**. A subsequent attempt to use a
v2 `repositoryTargets` Review against that scalar-repository Task was rejected
at PREPARE with `NOT_FOUND`, without an intent or Run. Source inspection showed
the scalar Task and v2 workset inputs did not match. The probe was preserved,
not rewritten or claimed to have continued into the later implementation.

### 2. Independent v2 Task and actual assistant-authored Review

A separately named FeatureArea and repository-neutral Task were explicitly
created for a v2 workset containing the same one disposable repository. Its
UPPER Review was separately prepared and confirmed. The independent assistant
worker claimed its exact target, read `labels.py`, `test_labels.py` and
`README.md` through Local MCP, and submitted a source-grounded report using the
actual evidence IDs and digests.

The proposal was DIRECT_FILE, writable only at `labels.py` and `test_labels.py`,
with RUN verification (`python3 -B -m unittest -v`). The formal report read back
unchanged; its worker receipt became RESULT_RECORDED with no execution payload.
The Task reached REVIEW_APPROVAL_PENDING, with no Execution.

### 3. Withdrawal, explicit approval, and restart before start

The parent prepared an approve-only intent. A separately labeled fixture-user
call discarded it, yielding SUPERSEDED. A late commit of that exact old card was
refused with `CONFIRMATION_INTENT_NOT_CURRENT`; the Task stayed at its original
review-approval checkpoint with no file grant or Execution.

After a fresh authoritative Task-generation read, a distinct approve-only
proposal was prepared and explicitly confirmed. Its selection contained an
explicit **ISOLATED TEST FIXTURE ONLY / USER_WAIVED** command-verification
waiver. The frozen definition became NONE; the original Review's RUN proposal
was not edited. The Task reached IMPLEMENTATION_READY, still without file
implementation authority or an Execution.

A separate START_DIRECT_FILE intent was prepared. All three services were again
gracefully restarted while it was PENDING. The original intent was still
PENDING, the Task was still IMPLEMENTATION_READY, the file grant was unavailable,
and the historical Review result was identical. Only a subsequent fixture-user
confirmation enabled the exact file grant. No panel was reopened on reconnect;
the presentation response never established actual host visibility.

### 4. Test-first implementation through Local MCP

The task was a small Python `normalize_labels(values)` utility:

- Strip surrounding whitespace and omit empty cleaned strings.
- Deduplicate by Unicode `casefold`, preserving first cleaned spelling and order.
- Preserve internal whitespace and caller input; reject non-string elements.
- Change only `labels.py` and `test_labels.py`; preserve README bytes.

The worker claimed the new exact file target and wrote only the test file first,
then paused. The parent independently ran the test command in the actual
Core-owned worktree: **16 tests, 20 failure events including subtests, exit 1**.
The implementation still had its original digest; only the tests had changed.

After this observation, the worker changed `labels.py` through MCP, read back
both files and README, and paused again. The independent observer then measured
**16 tests passing, exit 0**, plus `git diff --check` exit 0. Source-checkout
bytes/status, both Git HEADs and README bytes remained unchanged; only the two
approved paths differed in the managed worktree. The same checks passed again
after the final cold restart.

These shell observations are **not Core verification Runs**. The final report
explicitly says so; neither the reviewer nor worker approved its own work.

### 5. Formal result, closed authority, and truthful continuation boundary

The worker submitted exactly one formal file result and read it back:

- Exact original result and Task identities were preserved in the private
  evidence, rather than creating replacements on reconnect.
- `verificationStatus=NOT_RUN`, `accepted=false`.
- No Execution or verification Run; the only Run was the original SCOPE_REVIEW.
- File grant CLOSED / disabled; Task ACTIVE / ACCEPTANCE_PENDING.

A schema-valid new file write after closure was refused with
`DIRECT_FILE_CAPABILITY_NOT_ENABLED`, without changing bytes. A schema-valid
CONTINUE_TASK prepare without an Execution checkpoint was refused with
`REJECTED_NOT_PREPARED / INVALID_ARGUMENT` ("Exact Task verification choice
required"). This is a **negative boundary check**, not successful
DEFER_TASK/CONTINUE_TASK or native interrupted-execution recovery.

After one more graceful service restart, GPT and Local MCP each read the exact
same formal result. Replaying the original file-worker claim returned only
RESULT_RECORDED, not another execution payload. The original Review remained
identical. Both Task projections pointed to READ_DIRECT_FILE_RESULT, not an
invented Execution acceptance decision. Each fixture FeatureArea contained
exactly one Task; the earlier admission probe still had zero Runs.

No Task acceptance, Git delivery commit, feature merge, deployment, native
backend launch or Core-launched online model-provider request occurred. The native-launch
tripwire remained absent. The final owned Core, two adapters and transport all
joined with exit 0.

## Evidence and limitations

The private local bundle `devbridge-mcp-protocol-evidence` contains:

- `report.json` and `verify_evidence.py`: 45 evidence checks, all passing.
- `mcp.jsonl`, `requests/`, `responses/`, and per-call JSON snapshots.
- **80 protocol/registration records**: 77 MCP tools/call attempts, two
  tools/list calls and one public HTTP project registration. Actor labels are
  36 GPT, 33 Agent and 11 fixture-user records, not model-token accounting.
- `observer-before.json`, `observer-red.json`, `observer-green.json` and
  `observer-final.json`: command output, exit codes, hashes, Git observations.
- Exact authored Review, file-write and final-result inputs; cold readbacks;
  source revision, binary hashes, owned process logs and isolated data.
- Both ad hoc transport source/binary revisions. The last revision adds only
  graceful transport shutdown. Production binaries were unchanged.

An independent read-only agent audited the first 78 records and found no
blocking inconsistency; two final bounded Task-list reads brought the total to
80. It did not inspect the database or make MCP calls. Its caveat is important:
the mailbox is an acceptance aid, **not a qualified unknown-outcome runtime**.
No crash was injected between MCP dispatch and mailbox response-file persistence;
that window could cause a restarted driver to resend an unrecorded request.
This run made no such restart, no response-loss injection and no automatic
business retry. Its three workflow restarts occurred only after all dispatched
requests had retained responses, and were graceful—not crash tests.

Ad hoc setup errors are retained, not counted as product failures: a cross-exec
localhost request failed before MCP dispatch because shells had isolated network
namespaces; an empty-Core startup retry initially read a stale URL from an
append-mode log; the first panel call omitted its required presentation request
ID. Shared-file request transport, unique log-generation names and the actual
schema resolved these setup mistakes. The first post-run checker also compared
the outer report record to the inner authored report; its failed output is kept
as `report-checker-initial.json`, and the corrected nested comparison passed.

The evidence bundle is local and includes disposable state; do not publish it
wholesale. No claim is made that an actual person clicked a Card, that the
fixture waiver authorizes a user project, or that these results establish merge
readiness. Positive native continuation remains the separate
[test-only continuation gate](simulated-continuation-gate.md) / future native
qualification scope; installed host integration remains unverified.

The subsequent [transport-uncertainty acceptance](mcp-transport-unknown-outcome.md)
reproduces and closes that particular ad hoc mailbox redispatch window; it does
not retroactively change the scope or evidence of this run.
