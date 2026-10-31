# Single-run, host-neutral UPPER worker consumer

`internal/upperworker` is a reusable client for the existing
[UPPER pull-worker protocol](upper-review-pull-worker.md). It accepts an already
connected MCP transport and a trusted reviewer callback. It is **not** a dot
runtime, provider/model launcher, scheduler, credential manager, or host sandbox.
It adds no Core authority, schema migration or new MCP operation.

## Supported slice

- Exactly one already-admitted **v2 UPPER_LLM Review**, with ACTIVE access and a
  complete frozen read-only member set. Legacy v1 and DIRECT_FILE are not offered
  by this client. In particular, DIRECT_FILE result submission can dispatch a
  separately authorized RUN verification; it is not universally model-free.
- The caller supplies the full original `review.WorkerClaimInput` and a separate
  stable report request ID in `upperworker.Request`. It must retain these **before
  calling Run**. The consumer never discovers the newest Task/Run, creates a
  Session, changes a request/worker label or invents consent.
- `New(mcp, request, reviewer)` constructs one consumer. `Run(ctx)` requires a
  bounded context and is single-use, including concurrent calls. A second call
  returns `ErrAlreadyRun` with conservative `CLAIM_UNKNOWN`: a previous call may
  have dispatched. It does not retry even if the first call failed preflight.
- The injected MCP transport remains caller-owned. It must not automatically
  resend business calls; SDK Streamable HTTP clients should use `MaxRetries: -1`.
  This package makes at most one claim and one report-submission invocation per
  Run. An arbitrary injected transport is trusted to honor its no-resend contract.

## Preflight before delivery

The consumer first pages through `tools/list`, with finite page/schema bounds,
rejecting duplicates, missing tools, malformed schemas and repeated cursors. It
checks the actual closed input contracts for `review_access_get`,
`review_worker_claim`, `review_worker_get`, `file_list`, `file_read` and
`review_result_submit`, including the nested worker and exact-target fields.
Schemas are resolved without remote loaders, and representative valid inputs
plus unknown-field/unbound-read negatives are validated locally. The small report
specimen is only a local schema probe: it is never submitted as an engineering
report. Actual report input is validated again after the callback.

Familiar names, version labels and capability hints are insufficient. Missing
`workerClaim` support or a missing claim/get entry stops before any Core business
call. This detects connector-surface incompatibility, not the version or health
of a remote binary. It does not create or update the connector.

After preflight, the exact `review_access_get` must return the original target,
ACTIVE v2 state, workspace and valid read-only member set. No access prepare,
recovery or grant refresh is invoked. V2's scalar baseline/worktree/startSnapshot
may legitimately be empty: the member bindings are authoritative, not legacy v1
scalar fields.

## Executable delivery and bounded reads

The one claim response must be either:

- first `DISPATCH_INTENT`, `replay=false`, with exactly its executable payload; or
- an immutable replay/readback with no executable payload and either
  `OUTCOME_UNKNOWN` or a known `RESULT_RECORDED` receipt.

The callback runs only in the first case, after checking the full original
Session/target/worker/request identity, raw instruction SHA-256, and SHA-256 of
`json.Marshal(review.WorkerExecutionPayload)`. The returned access must match the
pre-claim authority, ignoring only its observation timestamp. Core's
`worksetSha256` also includes private SQL binding facts not exposed by
`UpperAccess`; the client preserves and checks its digest syntax, **not** an
independent reconstruction or qualification of that private binding.

`Delivery` is detached data: changing its target/member list cannot retarget
reads or submissions. `Files.Read/List(ctx, memberID, path)` resolves only a
member from the frozen payload, sends the original `reviewTarget` and exact
repository/member/version, and rejects noncanonical/escaping paths locally.
Core remains the authority for scope, containment, source identity and lifecycle.
Returned member/workspace/origin provenance must match. File bytes must match
their returned digest/size and evidence ID. Only Core's real read evidence may
support the authored report; path listings are not content evidence.

The callback receives no generic tool-call method, write, grant closure,
confirmation, cancellation or report-submission interface. Its reader is revoked
and in-flight scoped reads are canceled when the callback returns. This is API
confinement for trusted code, **not** OS isolation or proof that an external host
process stopped. The callback must honor its context; this package cannot kill
arbitrary caller-owned code.

## Report and uncertainty

The callback returns its actual `review.ReviewReport` or an error. On callback
error/panic, the client does not manufacture BLOCKED findings, submit a report,
close Core access or cancel the Review. It closes only its callback reader.

A valid v2 report is detached/frozen and combined with the original target,
worker claim and caller-supplied report ID. The receipt must match those original
Review/Run/request identities, completion status and report disposition; its
payload digest hashes the **entire typed `review.SubmitInput`**, not just the
report. Instruction and payload digests are not confirmation canonical hashes.
The unchanged authored report is not an approval or acceptance decision.

| Consumer state | Meaning — never retry authority |
| --- | --- |
| `NOT_DISPATCHED` | This invocation failed before its claim call. Not proof that this identity has no prior Core request. |
| `CLAIM_UNKNOWN` | Claim response missing/invalid, or a duplicate local Run refused. Preserve original identity. |
| `DELIVERY_UNKNOWN` | Core's original delivery exists, without a proven recorded report. No callback replay. |
| `CALLBACK_FAILED` | A delivered callback failed. No synthesized report or cancellation. |
| `REPORT_NOT_SUBMITTED` | Local validation/context prevented this report call; delivery still exists. No retry/reassignment inference. |
| `REPORT_UNKNOWN` | The report call may have committed; its response is unavailable or invalid. |
| `RESULT_RECORDED` | An exact Core report receipt was validated. Not user acceptance, model-consumption proof, or host termination. |

`Observe(ctx)` is explicit and read-only: tools/list plus the exact original
`review_worker_get`. It never reads current access to renew it, claims again,
invokes the callback, submits a report or chooses a successor. It rejects any
executable payload on readback. A known original claim receipt cannot change;
when the same client retains its report input, the returned report digest is
checked against those exact bytes. Public returned structures are detached from
these private retained values.

A new process/client can observe using the same original identities after Core
restart, but has no proof its own callback previously ran. Without a retained
report input it validates the returned receipt's identity/digest shape, not the
missing report body's hash independently. This client has **no durable local
journal or host execution recovery**. Preserve returned original inputs/receipts
for diagnosis; never turn UNKNOWN into a new request, replay the callback, or
execute from a saved/readback payload.

## Local verification and deployment boundary

Run the focused checks with the repository Go toolchain:

```sh
go test ./internal/upperworker -count=1
go test ./internal/controlapi -run '^TestUpperWorkerClient' -count=1
go test -race ./internal/upperworker -count=1
go test -race ./internal/controlapi -run '^TestUpperWorkerClient' -count=1
```

Unit tests use explicitly synthetic responses for hostile/malformed client input
and orchestration checks. Separate HTTP integration admits a genuine two-member
Review through production bootstrap/confirmation/coordinator services and a
schema-only DB template. The callback actually reads the two members and uses
Core-returned evidence in a report; independent source/managed file bytes,
HEADs/status, exact Run count and unaccepted Task state remain unchanged.
Missing tool/schema checks stop before claims. Losing an actual completed claim
or report response exercises uncertainty without fabricating business rows;
explicit cold Core readback preserves the original unknown/recorded result.
This is response loss, **not process-crash injection or live dot acceptance**.

The conversation-visible connector catalog inspected on 2026-10-31 advertised
`0.1.0-dev+gc4ae1d3` and omitted all four review/file worker claim/get tools. This
is only the visible catalog observation, not proof of the deployed Core version.
An authorized host still needs a compatible connector/deployment and an actual
dot callback/transport integration. No credentials, event wakeup, cloud task,
model call, native runtime qualification, acceptance or merge is installed or
established by this local client.

### Retained authoring failures and review correction

The first real HTTP run and its diagnostic rerun failed before claiming: the new
client incorrectly demanded nonempty legacy scalar baseline/startSnapshot on v2
access. Genuine v2 access instead exposes the workspace and complete immutable
member set; the client was corrected accordingly. An initial synthetic unit run
also used a Review ID that failed the actual advertised `review_access_get`
pattern. Those failed logs remain failed evidence, not Core defects or passes.

Independent review found a separate new-client defect before publication: the
returned claim receipt aliased the consumer's stored comparison baseline. It is
now a separate private copy. Regressions mutate returned receipt/report data,
including concurrent receipt mutation during Observe, and require unchanged
original validation. A first successful Observe also pins its own private
receipt baseline for future comparisons. These are client corrections, not
changes to Core authority or a claim of hostile-callback isolation.

The earlier integration race passed, but source changed while it ran; it is
retained as preliminary evidence only, not final frozen-source qualification.

### Frozen-source validation (2026-10-31)

- Consumer unit suite: **12 top-level tests and 21 named subcases**, ordinary
  **4.918s** and race **48.729s**, both exit 0. Includes one-shot concurrency,
  malformed/foreign proof, report-response uncertainty, detached returned data,
  immutable observation baselines and in-flight reader cancellation/join.
- Actual Local MCP/Core suite: **four new admitted-Review fixtures** (normal,
  claim response loss, report response loss, callback failure), plus two existing
  worker regressions in the ordinary invocation: **18.395s**, exit 0. The new
  fixtures' separate race invocation passed in **189.311s**, exit 0. Both loss
  fixtures include cold readback of the same original identity and unchanged
  authoritative rows; these are not additional process-crash tests.
- All five tested Go source hashes matched before/after ordinary and race runs;
  neither race log contained a race-detector warning. Root `go build -p 2 ./...`,
  `go vet -p 2 ./...`, relative documentation-link and whitespace checks passed.
- Independent source review approved the corrected client and its limitations.
  These focused checks do not clear the previously documented full-root-suite
  native/socket fixture limitations or qualify the live connector/dot host.
