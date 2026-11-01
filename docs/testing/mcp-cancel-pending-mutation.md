# Concurrent MCP cancellation with an unresolved file effect

## Reproduced defect

On 2026-11-01, a disposable Core/SQLite/worktree fixture on
`976514df44b083b55cc03e6f35f7734f5d9a0938` reproduced a recovery dead end through
the production GPT and Local MCP HTTP handlers:

1. An enrolled file worker made a legitimate first write, putting the Task in
   IMPLEMENTING. The fixture user separately prepared exact cancellation.
2. A second Local MCP `file_write` reached the existing post-effect test hook:
   its physical bytes and durable journal existed, but its operation receipt
   remained PENDING. The hook held the real file-authority mutex.
3. A concurrent GPT MCP `confirmation_commit` was consumed. The test observed
   the exact durable commit request and its hash, CONSUMED outcome, CONFIRMING
   intent and UNKNOWN step before releasing the write. This is stronger than
   merely starting two goroutines or sleeping and assuming they overlap.
4. The hook returned an injected error before mutation-receipt completion.
   The cancellation correctly refused to close authority while a mutation was
   unresolved, but its consumed confirmation became UNKNOWN / non-resumable.
5. Both requests terminated; both HTTP adapters closed; Core actually closed
   and reopened the same DataRoot. New SDK connections explicitly submitted
   the exact old mutation payload to obtain its existing journal proof. This
   reconciled the receipt to COMPLETED without executing another file effect,
   changing physical inode/mtime/size/digest, or refreshing file authority.
6. A new explicit cancellation was nevertheless rejected with
   TASK_ACTION_PENDING. The old UNKNOWN cancellation retained the Task action
   owner, despite having performed no cancellation effect.

The original failure was FILE_OPERATION_FAILED with the unresolved-receipt
reason. The first cancellation was **not successful**, and proving the file
mutation did not prove cancellation. The baseline red trace preserves both
facts; the test never deletes an owner row or bypasses confirmation admission.

## Narrow correction

The unresolved-mutation guard now returns the dedicated typed code
`DIRECT_FILE_CANCELLATION_MUTATION_UNRESOLVED`. It is emitted only after a
successful unresolved-receipt query and **before** any cancellation UPDATE or
cancellation-receipt insertion.

The confirmation consumer recognizes only CANCEL_DIRECT_FILE plus that typed
code plus an empty returned workflow. It uses the existing no-effect rejection
path to record the consumed cancellation as **REJECTED / NOT_STARTED**, rather
than UNKNOWN. This releases the rejected Task action without granting new file
authority or automatically repeating any command.

The separation is important:

- The earlier file mutation remains PENDING/UNKNOWN until its own proof is
  available. A new cancellation while it is unresolved still cannot close the
  grant.
- The rejected confirmation remains consumed. Its old commit must not become
  an executable choice again. Its historical receipt remains unchanged when a
  later, separately prepared and confirmed cancellation closes authority.
- Public `submission.businessOutcome=COMMITTED` still describes confirmation
  consumption, **not cancellation success**. The step records REJECTED and
  NOT_STARTED; this patch does not broaden the existing outcome reducer.
- Ordinary FILE_OPERATION_FAILED, message-only errors, storage/context errors,
  START/RECOVER, or a returned reserved workflow are not reclassified.
- There is no migration or automatic release of historical UNKNOWN intents.
  A crash before the specific no-effect rejection is durably recorded may
  still be conservatively UNKNOWN; this patch does not claim that window is
  closed.

## Repeatable gates

`TestFileWorkerPendingMutationBlocksHTTPCancellationAcrossRestart` exercises the
real two-adapter race and cold journal readback. Bootstrap and confirmation
choices are explicit deterministic test fixtures, not actual user tasks or
human UI actions. The production admission, mutex, SQLite, filesystem and MCP
handlers are real.

`TestDirectFileCancellationNoEffectClassificationIsNarrow` checks nine typed,
wrapped, generic, context, action and returned-state combinations. Only the
dedicated pre-effect CANCEL refusal may become NOT_STARTED.

```sh
go test ./internal/controlapi \
  -run '^Test(FileWorkerPendingMutationBlocksHTTPCancellationAcrossRestart|DirectFileCancellationNoEffectClassificationIsNarrow)$' \
  -count=1 -timeout 3m -v
go test -race ./internal/controlapi \
  -run '^Test(FileWorkerPendingMutationBlocksHTTPCancellationAcrossRestart|DirectFileCancellationNoEffectClassificationIsNarrow)$' \
  -count=1 -timeout 5m -v
```

Use this bounded selection for the changed persistence/race boundary rather
than routinely rerunning the whole suite. The final HTTP source passed three
focused runs in 19.695s; the nine-case classification gate passed in 0.123s.
The final HTTP race run passed in 166.10s (168.025s package) with no race report;
targeted vet passed. The final-source guard-disabled negative control failed
on the intended CLOSED + PENDING invariant. No broad aggregate or whole-suite
rerun was added for this correction.

The baseline overlay restores the original production source without rewriting
the branch. A separate negative control removes only the unresolved guard and
must fail when it observes a CLOSED grant with a PENDING mutation receipt. That
control demonstrates the safety oracle; it is not evidence that the unmodified
baseline prematurely canceled the write.

Private bundles `devbridge-cancel-pending-regression-evidence` and
`devbridge-cancel-pending-evidence` retain frozen source, exact MCP inputs and
outputs, durable-overlap and physical-proof witnesses, red/control/final logs
and review metadata. Raw runtime bundles are not part of the public patch.

## Limits

This is a deterministic fault-seam test using real MCP requests, not an
unmodified-binary crash experiment. It does not inject SIGKILL or TCP response
loss, prove an external worker process stopped, or qualify actual model
concurrency, production dot, host isolation or independent authenticated users.
The restart occurs after both concurrent calls have returned, not while they
are still executing. Journal proof here is an explicit controlled test action,
not permission to automatically retry unknown user mutations.

No file implementation result or acceptance, Execution proposal, delivery
commit, merge, deployment or native/provider launch is produced. The bootstrap
fixture's existing `agent_executions` count is preserved, not asserted to have
always been zero. Focused checks do not establish that the full repository
suite passes.

A separate bounded check of the two historically timed-out Local MCP cases
passed individually on the same baseline. Their original whole-package timeout
stacks were not found in the current workspace, so neither their cause nor a
whole-suite pass is claimed. Those rechecks retain command/result excerpts,
not complete original logs, in `devbridge-suite-timeout-recheck-evidence`.
