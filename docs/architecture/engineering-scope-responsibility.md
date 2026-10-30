# Engineering scope responsibility

The [final v0.1 model](task-execution-v0.1.md) governs scope and authorization.
Review Approval freezes goals, constraints, WorkSpec, VerificationSpec and exact
repository provenance in an immutable TaskDefinitionRevision. The Coding Agent
performs analysis and Implementation inside the Development Execution.

GPT and Codex are trusted. Bridge does not prove which files Codex inspected or
whether its analysis was sufficient. It retains exact repository/baseline facts,
Git drift, runtime ceilings, immutable identity and canonical workflow boundaries.
Semantic constraints and instructions to detect material boundary conflicts are
owned by the Upper LLM / caller. Bridge does not add such instructions. The
structured Implementation resultDisposition SCOPE_CONFLICT stops the Execution;
ordinary text has no control meaning. It cannot rewrite the revision. The upper
Agent chooses the next authorized Task/Review action from the durable result.
See the frozen [Prompt Neutrality Principle](task-execution-v0.1.md#prompt-neutrality-principle-frozen).

Review and Development share the existing FeatureArea claim. Independent areas
remain concurrent. Completion uses the original durable IDs, never a client's
current selection. Reads do not dispatch, repair or authorize work.

## Implementation SCOPE_CONFLICT handling

SCOPE_CONFLICT from the exact current Implementation stops that attempt, records
the failure and puts an active Development Task into RECOVERY_REQUIRED. It does
not change the frozen definition or authorize Verification, Acceptance, commit,
merge, retry, scope expansion, or a new Review. Keep all existing changes and
evidence. Plain text containing the token is not a control signal. A Review
materialization with the same outcome has a separate invalid-result resolution;
do not use the Implementation contract for it.

Read the exact Task/Execution/Run, failure, approved revision, Git/workset evidence,
and current Core capabilities first. Native result availability and a known
scope failure are independent: retain UNKNOWN when the final native result is
missing. A typed scope failure is not proof that engineering effects are absent.

| Observed condition | Authorized next path | New Review? |
| --- | --- | --- |
| Results or effects uncertain | Exact receipt/result readback; prepare/start/read a Recovery Status child only if Core admits its original source/Thread/workset | No automatic Review |
| Same approved goal/scope; resolved CONSISTENT / RECOVERED_PARTIAL; continuation eligible | Explicit same-revision RETRY_IMPLEMENTATION with recoveryStatusId and exact recoveryContinuationInstruction | Not required for that unchanged definition |
| Consistent recovery assessment says REVISION_REQUIRED | Resolve with the exact typed successor proposal; the resulting RECOVERY_PURPOSE confirmation freezes a new definition and successor Execution after the user confirms | Existing formal recovery revision does not automatically create a Review |
| Further engineering assessment is needed | Use exact same-Task REQUEST_TASK_REVIEW eligibility and review_prepare(action=REQUEST_SUPPLEMENTAL_REVIEW) when offered; then complete the new Review and approval | Yes, for that explicitly chosen and eligible supplemental Review |
| Actual unauthorized changes, drift, CONFLICT, INSUFFICIENT, active writer, or unavailable recovery source | Keep evidence and block continuation. Explain the concrete reason and only the current Core-provided stop/revision/cancellation choices | A new Review cannot bypass unresolved writer or scope facts |

Do not send ordinary execution_prepare(action=REQUEST_CHANGES) to bypass a
RECOVERY_REQUIRED fence. The typed Recovery Purpose confirmation and ordinary
acceptance continuation are different authorities. Never reset/clean changed
files, edit an immutable revision, reuse a terminal Run, or create another Task
to evade these gates. If the original runtime cannot be admitted for recovery,
report that exact limitation instead of promising that a new Review will start.
The current supplemental-Review gate in an Implementation recovery is not a
general escape hatch.

These are routing explanations, not new lifecycle transitions or approval policy.
The existing [Recovery Status contract](codex-recovery-status-reconciliation-v1.md)
owns assessment and resolution; current read-side actions remain authoritative.
