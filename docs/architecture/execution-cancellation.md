# Canonical owner cancellation

Current Task/Execution authority follows the [final v0.1 model](task-execution-v0.1.md).
Initial Review `APPROVE` freezes the TaskDefinitionRevision and moves the existing
Task to `IMPLEMENTATION_READY`; it creates no Execution. Initial Implementation
requires explicit `execution_start`. Approval, reads, polling and restart do not
start it automatically.

See [the canonical cancellation contract](unified-agent-work-and-cancellation.md)
for exact owner admission, one-shot dispatch, replay, known terminal handling,
UNKNOWN orphan convergence and permanent callback/workspace fencing.

Canceling a Review or Execution closes that exact child operation; it does not
automatically cancel its semantic Task. Task status and phase remain Core-owned,
not inferred from child status. For the exact current UPPER_LLM Review, Core can
return an ACTIVE Task from `REVIEWING` to `REVIEW_REQUIRED` when closing its grant.
Explicit Task cancellation is subject to existing Core admission (the TASK owner
is for eligible pre-work cancellation, not a bypass for active owned work).
[FeatureArea retirement](feature-area-retirement.md) explicitly terminalizes its
Tasks after exact operation cancellation and authority convergence.

A durable cancellation request, interrupt ACK, observed terminal result,
finalization and recovery receipt are different facts. `turn/interrupt` ACK alone
is not terminal. Exact observed terminal and finalization facts support the known
path. When these are unavailable, `UNKNOWN_ORPHANED` converges the exact owner
after permanent authority revocation, workspace quarantine and durable orphan
disposition. It does not itself cancel the Task, assert successful completion,
or prove backend quiescence or physical death. Original backend evidence is
retained. Claim release ends Bridge engineering authority, not OS backend
liveness; a recovery receipt is not backend termination evidence.

[UPPER_LLM Review cancellation](unified-review-executors.md#cancellation-recovery-and-client-presentation)
uses the same exact owner/Run receipt protocol and its proven no-backend path to
close the grant and release the claim. No Codex interrupt or backend death is
invented. Cancel wins: late report submission is rejected. Submit wins: cancel
returns `ALREADY_TERMINAL` and preserves the immutable report. Restart recovery
reauthorizes the same Run under a new epoch/grant generation; it neither creates
new work nor reverses completed cancellation.

The [Upper authority and replay contract](upper-review-core-protocol.md) defines
the required observation version for new Upper commands, lifecycle revalidation,
old-epoch grant closure, and the distinction between pending convergence and
completed receipt replay. Completed recovery only records PREPARING authorization;
it does not activate file access or execute Prepare again.

Approval `CANCEL` does not invoke whole-owner cancellation. Legacy Run cancel
cannot bypass the canonical protocol. Replay must use the original request ID,
owner, Run and reason and never follows a successor Run implicitly.
