package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Suite struct {
	Adapter Adapter
	Root    string
	Keep    bool
	Only    []GateID
	Now     Clock
}

func (s Suite) Run(ctx context.Context) ([]GateResult, string, error) {
	adapter := s.Adapter
	if adapter == nil {
		adapter = DefaultDeferredAdapter()
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	root := s.Root
	ownedRoot := false
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "devbridge-real-e2e-")
		if err != nil {
			return nil, "", err
		}
		ownedRoot = true
	}
	fixture, err := NewFixture(ctx, root)
	if err != nil {
		return nil, root, err
	}
	gates := []struct {
		id  GateID
		run func(context.Context, *Fixture, Adapter, Clock) GateResult
	}{
		{GateNormalExecution, s.runNormal},
		{GateRuntimeApproval, s.runApproval},
		{GateActiveInterruption, s.runActiveInterruption},
		{GateApprovalInterruption, s.runApprovalInterruption},
		{GateParallelIsolation, s.runParallelIsolation},
		{GateWorkspaceContinuity, s.runWorkspaceContinuity},
		{GateWorkVerification, s.runWorkVerification},
		{GateExecutionScope, s.runExecutionScope},
		{GateTaskFirstScopeReview, s.runTaskFirstScopeReview},
	}
	results := make([]GateResult, 0, len(gates))
	for _, gate := range gates {
		if len(s.Only) == 0 || slices.Contains(s.Only, gate.id) {
			results = append(results, gate.run(ctx, fixture, adapter, now))
		}
	}
	preserve := s.Keep
	for _, result := range results {
		if result.Status == StatusFail || result.Status == StatusBlocked {
			preserve = true
		}
	}
	if ownedRoot && !preserve {
		if err := os.RemoveAll(root); err != nil {
			return results, root, fmt.Errorf("clean successful harness artifacts: %w", err)
		}
		root = ""
	}
	return results, root, nil
}

func (s Suite) runWorkspaceContinuity(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateWorkspaceContinuity, now().UTC(), fixture)
	request := newRequest(GateWorkspaceContinuity, fixture)
	sourceBefore, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.WorkspaceContinuity(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	sourceAfter, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_one"] = observation.RunOneID
	result.Evidence["run_two"] = observation.RunTwoID
	result.Evidence["workspace_id"] = observation.WorkspaceOne
	result.Evidence["worktree_id"] = observation.WorktreeOne
	result.Evidence["worktree_path"] = observation.WorktreePathOne
	result.Evidence["thread_id"] = observation.ThreadOne
	result.Evidence["turn_one"] = observation.TurnOne
	result.Evidence["turn_two"] = observation.TurnTwo
	result.Evidence["run_one_end_snapshot"] = observation.EndSnapshotOne
	result.Evidence["run_two_start_snapshot"] = observation.StartSnapshotTwo
	result.Evidence["run_one_token_total"] = fmt.Sprint(observation.RunOneTokenTotal)
	result.Evidence["run_two_token_total"] = fmt.Sprint(observation.RunTwoTokenTotal)
	result.Evidence["one_active_rejection"] = observation.OneActiveRejection
	if sourceBefore.HEAD != sourceAfter.HEAD || sourceBefore.Status != sourceAfter.Status || sourceBefore.Diff != sourceAfter.Diff {
		return failResult(result, now, errors.New("continuation changed the source repository"))
	}
	if observation.RunOneID == "" || observation.RunTwoID == "" || observation.RunOneID == observation.RunTwoID || observation.TaskID == "" {
		return failResult(result, now, errors.New("continuation did not produce two distinct Runs for one Task"))
	}
	if observation.WorkspaceOne == "" || observation.WorkspaceOne != observation.WorkspaceTwo || observation.WorktreeOne == "" || observation.WorktreeOne != observation.WorktreeTwo || !samePath(observation.WorktreePathOne, observation.WorktreePathTwo) {
		return failResult(result, now, errors.New("continuation replaced the durable Workspace or worktree"))
	}
	if observation.ThreadOne == "" || observation.ThreadOne != observation.ThreadTwo || observation.TurnOne == "" || observation.TurnTwo == "" || observation.TurnOne == observation.TurnTwo {
		return failResult(result, now, errors.New("continuation did not resume one Codex thread with a distinct turn"))
	}
	if !observation.Restarted || observation.RunOneStatus != "SUCCEEDED" || observation.RunTwoStatus != "SUCCEEDED" {
		return failResult(result, now, errors.New("continuation did not succeed across the Bridge restart"))
	}
	if observation.OneActiveRejection != "TASK_ALREADY_HAS_ACTIVE_RUN" {
		return failResult(result, now, fmt.Errorf("same-Task active Run was not rejected: %s", observation.OneActiveRejection))
	}
	if observation.StartSnapshotOne == "" || observation.EndSnapshotOne == "" || observation.StartSnapshotTwo == "" || observation.EndSnapshotTwo == "" || observation.EndSnapshotOne != observation.StartSnapshotTwo {
		return failResult(result, now, errors.New("per-Run snapshot boundary was not durable and contiguous"))
	}
	if !slices.Equal(observation.RunOneChangedFiles, []string{"tracked.txt"}) || len(observation.RunTwoChangedFiles) != 0 {
		return failResult(result, now, fmt.Errorf("per-Run Git deltas are not isolated: run1=%v run2=%v", observation.RunOneChangedFiles, observation.RunTwoChangedFiles))
	}
	if observation.RunOneTokenStatus != "AVAILABLE" || observation.RunTwoTokenStatus != "AVAILABLE" || observation.RunOneTokenTotal <= 0 || observation.RunTwoTokenTotal <= 0 {
		return failResult(result, now, fmt.Errorf("per-Run token deltas are not available and nonzero: run1=%s/%d run2=%s/%d", observation.RunOneTokenStatus, observation.RunOneTokenTotal, observation.RunTwoTokenStatus, observation.RunTwoTokenTotal))
	}
	worktreeValue, err := fixture.Inspect(ctx, observation.WorktreePathTwo)
	if err != nil {
		return failResult(result, now, err)
	}
	if !slices.Equal(worktreeValue.ChangedFiles, []string{"tracked.txt"}) {
		return failResult(result, now, fmt.Errorf("cumulative dirty worktree truth=%v", worktreeValue.ChangedFiles))
	}
	tracked, trackedErr := os.ReadFile(filepath.Join(observation.WorktreePathTwo, "tracked.txt"))
	if trackedErr != nil || string(tracked) != request.ExpectedContent {
		return failResult(result, now, fmt.Errorf("repeated execution file contents are incorrect: tracked=%q/%v", tracked, trackedErr))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "one applied Task Plan reused one durable Workspace, worktree, and Codex thread across a restart with isolated Run deltas", now().UTC()
	return result
}

func (s Suite) runWorkVerification(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateWorkVerification, now().UTC(), fixture)
	request := newRequest(GateWorkVerification, fixture)
	sourceBefore, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.WorkVerification(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	sourceAfter, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	implementation, verification := observation.Implementation, observation.Verification
	result.Evidence["run_execution_id"] = observation.RunExecutionID
	result.Evidence["implementation_run_id"] = implementation.ID
	result.Evidence["verification_run_id"] = verification.ID
	result.Evidence["workspace_id"] = implementation.WorkspaceID
	result.Evidence["thread_id"] = implementation.ThreadID
	result.Evidence["implementation_tokens"] = fmt.Sprint(implementation.TokenTotal)
	result.Evidence["verification_tokens"] = fmt.Sprint(verification.TokenTotal)
	result.Evidence["verification_scope"] = observation.RunScope
	result.Evidence["verification_check"] = observation.RunCheckKind + ":" + observation.RunCheckCommand
	result.Evidence["verification_observed_command"] = verification.VerificationObservedCommand
	result.Evidence["verification_agent_claim"] = verification.AgentClaim
	result.Evidence["failing_execution_id"] = observation.FailExecutionID
	result.Evidence["failing_verification_run_id"] = observation.FailVerification.ID
	result.Evidence["failing_observed_command"] = observation.FailVerification.VerificationObservedCommand
	if observation.FailVerification.VerificationExitCode != nil {
		result.Evidence["failing_exit_code"] = fmt.Sprint(*observation.FailVerification.VerificationExitCode)
	}
	result.Evidence["none_execution_id"] = observation.NoneExecutionID
	result.Evidence["none_implementation_run_id"] = observation.NoneImplementation.ID
	result.Evidence["none_verification_cost"] = "NOT_REQUESTED"
	result.Evidence["none_waiver"] = observation.NoneWaiverSource + ":" + observation.NoneWaiverReason
	if sourceBefore.HEAD != sourceAfter.HEAD || sourceBefore.Status != sourceAfter.Status || sourceBefore.Diff != sourceAfter.Diff {
		return failResult(result, now, errors.New("implementation/verification plans changed the source repository"))
	}
	if observation.RunOutcome != "SUCCEEDED" || observation.RunTaskStatus != "SUCCEEDED" || implementation.Status != "SUCCEEDED" || verification.Status != "SUCCEEDED" || verification.VerificationStatus != "PASSED" || verification.VerificationChecks != 1 {
		return failResult(result, now, fmt.Errorf("RUN plan did not reach verified success: outcome=%s task=%s implementation=%s verification=%s/%s", observation.RunOutcome, observation.RunTaskStatus, implementation.Status, verification.Status, verification.VerificationStatus))
	}
	if verification.VerificationCheckStatus != "PASSED" || !strings.Contains(verification.VerificationObservedCommand, "git diff --check") || verification.VerificationExitCode == nil || *verification.VerificationExitCode != 0 || verification.AgentClaim == "" {
		return failResult(result, now, fmt.Errorf("successful deterministic check did not retain command Evidence and AgentClaim independently: %+v", verification))
	}
	failing := observation.FailVerification
	if observation.FailOutcome != "FAILED" || observation.FailTaskStatus != "FAILED" || observation.FailImplementation.Status != "SUCCEEDED" || failing.Status != "FAILED" || failing.FailureCode != "VERIFICATION_FAILED" || failing.VerificationStatus != "FAILED" || failing.VerificationChecks != 1 || failing.VerificationCheckStatus != "FAILED" || !strings.Contains(failing.VerificationObservedCommand, "false") || failing.VerificationExitCode == nil || *failing.VerificationExitCode == 0 || failing.VerificationFailureReason != "NONZERO_EXIT" || failing.AgentClaim == "" {
		return failResult(result, now, fmt.Errorf("controlled failing command was not derived from real command Evidence: outcome=%s task=%s implementation=%s verification=%+v", observation.FailOutcome, observation.FailTaskStatus, observation.FailImplementation.Status, failing))
	}
	if implementation.ID == "" || verification.ID == "" || implementation.ID == verification.ID || observation.RunTaskID == "" || implementation.TaskID != observation.RunTaskID || verification.TaskID != observation.RunTaskID || implementation.Purpose != "IMPLEMENTATION" || verification.Purpose != "VERIFICATION" || implementation.ExecutionProposalID != observation.RunExecutionID || verification.ExecutionProposalID != observation.RunExecutionID {
		return failResult(result, now, errors.New("RUN plan purpose, Task, or execution-plan provenance is inconsistent"))
	}
	if implementation.WorkspaceID == "" || implementation.WorkspaceID != verification.WorkspaceID || implementation.WorktreeID == "" || implementation.WorktreeID != verification.WorktreeID || !samePath(implementation.WorktreePath, verification.WorktreePath) || implementation.ThreadID == "" || implementation.ThreadID != verification.ThreadID || implementation.TurnID == "" || verification.TurnID == "" || implementation.TurnID == verification.TurnID {
		return failResult(result, now, errors.New("implementation and verification did not retain one Workspace/worktree/thread with distinct Runs and turns"))
	}
	if implementation.StartedAt == nil || implementation.FinishedAt == nil || verification.StartedAt == nil || verification.FinishedAt == nil || verification.StartedAt.Before(*implementation.FinishedAt) || implementation.FinishedAt.Before(*implementation.StartedAt) || verification.FinishedAt.Before(*verification.StartedAt) {
		return failResult(result, now, errors.New("implementation and verification timing is absent or not serial"))
	}
	if implementation.TokenStatus != "AVAILABLE" || verification.TokenStatus != "AVAILABLE" || implementation.TokenTotal <= 0 || verification.TokenTotal <= 0 {
		return failResult(result, now, errors.New("implementation and verification token deltas were not independently recorded"))
	}
	if observation.RunScope != "fixture tracked file" || observation.RunCheckKind != "GIT_ASSERTION" || observation.RunCheckCommand != "git diff --check" {
		return failResult(result, now, errors.New("confirmed verification scope/check was not preserved in the read model"))
	}
	if implementation.EndSnapshot == "" || implementation.EndSnapshot != verification.StartSnapshot || verification.StartSnapshot == "" || verification.StartSnapshot != verification.EndSnapshot || len(verification.ChangedFiles) != 0 {
		return failResult(result, now, errors.New("verification did not preserve the immutable implementation snapshot"))
	}
	contents, readErr := os.ReadFile(filepath.Join(implementation.WorktreePath, request.ExpectedPath))
	if readErr != nil || string(contents) != request.ExpectedContent || !slices.Contains(implementation.ChangedFiles, request.ExpectedPath) {
		return failResult(result, now, fmt.Errorf("RUN implementation evidence/content is incorrect: contents=%q err=%v files=%v", contents, readErr, implementation.ChangedFiles))
	}
	none := observation.NoneImplementation
	if observation.NoneOutcome != "SUCCEEDED" || observation.NoneTaskStatus != "SUCCEEDED" || observation.NoneMode != "NONE" || observation.NoneWaiverSource != "USER_WAIVED" || observation.NoneWaiverReason == "" || observation.NoneVerificationRunID != "" || none.Status != "SUCCEEDED" || none.Purpose != "IMPLEMENTATION" || none.TaskID != observation.NoneTaskID || none.ExecutionProposalID != observation.NoneExecutionID {
		return failResult(result, now, errors.New("NONE plan did not preserve the explicit user waiver or created a verification Run"))
	}
	if none.StartedAt == nil || none.FinishedAt == nil || none.FinishedAt.Before(*none.StartedAt) || none.TokenStatus != "AVAILABLE" || none.TokenTotal <= 0 {
		return failResult(result, now, errors.New("NONE plan implementation timing/token accounting is incomplete"))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "real Codex completed a serial implementation/verification plan and honored a separate USER_WAIVED plan without a fake Run", now().UTC()
	return result
}

func (s Suite) runExecutionScope(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateExecutionScope, now().UTC(), fixture)
	request := newRequest(GateExecutionScope, fixture)
	sourceBefore, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.ExecutionScope(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	sourceAfter, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	if sourceBefore.HEAD != sourceAfter.HEAD || sourceBefore.Status != sourceAfter.Status || sourceBefore.Diff != sourceAfter.Diff {
		return failResult(result, now, errors.New("execution-scope cases changed the source repository"))
	}

	valid := observation.ValidPaths
	result.Evidence["valid_run_id"] = valid.Implementation.ID
	if valid.ScopeKind != "PATHS" || !slices.Equal(valid.ScopePaths, []string{"allowed"}) || valid.Outcome != "SUCCEEDED" || valid.TaskStatus != "SUCCEEDED" || valid.Implementation.Status != "SUCCEEDED" || valid.Verification.Status != "SUCCEEDED" || valid.Verification.VerificationStatus != "PASSED" || valid.VerificationRunID == "" {
		return failResult(result, now, fmt.Errorf("valid PATHS case did not complete with verification: %+v", valid))
	}
	if !slices.Equal(valid.Implementation.ChangedFiles, []string{"allowed/target.txt"}) || len(valid.Verification.ChangedFiles) != 0 || valid.Implementation.DiffArtifactID == "" {
		return failResult(result, now, fmt.Errorf("valid PATHS evidence is incorrect: %+v", valid.Implementation))
	}
	validTruth, validTruthErr := fixture.Inspect(ctx, valid.Implementation.WorktreePath)
	if validTruthErr != nil || !slices.Equal(validTruth.ChangedFiles, valid.Implementation.ChangedFiles) {
		return failResult(result, now, fmt.Errorf("valid PATHS evidence differs from Git truth: truth=%+v err=%v", validTruth, validTruthErr))
	}
	validContents, validErr := os.ReadFile(filepath.Join(valid.Implementation.WorktreePath, "allowed", "target.txt"))
	if validErr != nil || string(validContents) != "allowed by PATHS\n" {
		return failResult(result, now, fmt.Errorf("valid PATHS content=%q err=%v", validContents, validErr))
	}

	violation := observation.Violation
	result.Evidence["violation_run_id"] = violation.Implementation.ID
	result.Evidence["violation_failure"] = violation.Implementation.FailureCode
	result.Evidence["violation_paths"] = strings.Join(violation.Implementation.ChangedFiles, ",")
	result.Evidence["violation_protocol_approvals"] = strings.Join(violation.ApprovedProtocolActionIDs, ",")
	if violation.ScopeKind != "PATHS" || !slices.Equal(violation.ScopePaths, []string{"allowed"}) || violation.Outcome != "FAILED" || violation.TaskStatus != "FAILED" || violation.Implementation.Status != "FAILED" || violation.Implementation.FailureCode != "OUT_OF_SCOPE_CHANGE" || violation.VerificationRunID != "" {
		return failResult(result, now, fmt.Errorf("out-of-scope case did not fail before verification: %+v", violation))
	}
	if !strings.Contains(violation.Implementation.FailureMessage, "outside/protected.txt") || !slices.Equal(violation.Implementation.ChangedFiles, []string{"allowed/target.txt", "outside/protected.txt"}) || violation.Implementation.DiffArtifactID == "" {
		return failResult(result, now, fmt.Errorf("out-of-scope failure/evidence is incomplete: %+v", violation.Implementation))
	}
	violationTruth, truthErr := fixture.Inspect(ctx, violation.Implementation.WorktreePath)
	if truthErr != nil || !slices.Equal(violationTruth.ChangedFiles, violation.Implementation.ChangedFiles) {
		return failResult(result, now, fmt.Errorf("out-of-scope evidence differs from Git truth: truth=%+v err=%v", violationTruth, truthErr))
	}
	protectedContents, protectedErr := os.ReadFile(filepath.Join(violation.Implementation.WorktreePath, "outside", "protected.txt"))
	if protectedErr != nil || string(protectedContents) != "deliberate unauthorized mutation\n" {
		return failResult(result, now, fmt.Errorf("out-of-scope mutation was reverted or absent: content=%q err=%v", protectedContents, protectedErr))
	}

	repository := observation.Repository
	result.Evidence["repository_run_id"] = repository.Implementation.ID
	if repository.ScopeKind != "REPOSITORY" || len(repository.ScopePaths) != 0 || repository.Outcome != "SUCCEEDED" || repository.TaskStatus != "SUCCEEDED" || repository.Implementation.Status != "SUCCEEDED" || repository.VerificationRunID != "" {
		return failResult(result, now, fmt.Errorf("explicit REPOSITORY case did not succeed: %+v", repository))
	}
	if !slices.Equal(repository.Implementation.ChangedFiles, []string{"allowed/target.txt", "outside/protected.txt"}) || repository.Implementation.DiffArtifactID == "" {
		return failResult(result, now, fmt.Errorf("REPOSITORY evidence is incorrect: %+v", repository.Implementation))
	}
	repositoryTruth, repositoryTruthErr := fixture.Inspect(ctx, repository.Implementation.WorktreePath)
	if repositoryTruthErr != nil || !slices.Equal(repositoryTruth.ChangedFiles, repository.Implementation.ChangedFiles) {
		return failResult(result, now, fmt.Errorf("REPOSITORY evidence differs from Git truth: truth=%+v err=%v", repositoryTruth, repositoryTruthErr))
	}
	repositoryTarget, targetErr := os.ReadFile(filepath.Join(repository.Implementation.WorktreePath, "allowed", "target.txt"))
	repositoryProtected, protectedErr := os.ReadFile(filepath.Join(repository.Implementation.WorktreePath, "outside", "protected.txt"))
	if targetErr != nil || protectedErr != nil || string(repositoryTarget) != "repository allowed target\n" || string(repositoryProtected) != "repository allowed protected\n" {
		return failResult(result, now, fmt.Errorf("REPOSITORY contents are incorrect: target=%q/%v protected=%q/%v", repositoryTarget, targetErr, repositoryProtected, protectedErr))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "real Codex PATHS and REPOSITORY scopes were enforced against immutable per-Run Git evidence", now().UTC()
	return result
}

func (s Suite) runTaskFirstScopeReview(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateTaskFirstScopeReview, now().UTC(), fixture)
	request := newRequest(GateTaskFirstScopeReview, fixture)
	sourceBefore, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.TaskFirstScopeReview(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	sourceAfter, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	if sourceBefore.HEAD != sourceAfter.HEAD || sourceBefore.Status != sourceAfter.Status || sourceBefore.Diff != sourceAfter.Diff {
		return failResult(result, now, errors.New("Task-first Scope Review changed the source repository"))
	}

	review, implementation, verification := observation.Review, observation.Implementation, observation.Verification
	result.Evidence["task_id"] = observation.TaskID
	result.Evidence["review_run_id"] = review.ID
	result.Evidence["task_plan_id"] = observation.TaskPlanID
	result.Evidence["execution_id"] = observation.ExecutionID
	result.Evidence["implementation_run_id"] = implementation.ID
	result.Evidence["verification_run_id"] = verification.ID
	result.Evidence["workspace_id"] = review.WorkspaceID
	result.Evidence["worktree_id"] = review.WorktreeID
	result.Evidence["thread_id"] = review.ThreadID
	result.Evidence["search_match"] = fmt.Sprintf("%s:%d:%s", observation.SearchPath, observation.SearchLine, observation.SearchSnippet)
	result.Evidence["review_cached_input_tokens"] = fmt.Sprint(review.TokenCachedInput)
	result.Evidence["implementation_cached_input_tokens"] = fmt.Sprint(implementation.TokenCachedInput)
	result.Evidence["verification_cached_input_tokens"] = fmt.Sprint(verification.TokenCachedInput)
	result.Evidence["pending_plan_apply_route_closed"] = fmt.Sprint(observation.PendingTaskPlanApplyRouteClosed)
	result.Evidence["pending_execution_start_route_closed"] = fmt.Sprint(observation.PendingExecutionStartRouteClosed)
	result.Evidence["runs_after_plan_continuation"] = fmt.Sprint(observation.RunsAfterPlanContinuation)
	result.Evidence["repeated_plan_converged"] = fmt.Sprint(observation.RepeatedPlanConverged)
	result.Evidence["repeated_execute_converged"] = fmt.Sprint(observation.RepeatedExecuteConverged)
	result.Evidence["rejected_task_id"] = observation.RejectedTaskID

	if observation.TaskID == "" || observation.InitialTaskStatus != "PLANNING" || observation.InitialRunCount != 0 {
		return failResult(result, now, fmt.Errorf("Task-first creation was not visible as PLANNING with zero Runs: task=%s status=%s runs=%d", observation.TaskID, observation.InitialTaskStatus, observation.InitialRunCount))
	}
	if observation.SearchPath != "tracked.txt" || observation.SearchLine != 1 || observation.SearchSnippet != "base line" || observation.SearchTruncated {
		return failResult(result, now, fmt.Errorf("bounded fixed repository search was not deterministic: %s:%d %q truncated=%t", observation.SearchPath, observation.SearchLine, observation.SearchSnippet, observation.SearchTruncated))
	}
	if review.ID == "" || review.TaskID != observation.TaskID || review.Purpose != "SCOPE_REVIEW" || review.Status != "SUCCEEDED" || review.ExecutionProposalID != "" {
		return failResult(result, now, fmt.Errorf("Scope Review Run provenance/status is inconsistent: %+v", review))
	}
	if review.StartSnapshot == "" || review.StartSnapshot != review.EndSnapshot || len(review.ChangedFiles) != 0 || review.DiffArtifactID == "" {
		return failResult(result, now, fmt.Errorf("Scope Review was not immutably evidenced: start=%s end=%s files=%v artifact=%s", review.StartSnapshot, review.EndSnapshot, review.ChangedFiles, review.DiffArtifactID))
	}
	if strings.TrimSpace(observation.ReviewResult) == "" || observation.ReviewResult != observation.ReviewResultAfter {
		return failResult(result, now, errors.New("Scope Review result was empty or was not persisted exactly"))
	}
	if observation.TaskPlanID == "" || observation.PendingTaskPlanStatus != "PENDING_CONFIRMATION" || !observation.PendingTaskPlanApplyRouteClosed || observation.AppliedTaskPlanStatus != "APPLIED" || observation.AppliedTaskStatus != "READY" {
		return failResult(result, now, fmt.Errorf("legacy TaskPlan apply bypass closure is inconsistent: id=%s pending=%s route_closed=%t applied=%s task=%s", observation.TaskPlanID, observation.PendingTaskPlanStatus, observation.PendingTaskPlanApplyRouteClosed, observation.AppliedTaskPlanStatus, observation.AppliedTaskStatus))
	}
	if observation.RunsAfterPlanContinuation != 1 || !observation.RepeatedPlanConverged {
		return failResult(result, now, fmt.Errorf("MCP Task continuation was not convergent: runs=%d repeated_converged=%t", observation.RunsAfterPlanContinuation, observation.RepeatedPlanConverged))
	}
	if observation.ExecutionID == "" || !observation.PendingExecutionStartRouteClosed || observation.ExecutionOutcome != "SUCCEEDED" || observation.CompletedTaskStatus != "SUCCEEDED" {
		return failResult(result, now, fmt.Errorf("legacy execution start bypass closure is inconsistent: id=%s route_closed=%t outcome=%s task=%s", observation.ExecutionID, observation.PendingExecutionStartRouteClosed, observation.ExecutionOutcome, observation.CompletedTaskStatus))
	}
	if !observation.RepeatedExecuteConverged {
		return failResult(result, now, errors.New("MCP Execute continuation did not converge on the existing Run"))
	}
	if implementation.ID == "" || verification.ID == "" || review.ID == implementation.ID || review.ID == verification.ID || implementation.ID == verification.ID || implementation.Purpose != "IMPLEMENTATION" || verification.Purpose != "VERIFICATION" || implementation.ExecutionProposalID != observation.ExecutionID || verification.ExecutionProposalID != observation.ExecutionID {
		return failResult(result, now, errors.New("Scope Review, implementation, and verification did not use distinct Runs with correct provenance"))
	}
	if review.WorkspaceID == "" || review.WorkspaceID != implementation.WorkspaceID || review.WorkspaceID != verification.WorkspaceID || review.WorktreeID == "" || review.WorktreeID != implementation.WorktreeID || review.WorktreeID != verification.WorktreeID || !samePath(review.WorktreePath, implementation.WorktreePath) || !samePath(review.WorktreePath, verification.WorktreePath) || review.ThreadID == "" || review.ThreadID != implementation.ThreadID || review.ThreadID != verification.ThreadID {
		return failResult(result, now, errors.New("Scope Review, implementation, and verification did not retain one Workspace/worktree/thread"))
	}
	if review.TurnID == "" || implementation.TurnID == "" || verification.TurnID == "" || review.TurnID == implementation.TurnID || review.TurnID == verification.TurnID || implementation.TurnID == verification.TurnID {
		return failResult(result, now, errors.New("Scope Review, implementation, and verification did not use distinct Codex turns"))
	}
	if implementation.StartSnapshot != review.EndSnapshot || implementation.EndSnapshot == "" || verification.StartSnapshot != implementation.EndSnapshot || verification.StartSnapshot != verification.EndSnapshot {
		return failResult(result, now, errors.New("Scope Review-to-implementation-to-verification snapshot continuity is not exact"))
	}
	if implementation.Status != "SUCCEEDED" || verification.Status != "SUCCEEDED" || verification.VerificationStatus != "PASSED" || verification.VerificationChecks != 1 || !slices.Equal(implementation.ChangedFiles, []string{"tracked.txt"}) || len(verification.ChangedFiles) != 0 {
		return failResult(result, now, fmt.Errorf("implementation/verification result is inconsistent: implementation=%+v verification=%+v", implementation, verification))
	}
	for _, observed := range []PlanRunObservation{review, implementation, verification} {
		if observed.TokenStatus != "AVAILABLE" || observed.TokenTotal <= 0 || observed.TokenCachedInput < 0 {
			return failResult(result, now, fmt.Errorf("Run %s lacks usable token/cache telemetry: %s total=%d cached=%d", observed.ID, observed.TokenStatus, observed.TokenTotal, observed.TokenCachedInput))
		}
	}
	if observation.RejectedTaskID == "" || observation.RejectedTaskStatus != "PLANNING" || observation.RejectedTaskPlanStatus != "REJECTED" || observation.RejectedTaskRunCount != 0 {
		return failResult(result, now, fmt.Errorf("rejected TaskPlan changed Task/run lifecycle: task=%s status=%s plan=%s runs=%d", observation.RejectedTaskID, observation.RejectedTaskStatus, observation.RejectedTaskPlanStatus, observation.RejectedTaskRunCount))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "Task-first planning, deterministic MCP card continuations, immutable Scope Review, and one-thread execution continuity passed against real Codex", now().UTC()
	return result
}

func DefaultDeferredAdapter() DeferredAdapter {
	return DeferredAdapter{Reasons: map[GateID]string{
		GateNormalExecution:      "MCP can authorize and observe Runs, but no public black-box fixture-registration and worktree/evidence observation adapter is available",
		GateRuntimeApproval:      "MCP exposes correlated runtime approval decisions, but no public black-box fixture-registration adapter drives the real approval scenario",
		GateActiveInterruption:   "the restartable MCP service exists, but the harness lacks a public black-box fixture bootstrap and recovery-observation adapter",
		GateApprovalInterruption: "durable approval recovery exists, but the harness lacks a public black-box fixture bootstrap and restart/recovery adapter",
		GateParallelIsolation:    "parallel runtime ownership exists, but the harness lacks a public black-box fixture bootstrap and observation adapter",
		GateWorkspaceContinuity:  "workspace continuity requires a real Bridge and Codex black-box adapter",
		GateWorkVerification:     "implementation and verification planning requires a real Bridge and Codex black-box adapter",
		GateExecutionScope:       "execution-scope enforcement requires a real Bridge and Codex black-box adapter",
		GateTaskFirstScopeReview: "Task-first planning and Scope Review continuity require a real Bridge and Codex black-box adapter",
	}}
}

func (s Suite) runNormal(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	started := now().UTC()
	result := baseResult(GateNormalExecution, started, fixture)
	request := newRequest(GateNormalExecution, fixture)
	before, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.NormalExecution(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	after, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	worktree, err := fixture.Inspect(ctx, observation.WorktreePath)
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_id"] = observation.RunID
	result.Evidence["worktree_path"] = observation.WorktreePath
	result.Evidence["worktree_status"] = worktree.Status
	result.Evidence["worktree_diff"] = worktree.Diff
	result.Evidence["source_status"] = after.Status
	result.Evidence["source_diff"] = after.Diff
	result.Evidence["agent_claim"] = observation.AgentClaim
	result.Evidence["diff_artifact_id"] = observation.DiffArtifactID
	result.Evidence["thread_id"] = observation.ThreadID
	result.Evidence["token_status"] = observation.TokenStatus
	if before.HEAD != after.HEAD || before.Status != after.Status || before.Diff != after.Diff {
		return failResult(result, now, errors.New("source repository changed during execution"))
	}
	if samePath(observation.WorktreePath, fixture.SourceRepo) || !withinPath(observation.WorktreePath, request.WorktreeRoot) {
		return failResult(result, now, fmt.Errorf("adapter reported non-owned worktree %q", observation.WorktreePath))
	}
	contents, err := os.ReadFile(filepath.Join(observation.WorktreePath, request.ExpectedPath))
	if err != nil || string(contents) != request.ExpectedContent {
		return failResult(result, now, fmt.Errorf("expected modification missing or incorrect: contents=%q err=%v", contents, err))
	}
	if !ordered(observation.Timeline, CheckpointCodexCompleted, CheckpointEvidenceCollecting, CheckpointRunSucceeded) {
		return failResult(result, now, fmt.Errorf("completion/evidence/success lifecycle not proven: %v", observation.Timeline))
	}
	if !slices.Contains(worktree.ChangedFiles, request.ExpectedPath) {
		return failResult(result, now, fmt.Errorf("independent Git evidence omits %s", request.ExpectedPath))
	}
	if observation.ThreadID == "" || observation.DiffArtifactID == "" {
		return failResult(result, now, errors.New("real Codex thread or durable diff artifact was not observed"))
	}
	if observation.BaseCommit != fixture.BaseCommit || observation.HeadCommit != worktree.HEAD || observation.Dirty != (worktree.Status != "") || !slices.Equal(observation.ChangedFiles, worktree.ChangedFiles) || observation.Diff != worktree.Diff {
		return failResult(result, now, errors.New("persisted Evidence does not match independent Git truth"))
	}
	if observation.TokenStatus == "" || observation.TokenStatus == "PENDING" {
		return failResult(result, now, fmt.Errorf("terminal run retained unresolved token accounting: %s", observation.TokenStatus))
	}
	result.Status = StatusPass
	result.Reason = "real execution satisfied independent source, worktree, lifecycle, and Git evidence assertions"
	result.FinishedAt = now().UTC()
	return result
}

func (s Suite) runApproval(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateRuntimeApproval, now().UTC(), fixture)
	observation, err := adapter.RuntimeApproval(ctx, newRequest(GateRuntimeApproval, fixture), "DECLINE")
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_id"] = observation.RunID
	result.Evidence["approval_id"] = observation.ApprovalID
	result.Evidence["final_status"] = observation.FinalStatus
	if observation.RunID == "" || observation.ApprovalID == "" || observation.CorrelatedRunID != observation.RunID {
		return failResult(result, now, errors.New("approval ownership/correlation was not proven"))
	}
	if observation.Decision != "DECLINE" || !ordered(observation.Timeline, CheckpointWaitingApproval, CheckpointApprovalDecided, CheckpointApprovalDeclined, CheckpointApprovalDelivered) {
		return failResult(result, now, errors.New("WAITING_APPROVAL to DECLINE lifecycle was not proven"))
	}
	if strings.Contains(observation.FinalStatus, "FAIL") {
		return failResult(result, now, fmt.Errorf("DECLINE automatically failed run: %s", observation.FinalStatus))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "runtime approval decline and ownership semantics verified", now().UTC()
	return result
}

func (s Suite) runActiveInterruption(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateActiveInterruption, now().UTC(), fixture)
	request := newRequest(GateActiveInterruption, fixture)
	var err error
	spec, err := adapter.ActiveInterruptionCommand(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	child, err := StartChild(spec, request.ArtifactDir, "bridge-active")
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["first_stdout"] = child.StdoutPath
	result.Evidence["first_stderr"] = child.StderrPath
	if err := adapter.WaitUntilActive(ctx, request); err != nil {
		_ = child.KillNonGracefully()
		_ = child.Wait()
		return failResult(result, now, err)
	}
	if err := child.KillNonGracefully(); err != nil {
		return failResult(result, now, err)
	}
	_ = child.Wait() // an exit error is expected after a non-graceful kill
	restartSpec, err := adapter.RestartCommand(ctx, request)
	if err != nil {
		return failResult(result, now, err)
	}
	restarted, err := StartChild(restartSpec, request.ArtifactDir, "bridge-restart")
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["restart_stdout"] = restarted.StdoutPath
	result.Evidence["restart_stderr"] = restarted.StderrPath
	observation, err := adapter.ObserveActiveRecovery(ctx, request, restarted)
	_ = restarted.KillNonGracefully()
	_ = restarted.Wait()
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_id"] = observation.RunID
	result.Evidence["recovery_status"] = observation.FinalStatus
	result.Evidence["failure_code"] = observation.FailureCode
	result.Evidence["worktree_path"] = observation.WorktreePath
	if !samePath(observation.DataRoot, fixture.DataRoot) || observation.FinalStatus != "INFRASTRUCTURE_FAILED" || observation.FailureCode != "bridge_restart_interrupted" {
		return failResult(result, now, errors.New("active recovery did not record bridge_restart_interrupted infrastructure failure"))
	}
	if observation.FinishedAt == "" || observation.WorktreePath == "" || observation.ThreadID == "" || observation.LiveSession || observation.TokenStatus != "UNAVAILABLE" || observation.RecoveryEventCount != 1 {
		return failResult(result, now, fmt.Errorf("active recovery metadata is incomplete or fabricated: %+v", observation))
	}
	if _, err := os.Stat(observation.WorktreePath); err != nil {
		return failResult(result, now, fmt.Errorf("recovered worktree was not retained: %w", err))
	}
	second, err := StartChild(restartSpec, request.ArtifactDir, "bridge-restart-idempotent")
	if err != nil {
		return failResult(result, now, err)
	}
	again, err := adapter.ObserveActiveRecovery(ctx, request, second)
	_ = second.KillNonGracefully()
	_ = second.Wait()
	if err != nil {
		return failResult(result, now, err)
	}
	if again.FinishedAt != observation.FinishedAt || again.RecoveryEventCount != 1 || again.FinalStatus != observation.FinalStatus || again.WorktreePath != observation.WorktreePath || again.ThreadID != observation.ThreadID || again.TokenStatus != observation.TokenStatus || again.LiveSession {
		return failResult(result, now, errors.New("second restart changed the recovered Run"))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "non-graceful interruption and same DataRoot recovery verified", now().UTC()
	return result
}

func (s Suite) runApprovalInterruption(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateApprovalInterruption, now().UTC(), fixture)
	request := newRequest(GateApprovalInterruption, fixture)
	var err error
	spec, err := adapter.ApprovalInterruptionCommand(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	child, err := StartChild(spec, request.ArtifactDir, "bridge-waiting-approval")
	if err != nil {
		return failResult(result, now, err)
	}
	if err := adapter.WaitUntilWaitingApproval(ctx, request); err != nil {
		_ = child.KillNonGracefully()
		_ = child.Wait()
		return failResult(result, now, err)
	}
	if err := child.KillNonGracefully(); err != nil {
		return failResult(result, now, err)
	}
	_ = child.Wait()
	restartSpec, err := adapter.RestartCommand(ctx, request)
	if err != nil {
		return failResult(result, now, err)
	}
	restarted, err := StartChild(restartSpec, request.ArtifactDir, "bridge-approval-restart")
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.ObserveApprovalRecovery(ctx, request, restarted)
	_ = restarted.KillNonGracefully()
	_ = restarted.Wait()
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_id"] = observation.RunID
	result.Evidence["recovery_status"] = observation.FinalStatus
	result.Evidence["data_root"] = observation.DataRoot
	result.Evidence["approval_status"] = observation.ApprovalStatus
	if !samePath(observation.DataRoot, fixture.DataRoot) || observation.FinalStatus != "INFRASTRUCTURE_FAILED" || observation.FailureCode != "bridge_restart_interrupted" {
		return failResult(result, now, errors.New("approval recovery did not record bridge_restart_interrupted infrastructure failure"))
	}
	if observation.ApprovalStatus != "EXPIRED" || observation.ApprovalDecision != "" || observation.FinishedAt == "" || observation.RecoveryEventCount != 1 || observation.LiveSession || observation.TokenStatus != "UNAVAILABLE" {
		return failResult(result, now, fmt.Errorf("waiting-approval recovery metadata is incorrect: %+v", observation))
	}
	if observation.WorktreePath == "" {
		return failResult(result, now, errors.New("waiting-approval recovery omitted the retained worktree"))
	}
	if _, err := os.Stat(observation.WorktreePath); err != nil {
		return failResult(result, now, fmt.Errorf("waiting-approval worktree was not retained: %w", err))
	}
	second, err := StartChild(restartSpec, request.ArtifactDir, "bridge-approval-restart-idempotent")
	if err != nil {
		return failResult(result, now, err)
	}
	again, err := adapter.ObserveApprovalRecovery(ctx, request, second)
	_ = second.KillNonGracefully()
	_ = second.Wait()
	if err != nil {
		return failResult(result, now, err)
	}
	if again.FinishedAt != observation.FinishedAt || again.RecoveryEventCount != 1 || again.ApprovalStatus != observation.ApprovalStatus || again.ApprovalDecision != observation.ApprovalDecision {
		return failResult(result, now, errors.New("second restart changed recovered approval state"))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "waiting-approval interruption recovery verified", now().UTC()
	return result
}

func (s Suite) runParallelIsolation(ctx context.Context, fixture *Fixture, adapter Adapter, now Clock) GateResult {
	result := baseResult(GateParallelIsolation, now().UTC(), fixture)
	request := newRequest(GateParallelIsolation, fixture)
	sourceBefore, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	observation, err := adapter.ParallelIsolation(ctx, request)
	if unavailableResult(&result, now, err) {
		return result
	}
	if err != nil {
		return failResult(result, now, err)
	}
	sourceAfter, err := fixture.SourceStatus(ctx)
	if err != nil {
		return failResult(result, now, err)
	}
	worktreeA, err := fixture.Inspect(ctx, observation.WorktreeA)
	if err != nil {
		return failResult(result, now, err)
	}
	worktreeB, err := fixture.Inspect(ctx, observation.WorktreeB)
	if err != nil {
		return failResult(result, now, err)
	}
	result.Evidence["run_a"] = observation.RunAID
	result.Evidence["run_b"] = observation.RunBID
	result.Evidence["same_task_rejection"] = observation.SameTaskRejectionCode
	if observation.RunAID == "" || observation.RunBID == "" || observation.RunAID == observation.RunBID || observation.TaskAID == observation.TaskBID {
		return failResult(result, now, errors.New("parallel runs or tasks were not independently identified"))
	}
	if sourceBefore.HEAD != sourceAfter.HEAD || sourceBefore.Status != sourceAfter.Status || sourceBefore.Diff != sourceAfter.Diff {
		return failResult(result, now, errors.New("parallel execution changed the source repository"))
	}
	if samePath(observation.WorktreeA, observation.WorktreeB) || !withinPath(observation.WorktreeA, request.WorktreeRoot) || !withinPath(observation.WorktreeB, request.WorktreeRoot) || observation.ThreadA == "" || observation.ThreadA == observation.ThreadB {
		return failResult(result, now, errors.New("parallel runs shared a worktree or Codex session"))
	}
	if observation.RunAStatusWhileBActive != "WAITING_APPROVAL" || observation.RunAFinalStatus != "SUCCEEDED" || observation.RunBFinalStatus != "SUCCEEDED" {
		return failResult(result, now, errors.New("WAITING_APPROVAL on Run A blocked or serialized Run B"))
	}
	if observation.SameTaskRejectionCode != "TASK_ALREADY_HAS_ACTIVE_RUN" {
		return failResult(result, now, errors.New("a second active Run for the same Task was not rejected"))
	}
	contents, readErr := os.ReadFile(filepath.Join(observation.WorktreeB, request.ExpectedPath))
	if readErr != nil || string(contents) != request.ExpectedContent || len(worktreeA.ChangedFiles) != 0 || !slices.Equal(observation.RunAChangedFiles, worktreeA.ChangedFiles) || !slices.Equal(observation.RunBChangedFiles, worktreeB.ChangedFiles) || observation.RunATokenStatus == "PENDING" || observation.RunBTokenStatus == "PENDING" || !slices.Contains(observation.RunBChangedFiles, request.ExpectedPath) {
		return failResult(result, now, errors.New("parallel token/evidence accounting was not isolated"))
	}
	result.Status, result.Reason, result.FinishedAt = StatusPass, "parallel Tasks used isolated worktrees, Codex sessions, approval state, token accounting, and Evidence", now().UTC()
	return result
}

func newRequest(id GateID, fixture *Fixture) GateRequest {
	return GateRequest{GateID: id, SourceRepo: fixture.SourceRepo, BaseCommit: fixture.BaseCommit, DataRoot: fixture.DataRoot, WorktreeRoot: filepath.Join(fixture.DataRoot, "worktrees"), ArtifactDir: filepath.Join(fixture.Artifacts, string(id)), ExpectedPath: "tracked.txt", ExpectedContent: "modified by real Dev Bridge gate\n"}
}

func baseResult(id GateID, started time.Time, fixture *Fixture) GateResult {
	return GateResult{GateID: id, Status: StatusFail, Reason: "gate did not complete", StartedAt: started, Evidence: Evidence{"fixture_repo": fixture.SourceRepo, "data_root": fixture.DataRoot, "artifact_root": fixture.Artifacts, "base_commit": fixture.BaseCommit}}
}

func failResult(result GateResult, now Clock, err error) GateResult {
	result.Status, result.Reason, result.FinishedAt = StatusFail, err.Error(), now().UTC()
	return result
}

func unavailableResult(result *GateResult, now Clock, err error) bool {
	var unavailable *CapabilityUnavailableError
	if !errors.As(err, &unavailable) {
		return false
	}
	result.Status, result.Reason, result.FinishedAt = StatusBlocked, unavailable.Reason, now().UTC()
	return true
}

func ordered(values []Checkpoint, expected ...Checkpoint) bool {
	position := 0
	for _, value := range values {
		if position < len(expected) && value == expected[position] {
			position++
		}
	}
	return position == len(expected)
}

func samePath(a, b string) bool {
	left, err := canonicalPath(a)
	if err != nil {
		return false
	}
	right, err := canonicalPath(b)
	return err == nil && left == right
}

func withinPath(path, root string) bool {
	absolutePath, err := canonicalPath(path)
	if err != nil {
		return false
	}
	absoluteRoot, err := canonicalPath(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
