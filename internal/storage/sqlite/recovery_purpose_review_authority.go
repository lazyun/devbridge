package sqlite

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// A completed Review purpose resolves only its exact historical source. This
// reader never grants current start permission or rewrites an UNKNOWN boundary.
func recoveryReviewPurposeResolvedSourceRunsTx(ctx context.Context, q definitionQuerier, areaID string) ([]string, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(c.successor_review_id),'[]') FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_link_history l ON l.link_id=c.purpose_link_id AND l.link_ordinal=c.purpose_link_ordinal WHERE l.feature_area_id=? AND c.source_owner_kind='REVIEW'`, areaID).Scan(&raw); err != nil {
		return nil, err
	}
	var reviews []string
	if err := json.Unmarshal([]byte(raw), &reviews); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var sources []string
	for _, id := range reviews {
		if id == "" || seen[id] {
			return nil, rs.ErrPurposeReceiptMismatch
		}
		seen[id] = true
		source, err := readRecoveryReviewPurposeEvidenceTx(ctx, q, areaID, id)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func readRecoveryReviewPurposeEvidenceTx(ctx context.Context, q definitionQuerier, areaID, successorID string) (string, error) {
	var in rs.PurposeConsumeInput
	var owner, variantID, proposalHash, revision, executionID, runID, linkID string
	var ordinal int
	err := q.QueryRowContext(ctx, `SELECT intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind,selected_variant_id,proposal_hash,successor_task_definition_revision_id,successor_execution_id,successor_run_id,purpose_link_id,purpose_link_ordinal FROM recovery_purpose_confirmation_consumptions WHERE successor_review_id=?`, successorID).Scan(&in.AISessionID, &in.IntentID, &in.Version, &in.ContentHash, &in.Action, &in.RequestID, &in.PayloadHash, &owner, &variantID, &proposalHash, &revision, &executionID, &runID, &linkID, &ordinal)
	if err != nil {
		return "", err
	}
	if owner != "REVIEW" || in.Action != rs.ApplyConfirmedReviewPurpose || in.AISessionID == "" || in.RequestID == "" || in.Version < 1 || in.PayloadHash != in.ComputePayloadHash() || revision != "" || executionID != "" || runID != "" || linkID == "" || ordinal < 1 {
		return "", rs.ErrPurposeReceiptMismatch
	}
	identity := workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	if successorID != "review_recovery_purpose_"+identity {
		return "", rs.ErrPurposeReceiptMismatch
	}
	var raw, prepareHash, project string
	if err = q.QueryRowContext(ctx, `SELECT intent_json,prepare_hash,project_id FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, in.IntentID, in.AISessionID).Scan(&raw, &prepareHash, &project); err != nil {
		return "", err
	}
	var intent confirmation.Intent
	if json.Unmarshal([]byte(raw), &intent) != nil {
		return "", rs.ErrPurposeReceiptMismatch
	}
	content, _ := json.Marshal(intent.Content)
	if intent.IntentID != in.IntentID || intent.AISessionID != in.AISessionID || intent.Version != in.Version || intent.ContentHash != in.ContentHash || prepareHash != in.ContentHash || workflowHash(string(content)) != in.ContentHash || intent.ProjectID != project || intent.Purpose != confirmation.RecoveryPurpose || intent.Action != confirmation.ConfirmRecoveryPurpose || intent.State != "COMPLETED" || intent.Commit == nil || intent.Commit.VariantID != variantID || intent.Content.RecoveryPurpose == nil {
		return "", rs.ErrPurposeReceiptMismatch
	}
	p := intent.Content.RecoveryPurpose.Proposal
	if p.OwnerKind != owner || p.OwnerID != p.SourceReviewID || p.SourceExecutionID != "" || p.SourceAISessionID != intent.AISessionID || p.FeatureAreaID != areaID || p.SubmittedHash != proposalHash || p.ComputeSubmittedHash() != proposalHash {
		return "", rs.ErrPurposeReceiptMismatch
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_receipts r JOIN recovery_purpose_link_history l ON l.link_id=r.purpose_link_id WHERE r.intent_ai_session_id=? AND r.intent_id=? AND r.intent_version=? AND r.intent_content_hash=? AND r.commit_request_id=? AND r.selected_variant_id=? AND r.outcome='COMMITTED' AND r.source_owner_kind='REVIEW' AND r.source_owner_id=? AND r.proposal_hash=? AND r.purpose_link_id=? AND r.purpose_link_ordinal=? AND r.recovery_id=? AND r.recovery_generation=? AND l.link_ordinal=r.purpose_link_ordinal AND l.intent_ai_session_id=r.intent_ai_session_id AND l.intent_id=r.intent_id AND l.intent_version=r.intent_version AND l.intent_content_hash=r.intent_content_hash AND l.proposal_hash=r.proposal_hash AND l.recovery_id=r.recovery_id AND l.recovery_generation=r.recovery_generation AND l.source_owner_kind=r.source_owner_kind AND l.source_owner_id=r.source_owner_id AND l.task_id=? AND l.feature_area_id=? AND l.source_execution_id='' AND l.source_review_id=? AND l.task_definition_revision_id=? AND l.source_operation_id=? AND l.source_run_id=? AND l.source_turn_id=? AND l.runtime_epoch=? AND l.runtime_workset_hash=? AND l.assessment_hash=?)`, in.AISessionID, in.IntentID, in.Version, in.ContentHash, intent.Commit.RequestID, variantID, p.OwnerID, proposalHash, linkID, ordinal, p.RecoveryID, p.RecoveryGeneration, p.TaskID, areaID, p.SourceReviewID, p.TaskDefinitionRevisionID, p.SourceOperationID, p.SourceRunID, p.SourceTurnID, p.RuntimeEpoch, p.RuntimeWorksetHash, p.AssessmentHash).Scan(&exact)
	if err != nil {
		return "", err
	}
	if !exact {
		return "", rs.ErrPurposeReceiptMismatch
	}
	sub, err := confirmationSubmissionTx(ctx, q, intent, intent.Commit.RequestID)
	if err != nil {
		return "", err
	}
	var typedHash, submitted string
	if err = q.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=?`, in.AISessionID, intent.Commit.RequestID, in.IntentID).Scan(&typedHash); err != nil {
		return "", err
	}
	if err = q.QueryRowContext(ctx, `SELECT submitted_hash FROM recovery_purpose_confirmation_receipts WHERE intent_ai_session_id=? AND intent_id=?`, in.AISessionID, in.IntentID).Scan(&submitted); err != nil {
		return "", err
	}
	commitJSON, _ := json.Marshal(intent.Commit)
	if !confirmationCommitIdentity(intent, *intent.Commit) || workflowHash(string(commitJSON)) != typedHash || !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.Request.VariantID != variantID || sub.PayloadSHA256 != submitted || sub.Request.Selection.RuntimeConfig != nil || sub.Request.Selection.Backend != nil || sub.Request.BackendSelection != nil || intent.Commit.Selection.RuntimeConfig != nil || intent.Commit.Selection.Backend != nil || intent.Commit.BackendSelection != nil {
		return "", rs.ErrPurposeReceiptMismatch
	}
	record, err := scanRecovery(q.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE id=?`, p.RecoveryID))
	if err != nil {
		return "", err
	}
	var assessment struct{ Consistency, EngineeringDisposition string }
	var resolution struct{ RequestID, PayloadHash, AssessmentHash, ReportHash string }
	if record.ID != p.RecoveryID || record.State != "RESOLVED" || record.Spec.ProjectID != project || record.Spec.Generation != p.RecoveryGeneration || record.Spec.Validate() != nil || record.Spec.PayloadHash != record.Spec.ComputePayloadHash() || record.AssessmentHash != p.AssessmentHash || rs.Hash(record.Assessment) != record.AssessmentHash || rs.Hash(record.Report) != record.ReportHash || json.Unmarshal(record.Assessment, &assessment) != nil || assessment.Consistency != "CONSISTENT" || assessment.EngineeringDisposition != "REVISION_REQUIRED" || json.Unmarshal(record.Resolution, &resolution) != nil || resolution.RequestID == "" || resolution.PayloadHash == "" || resolution.AssessmentHash != record.AssessmentHash || resolution.ReportHash != record.ReportHash || p.Validate(record.Spec.Source, record.ID, record.Spec.Generation, record.AssessmentHash) != nil {
		return "", rs.ErrPurposeReceiptMismatch
	}
	specJSON, _ := json.Marshal(record.Spec)
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE id=? AND state='RESOLVED' AND source_run_id=? AND source_turn_id=? AND generation=? AND runtime_epoch=? AND spec_json=? AND COALESCE(recovery_run_id,'')=? AND (?<>'' OR NOT EXISTS(SELECT 1 FROM recovery_child_admissions a WHERE a.recovery_id=recovery_status_records.id)))`, record.ID, p.SourceRunID, p.SourceTurnID, p.RecoveryGeneration, record.RuntimeEpoch, string(specJSON), record.RecoveryRunID, record.RecoveryRunID).Scan(&exact); err != nil {
		return "", err
	}
	if !exact || record.InstructionHash != rs.Hash([]byte(record.Spec.Instruction)) || record.SchemaHash != rs.Hash(record.Schema) {
		return "", rs.ErrPurposeReceiptMismatch
	}
	// Old archives without a diagnostic child remain readable. A recorded child
	// must prove its complete immutable preparation and terminal-turn chain.
	if record.RecoveryRunID != "" {
		if _, _, err = readRecoveryDiagnosticEvidenceTx(ctx, q, record, true); err != nil {
			return "", err
		}
	}
	source, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, p.SourceRunID))
	if err != nil {
		return "", err
	}
	w := source.RuntimeWorkset
	if source.ProjectID != project || source.TaskID != p.TaskID || source.FeatureAreaID != areaID || source.OperationKind != run.OperationReview || source.OperationID != p.SourceReviewID || source.ReviewID != p.SourceReviewID || source.Purpose != run.PurposeScopeReview || source.TurnID != p.SourceTurnID || source.ThreadID != record.Spec.ThreadID || source.RuntimeEpoch != p.RuntimeEpoch || source.Status != run.StatusFailed || source.FinishedAt == nil || w == nil || w.Validate() != nil || w.ContentHash != p.RuntimeWorksetHash || w.ProjectID != project || w.FeatureAreaID != areaID || w.ReviewID != p.SourceReviewID || w.ExecutionID != "" || w.RunID != source.ID || w.TaskDefinitionRevisionID != p.TaskDefinitionRevisionID || w.TaskID != "" && w.TaskID != p.TaskID {
		return "", rs.ErrPurposeReceiptMismatch
	}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN app_server_run_bindings a ON a.run_id=b.run_id JOIN runs r ON r.id=b.run_id WHERE b.run_id=? AND b.agent_conversation_id=? AND json_extract(a.binding_json,'$.id')=? AND r.runtime_workset_hash=?)`, source.ID, record.Spec.AgentConversationID, record.Spec.AppServerInstanceID, w.ContentHash).Scan(&exact); err != nil {
		return "", err
	}
	if !exact {
		return "", rs.ErrPurposeReceiptMismatch
	}
	var variant *rs.PurposeVariant
	for i := range p.Variants {
		if p.Variants[i].ID == variantID {
			variant = &p.Variants[i]
		}
	}
	if variant == nil || variant.Review == nil {
		return "", rs.ErrPurposeReceiptMismatch
	}
	previous, err := scanReview(q.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, p.SourceReviewID))
	if err != nil {
		return "", err
	}
	successor, err := scanReview(q.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, successorID))
	if err != nil {
		return "", err
	}
	b := variant.Review
	if previous.ProjectID != project || previous.TaskID != p.TaskID || previous.FeatureAreaID != areaID || previous.RunID != source.ID || previous.Ordinal < 1 || successor.ProjectID != project || successor.TaskID != p.TaskID || successor.FeatureAreaID != areaID || successor.Ordinal != previous.Ordinal+1 || successor.Instruction != b.ReviewInstruction || successor.ReviewDepth != b.ReviewDepth || successor.CreatedByAISessionID != previous.CreatedByAISessionID || successor.AuthorKind != previous.AuthorKind || successor.AuthorID != previous.AuthorID || successor.ExecutorKind != previous.ExecutorKind || successor.RepositoryID != previous.RepositoryID || successor.BaseCommit != previous.BaseCommit || successor.PrimaryMemberID != previous.PrimaryMemberID || successor.EngineeringGoal != previous.EngineeringGoal || !reflect.DeepEqual(successor.Focus, previous.Focus) || !reflect.DeepEqual(successor.ExecutorMetadata, previous.ExecutorMetadata) {
		return "", rs.ErrPurposeReceiptMismatch
	}
	// Purpose consumption changes Review scope and instructions, while the
	// successor keeps the source Review's frozen runtime configuration.
	if successor.CodexConfigID != previous.CodexConfigID || successor.Model != previous.Model || successor.ReasoningEffort != previous.ReasoningEffort || successor.RuntimeConfig != previous.RuntimeConfig || successor.RuntimeConfirmationID != previous.RuntimeConfirmationID || !reflect.DeepEqual(successor.BackendSelection, previous.BackendSelection) {
		return "", rs.ErrPurposeReceiptMismatch
	}
	if previous.RepositoryID == "" {
		if !reflect.DeepEqual(successor.RepositoryTargets, workset.CloneTargets(b.RepositoryTargets)) {
			return "", rs.ErrPurposeReceiptMismatch
		}
	} else {
		if len(successor.RepositoryTargets) != 0 || len(b.RepositoryTargets) != 1 || b.RepositoryTargets[0].RepositoryID != previous.RepositoryID || b.RepositoryTargets[0].Write.Kind != "NONE" || previous.RepositoryProvenance == nil || successor.RepositoryProvenance == nil {
			return "", rs.ErrPurposeReceiptMismatch
		}
		expected := *previous.RepositoryProvenance
		expected.ReviewID, expected.CapturedAt = successor.ID, successor.CreatedAt
		if !reflect.DeepEqual(*successor.RepositoryProvenance, expected) {
			return "", rs.ErrPurposeReceiptMismatch
		}
	}
	mode := reviewgrant.ModeRepository
	if successor.ExecutorKind == review.ExecutorUpperLLM && b.ReviewPlan.InitialScope.Kind == run.ReviewScopePaths {
		mode = reviewgrant.ModeFocused
	}
	paths, err := encodeReviewScopePaths(b.ReviewPlan.InitialScope.Paths)
	if err != nil {
		return "", err
	}
	forbidden, _ := json.Marshal(append([]string{}, previous.ForbiddenPaths...))
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_grants WHERE review_id=? AND project_id=? AND feature_area_id=? AND COALESCE(repository_id,'')=? AND mode=? AND initial_scope_kind=? AND initial_scope_paths_json=? AND protocol_version=? AND selected_modules_json='{"version":1,"items":[]}' AND (forbidden_paths_json=? OR (forbidden_paths_json='null' AND ?='[]')))`, successorID, project, areaID, successor.RepositoryID, mode, b.ReviewPlan.InitialScope.Kind, paths, reviewProtocol(successor), string(forbidden), string(forbidden)).Scan(&exact); err != nil {
		return "", err
	}
	if !exact {
		return "", rs.ErrPurposeReceiptMismatch
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, q, "runtime_scope_recovery_purpose_"+identity)
	if err != nil {
		return "", err
	}
	model, effort := w.Model, w.ReasoningEffort
	if runtime := intent.Content.RecommendedRuntimeConfig; runtime != nil {
		model, effort = runtime.Model, runtime.ReasoningEffort
	}
	expected := runtimepreparation.Plan{ID: plan.ID, Generation: 1, OwnerKind: runtimepreparation.OwnerReview, OwnerID: successorID, ProjectID: project, FeatureAreaID: areaID, TaskID: p.TaskID, SourceCommand: in.Action, SourceReceiptID: in.IntentID, SelectedVariantID: variantID, ProposalHash: proposalHash, SourceReviewID: p.SourceReviewID, SuccessorReviewID: successorID, RepositoryTargets: workset.CloneTargets(b.RepositoryTargets), PrimaryMemberID: w.PrimaryMemberID, BackendIdentity: w.BackendConfigID, Model: model, ReasoningEffort: effort, RuntimeAdapterIdentity: w.RuntimeAdapterIdentity, TopologyRevision: intent.Content.RecoveryPurpose.PreparedTopologyRevision, DevelopmentContextHash: intent.Content.RecoveryPurpose.PreparedDevelopmentContextHash, DevelopmentRefs: map[string]string{}, ExpectedSourceHEADs: map[string]string{}, WriteScopes: map[string]workset.WriteScope{}, PreparationRequestID: "runtime_prepare_recovery_purpose_" + identity, PreparationPayloadHash: workflowHash(in.Action + "\x00" + in.PayloadHash + "\x00" + variantID), CreatedAt: successor.CreatedAt}
	for _, target := range expected.RepositoryTargets {
		expected.DevelopmentRefs[target.MemberID] = target.OriginRef
		expected.ExpectedSourceHEADs[target.MemberID] = target.OriginCommit
		expected.WriteScopes[target.MemberID] = target.Write
	}
	expected.ContentHash, err = expected.ComputeContentHash()
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(plan, expected) {
		return "", rs.ErrPurposeReceiptMismatch
	}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_preparation_authorities WHERE plan_id=? AND owner_kind='REVIEW' AND owner_id=? AND generation=1 AND request_id=? AND payload_hash=? AND created_at=?)`, plan.ID, successorID, plan.PreparationRequestID, plan.PreparationPayloadHash, formatTime(plan.CreatedAt)).Scan(&exact); err != nil {
		return "", err
	}
	if !exact {
		return "", rs.ErrPurposeReceiptMismatch
	}
	return source.ID, nil
}
