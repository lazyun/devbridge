# Cancel Agent work

Ask to cancel the exact Task work you are viewing. Review and Development use
`operation_cancel` with their owner type/ID and observed Run. Execution supports
the same consolidated command. A pending/confirmed Execution without a
Run can be canceled by omitting `runId`; it needs no extra confirmation or fake
Run. Reuse the same request ID and target after a lost response.

For an Upper Review, include the complete `expectedUpperVersion` from the
observed action. Keep that version with the original request after a lost
response. A newly observed version requires a new explicit operation and request
ID; do not silently update an unresolved request. Recovery records reauthorization
to PREPARING, and file access remains unavailable until its separately checked
preparation succeeds. Closing a FeatureArea prevents reactivation and new source
reads even if recovery was recorded before closing began.

The Task remains visible during cancellation. An interrupt acknowledgment means
the request was received, not that the Agent finished. A known interrupted Turn
converges to canceled work through normal finalization. If transport is lost,
Bridge can revoke old authority, quarantine its workspace/context and cancel the
exact owner with `UNKNOWN_ORPHANED`. Canceling a Review or Execution does not
automatically cancel its Task. Explicit Task cancellation and FeatureArea
retirement follow Core's separate business termination rules.
The backend may still be alive; its original
UNKNOWN facts and partial work remain historical. It cannot advance Bridge work
or reclaim the old context.

An initial Review approval freezes the Task definition and makes Implementation
ready; use explicit `execution_start` to begin initial Implementation.

Canceled Task decisions cannot confirm/revise or start another attempt. Use a
new Task for follow-up work. Canceling an individual Approval remains distinct
from canceling its owner. Closing a FeatureArea submits one durable stop request; Bridge automatically
cancels its current Tasks and Reviews through this same protocol. No preliminary
Task cancellation, Decision resolution, recovery, or second close is required.

See [canonical cancellation](../architecture/unified-agent-work-and-cancellation.md)
for the exact safety and restart contract.
