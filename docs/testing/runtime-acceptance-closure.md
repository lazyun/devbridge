> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# Final runtime acceptance fixtures

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

The focused live fixture supplements REAL-E2E-10. It requires the pinned real
Codex 0.149.0 executable and production Bridge/MCP binaries built from the
commit being accepted. It creates a disposable repository, Project, DataRoot,
FeatureArea, and branch selection. It never opens the production DataRoot or
the Dev Bridge repository as an execution target.

Run from the final checkout after building its final binaries:

```sh
DEVBRIDGE_REAL_RUNTIME_ACCEPTANCE=1 \
DEVBRIDGE_REAL_CANCEL_ACCEPTANCE=1 \
DEVBRIDGE_REAL_EXPECTED_COMMIT=the_verified_final_main_sha \
DEVBRIDGE_REAL_E2E_BRIDGE=/absolute/final/build/bin/devbridge \
DEVBRIDGE_REAL_E2E_MCP=/absolute/final/build/bin/devbridge-mcp \
DEVBRIDGE_REAL_E2E_CODEX=/absolute/path/to/codex \
DEVBRIDGE_REAL_ACCEPTANCE_ROOT=/private/tmp/devbridge-final-runtime-proof \
go test -count=1 -timeout 15m ./test/real_e2e/harness \
  -run '^TestRealCodexReadOnlyToWriteSameConversation$' -v
```

The chosen fixture root must be fresh. The fixture is retained for evidence
and its Bridge/MCP processes are stopped. The production stack is unaffected.
Without `DEVBRIDGE_REAL_RUNTIME_ACCEPTANCE=1`, the test is explicitly skipped;
that skip is not real-Codex acceptance.

`DEVBRIDGE_REAL_EXPECTED_COMMIT` requires both binary Go build records to match
the supplied commit with `vcs.modified=false`. Binary provenance is saved in the
report even when this check is omitted during intermediate fixture development.

`DEVBRIDGE_REAL_CANCEL_ACCEPTANCE=1` extends the same fixture with late
cancellation of its naturally completed Execution, safe cancellation of a
controlled active Implementation, and an interrupt-outcome-unknown scenario.
Omit that variable to run only the four-turn sandbox/continuity acceptance.

- Actual per-turn policies `readOnly`, `readOnly`, `workspaceWrite`, `workspaceWrite`
  on the same backend Thread, with four distinct turns.
  backend command completion with nonzero exit and a permission/read-only
  error. Agent prose alone and mere absence of file changes cannot pass.
- The authorized Implementation writes exactly `tracked.txt`;
  checkout remains clean. Verification retains its existing Execution-owned
  workspace-write ceiling and post-execution nonmutation validation.
- Read-only SQLite evidence proves all four Runs belong to the same
  FeatureArea and its one canonical AgentConversation, every terminal boundary
  is quiesced and known, and no FeatureArea claim remains.
- A second fresh MCP transport and new AI session read the durable Execution
  result.

`artifacts/runtime-acceptance.json` contains the identities, independent Git
facts, observations, and fresh readback. `artifacts/codex-protocol.jsonl`
contains the observed backend requests/events, including sandbox policies and
write denials. The fixture fails if Codex declines the probe without executing
it: that behavior does not establish the requested sandbox proof.

For cancellation, an authorized long-running Python command writes its PID
and heartbeat counter only to `tracked.txt`. Reading that actual write is the
dispatch barrier. The safe test requires one exact `turn/interrupt`, real
`turn/completed` with `interrupted`, durable CANCELED, released claim, no live
command process, retained partial contents, and no automatic conversation
rotation or successor Run. Repeated requests replay the receipt and changed
provenance conflicts. The late request retains actual SUCCEEDED history.

The unknown scenario activates a fixture-only recorder fault after observing
the command running. It forwards the real interrupt and records but withholds
its ACK and exact terminal notification from Bridge. The claim must remain
held with recovery required. A normal isolated Core restart and the existing
`agent_operation_recover` MCP operation then quarantine the old workspace,
rotate to a threadless conversation, and release the claim. A fresh MCP
transport and newly bound session then read `execution_get`; replaying the
exact original cancellation request must return the same resolved receipt.
Both views retain state `OUTCOME_UNKNOWN` and historical quiescence `UNKNOWN`,
with `recoveryRequired=false` and a nonempty durable `resolvedAt`. Explicit
recovery resolves the caller's required action without inventing a known or
safe outcome for the abandoned Run. The saved
`artifacts/execution-cancellation-acceptance.json` distinguishes observed
backend events from deliberately withheld evidence and preserves the before
and after recovery readbacks. No production state or runtime implementation
is changed to inject this fault.

The existing isolated recovery and finalization fixtures cover the remaining
runtime boundaries without modifying production state:

```sh
go test -count=1 ./internal/storage/sqlite -run \
'Test(RecordFinalizationIntentRollsBackAsAUnit|TerminalRunAndFinalizerAreAtomicAcrossCrashWindows|PersistedFinalizationIntentConvergesAfterRestart|PersistedKnownBoundaryConvergesWithoutLosingHistory|UnknownRecoveryRequiresFencedExactOwnerAndIsReplayable|UnknownRecoveryRollsBackAcrossCrashWindows|CompletedBoundaryImmutableAndLateOwnerRejected|RecoveredRuntimeRejectsEveryStaleCallbackWrite|RuntimeEpochWriteFenceRejectsStaleContextAndAuthority|ExecutionPlanAndVerificationResultSurviveReopen)$' -v
go test -count=1 ./internal/localmcp \
  -run '^TestAgentOperationRecoveryIsDurableAndEquivalentAcrossAdapters$' -v
go test -count=1 ./internal/execution \
  -run '^TestAcceptedTurnLostResponseRestartRecoveryFreshThreadAndLateCallback$' -v
```

The recovery tests assert exact old claim/Run authorization, workspace
quarantine, threadless conversation rotation, replay, and stale-epoch callback
rejection against a successor. Cancellation-specific deterministic race tests
remain separate acceptance requirements.
