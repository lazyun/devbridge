# Assistant-authored GPT → Agent through local MCP

## What was actually exercised

On 2026-10-31, at feature head `e135904e704a9351c7cec5627ed77dde9f87d7a4`,
assistant-authored requests exercised one isolated real Core and the production
GPT and Local MCP HTTP adapters on loopback in the same cloud computer. This
route needs neither a remote ChatGPT connector nor a tunnel.

Unlike the [consumer composition gate](worker-client-composition.md), the review,
implementation and completion were authored by the assistant after observing
actual MCP responses, **not supplied by a deterministic test callback**. A retained
SDK session driver only transported the explicitly authored requests. It was an
ad hoc acceptance aid, not a shipped host runtime or a qualified launcher.

The bounded feature was Python `greeting(name)`: strip surrounding whitespace,
use `world` for an empty stripped string, preserve Unicode and internal
whitespace, and change only `greeting.py` and `test_greeting.py` in a disposable
repository. Non-string inputs were excluded.

1. GPT created the Task and separately requested UPPER review through MCP.
2. Agent claimed the exact review, listed/read its admitted member, and submitted
   a source-grounded DIRECT_FILE proposal with real evidence IDs/digests and RUN
   verification. The original report and full-input hash survived unchanged.
3. Separately labeled **fixture-user** confirmations approved the exact work with
   an explicit USER_WAIVED/NONE selection, then independently started DIRECT_FILE.
   These were authorized test decisions, not a claim that a person clicked a UI,
   the Agent could approve itself, or a production verification waiver existed.
4. Agent claimed the exact file grant, read before writing, wrote tests first,
   then changed the function using scoped MCP file APIs and stable request IDs.
5. An independent read-only local observer ran `python3 -B -m unittest -v`:
   **9 tests / 7 failures before the function change; 9 tests / 0 failures after**.
   The observer checked the actual owned worktree, HEAD, source hashes and command
   exit codes. These observations are **not a Core verification Run**.
6. Agent read back both files and the unchanged README, then submitted its actual
   formal result. GPT read the final workspace, historical review and Run inventory.

The recorded result was `accepted=false`, `verificationStatus=NOT_RUN`, with no
Execution or verification Run. The file grant was CLOSED; Task remained
ACTIVE / ACCEPTANCE_PENDING. Only the original SCOPE_REVIEW Run existed.
Original repository bytes/HEAD and managed HEAD were unchanged. No acceptance,
Git delivery, merge, deployment, external provider/model call or native backend
launch occurred. A native-launch tripwire was not triggered.

The isolated runtime's owned Core, two adapters and driver were gracefully stopped
with exit 0. Its transcript, per-call responses, red/green observations, artifact
hashes, formal identities and manifest were retained in the local evidence bundle
`assistant-mcp-20261031`; the ad hoc driver/runtime databases are not published as
product code. One rejected, unconsumed branch-configuration intent was correctly
reprepared. One overlong terminal JSON input failed before MCP dispatch; the same
request was transmitted in bounded chunks, with exactly one actual report submit.
Neither event is represented as a process crash or a response-loss recovery.

## Defect found and narrow correction

The actual formal result response and workspace incorrectly suggested assessing
an Execution that did not exist. The compact envelope even emitted the malformed
`TASK_ACCEPTANCE:` decision with an empty Execution ID.

The correction changes read projections only:

- An acceptance-pending Task without an Execution gets `READ_TASK_EVIDENCE`,
  never a fabricated Execution acceptance decision.
- Only a matching current v2 DIRECT_FILE definition with explicit USER_WAIVED
  NONE verification, CLOSED grant and formal NOT_RUN result gets
  `READ_DIRECT_FILE_RESULT`. Guidance names the exact Task, revision and workflow
  for `file_implementation_result_get`, and keeps acceptance a separate explicit
  user decision. It creates no result, grant, decision or acceptance authority.
- Existing recovery, active-owner, retirement and Git-finalization blockers take
  precedence. A RUN handoff with a real Execution retains its real acceptance
  decision. Historical v1 data is not upgraded or rewritten; its existing explicit
  file acceptance path remains intact.

The public regression uses genuine admitted Core state, explicit fixture-user
confirmations and real file operations through **both** production MCP adapters.
It checks submission, exact readback, workspace/TaskView/FeatureArea projection,
historical review conservation, closed authority, zero automatic acceptance or
Git effects, and unchanged durable rows during reads. Separate storage-unit tests
use clearly labeled minimal synthetic histories for mismatches and overlay order;
those rows are not evidence of admission or execution.

```sh
go test ./internal/controlapi -run 'TestDirectFileNoneAcceptanceGuidanceThroughBothMCPAdapters|TestFileWorkerClientRealCoreVerificationHandoff|TestMultiRepositoryDirectG8V1FileResultCommitMergeCompatibility' -count=1
go test ./internal/storage/sqlite -run '^TestDirectFileResultGuidance' -count=1
```

The RUN compatibility test uses the existing **test-only protocol adapter** and
real bounded local check commands, not a live model. The v1 compatibility test
continues its pre-existing accept/commit/merge lifecycle only inside a disposable
fixture; this does not accept or merge the manual acceptance Task or feature branch.

This establishes assistant-driven engineering over local MCP, not an installed
production dot worker, native isolation qualification, live provider integration,
confirmation UI qualification, or a passing full-root test suite.

## Qualification of the projection correction

- The new public regression against the two **old** production source files
  (Go overlay, no branch rewrites) failed on both adapters at the malformed
  `TASK_ACCEPTANCE:` decision. This reproduces the actual manual-run defect.
- Final frozen source passed the expanded Core/client regression selection in
  **35.122s**, the NONE/RUN/v1 public race selection in **534.059s**, and focused
  SQLite authority/projection tests in **5.148s**, including 25 new negative and
  overlay cases. Root build and vet passed. Six Go source hashes were stable
  across the final runs; independent review approved source, tests and evidence.
- Broader package runs were **not green**. Both attempts hit the 10-minute
  SQLite and Local MCP package limits. Local MCP additionally recorded HTTP
  response timeouts in GPT/REJECT acceptance and approved-Task restart readback.
  These failures are retained and are not attributed to a cause without proof.
  The two HTTP-timeout cases passed in a later isolated selection (**10.144s**);
  that recheck neither erases the failed broad runs nor proves their cause.
- The MCP server package's architecture guard rejected five SDK imports in the
  already-published outbound `upperworker`/`fileworker` clients. Those client
  files and the guard were unchanged by this projection correction. Correctly
  classifying outbound clients while protecting reverse dependency direction is
  a separate increment; this document does not count that guard as passing.
- An initial root build also found the acceptance author's old-source overlay
  copies inside the module. Moving only those owned diagnostic copies outside
  the module removed that workspace artifact; final build/vet passed without
  changing product code for it. No failing log was substituted for a pass.

The separately reviewed [MCP dependency-boundary correction](../architecture/mcp-dependency-boundary.md)
addresses that outbound-client guard failure without changing this projection
fix or treating the timed-out package suites as passing.
