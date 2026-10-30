# Create-and-review Host transport and original receipt readback closure

Status: `CREATE_AND_REVIEW_HOST_TRANSPORT_AND_ORIGINAL_RECEIPT_READBACK_CLOSED`

This closure preserves the G16 public confirmation contract. The Card builds one canonical `CREATE_TASK_AND_REVIEW` request, freezes an explicit prepared `variantId`, and hashes that exact request before sending it. A Host timeout, JSON-RPC error, missing result, malformed response, or response loss leaves the original request in the Card's durable widget state and immediately starts `confirmation_get` with the same Session, intent, version, purpose, and request ID. The Card does not create a replacement request and does not call Task or Review commands directly.

Core's immutable submission ledger distinguishes `CONSUMED`, `REJECTED_NOT_CONSUMED`, and an unrecorded `UNKNOWN`. A replay must match the original request and submitted SHA-256. Core resumes only the original durable step sequence when recovery proves it safe. The composed result is the authoritative `CREATE_TASK` and `START_REVIEW` step set, including the exact Task, Review, and Run IDs.

Workspace reads now inventory incomplete submitted `CREATE_TASK_AND_REVIEW` confirmations separately from business decisions. Each read-only projection contains the original intent and request identities, receipt outcome, recovery direction, and any authoritative Task, Review, and Run IDs. While such a confirmation is incomplete, the owning FeatureArea reports `READ_CONFIRMATION_RESULT` instead of `CREATE_TASK`. The Dev Bridge Card renders the same facts without exposing a fallback command.

Automated evidence covers the production SQLite confirmation store and facade, exact receipt identity, payload conflict, identical replay, restart readback, pre-effect variant rejection, Task/Review/Run composition, Workspace convergence, Card Host error classification, automatic original-request readback, schema validation, and package compilation. Manual Desktop and ChatGPT Card acceptance remains pending; no live DataRoot, live intent, model service, Desktop UI, device, or simulator was used.
