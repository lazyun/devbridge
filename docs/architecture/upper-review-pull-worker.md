# Pull-coordinated Upper Review workers

This is a narrow Core-owned integration for an external assistant, including dot,
that can call Dev Bridge's existing authenticated MCP surface. It operates only
on an **already admitted `UPPER_LLM` Review**. It is not an `AgentID=dot` backend,
a new work queue, a cloud-thread launcher, or a replacement for user confirmation.

## Existing authority, one delivery

The worker first reads the exact referenced Review/Run and `review_access_get`.
It calls `review_worker_claim` with that full `UpperTarget`, a new `requestId`, and
a stable `workerId` label. Core validates the original creator AISession and the
adapter-authenticated caller scope, active Review/Run, current runtime epoch,
grant generation and canonical FeatureArea operation claim. The worker label is
only correlation: it is not proof of a particular dot account, model or process.

The claim is subordinate to those existing authorities. In the same writer
transaction, Core freezes an immutable receipt and `DISPATCH_INTENT`. The receipt
binds the exact instruction hash, current frozen workset (including every member,
version, origin, start snapshot, root and worktree identity), and the exact
returned payload hash. Only after commit does the first response include
`executionPayload`: unchanged instruction bytes and the existing bounded
`UpperAccess`, in separate fields. It does not concatenate policy into the task
instruction, grant new file authority, create a Run, or launch a model.

| Readback state | Meaning |
| --- | --- |
| `DISPATCH_INTENT` | First response with executable payload; durable offer, not proof it was consumed. |
| `OUTCOME_UNKNOWN` | Intent exists but execution/result cannot be proven. No executable payload is returned. |
| `RESULT_RECORDED` | The existing structured report receipt was committed. Still not user acceptance or host termination proof. |

Concurrent identical claims return executable payload at most once. Every retry
and `review_worker_get` returns only the immutable receipt and known result. If a
first response is lost, **do not change request/worker IDs or execute from another
readback**. There is no lease expiry, timeout reassignment, implicit replay,
background recovery, or exactly-once execution guarantee. This intentionally
trades availability for avoiding an unprovable duplicate execution.

## Reads, reports, cancellation and recovery

The worker reads files through existing `file_list` / `file_read` using the exact
`reviewTarget` and repository/member identities. Original Session/grant controls
and read evidence remain authoritative. These APIs are not worker-exclusive;
the new claim establishes a single authorized **result committer**, not exclusive
read access, host execution or a hostile-agent sandbox.

An enrolled Run requires `workerClaim: {requestId, workerId}` on every fresh
`review_result_submit`, including legacy and v2 store paths. Missing or different
claims fail. The binding and lifecycle fences are checked before source
observation and again in the result writer transaction. Report receipt, existing
Review/Run transition and worker completion commit atomically. An unclaimed
historical Review retains the old submission shape and payload hash.

An exact completed report replay returns its original receipt before current
epoch/grant checks, while claimed replay still authenticates its original
Session/caller/worker. This works after restart without asserting the original
worker is still running. Fresh stale-generation, canceled, recovered, or otherwise
revoked work cannot submit. Existing explicit Core cancellation remains the
revocation path; the worker store does not refresh grants, release the canonical
claim, transfer ownership, or authorize a new generation. One intent is allowed
per Run: fresh grant recovery for an enrolled Run is refused before effects
because safe same-Run worker continuation is unsupported. A new admitted Run,
if separately authorized through the existing workflow, is distinct work; no
automatic continuation is implemented. Cancellation can revoke Core access/report
acceptance but cannot prove an external process stopped (`NO_BACKEND` remains
truthful). Restart guidance keeps `RECOVERY_REQUIRED` but offers existing explicit
Review cancellation only when that exact owner/Run cancellation is available;
it does not suggest reauthorization will resume a worker. Cancellation envelopes
use the semantic Review owner, not the legacy virtual execution inventory ID.
An old cancellation replay keeps its original receipt/owner and marks readback
stale rather than attaching a successor Review's identity or actions. Historical
worker claim/get readback follows the same rule. A different selected Task never
inherits the interrupted worker's cancellation or recovery action.

## Event wakeups and deployment boundary

A notification can tell dot to inspect an exact referenced Review/Run. It is only
a hint: duplicate, delayed, missing or out-of-order wakeups must not authorize
work, choose the newest Run, or cause redispatch. The durable Core claim/readback
is the authority. No event subscription, credentials, notification sender,
background poller or cloud-thread endpoint is installed by this implementation.
The user/operator must separately connect an authorized MCP client and configure
any supported wakeup route. No direct Go-to-`cloud_threads` transport, persistent
callback, cloud child creation or cloud cancellation API is assumed.

## Verification and limits

Deterministic tests cover real SQLite first/concurrent dispatch, response-loss
readback, exact instruction bytes, restart/stale target fences, claimed-vs-legacy
submission races, terminal receipt replay, legacy nil-claim hashes and rollback
of an injected completion failure. A production bootstrap and shared in-memory
MCP test covers a genuinely admitted two-member v2 Review through claim, bounded
reads, structured report, replay and no extra Run/user acceptance. Separate v2
checks cover cancellation/report races, foreign caller scope and restart with
side-effect-free refusal of unsafe same-Run recovery. Real Local MCP HTTP tests
also cover both restart guidance projections, cancellation enrichment and old
cancellation replay after a separately admitted successor Review.

These are protocol and Core integration tests, not a live dot/model acceptance
test. General implementation/verification Agent execution, durable cloud task
continuation, host cancellation and result acquisition outside this UPPER_LLM
MCP path remain unsupported.

Actual file implementation is available separately through the already-approved
DIRECT_FILE authority and its [pull worker](direct-file-pull-worker.md). That
channel uses Core file operations and does not turn this Review protocol into
a generic Agent runtime or grant command execution.
