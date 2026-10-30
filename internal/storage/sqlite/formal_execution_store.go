package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/formalapproval"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

var _ fe.Store = (*WorkflowProgressionStore)(nil)

// Called only by the original Prepare writer. Replay never arms old requests.
func captureFormalExecutionPrepareTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	if intent.Purpose != confirmation.Execution || intent.Action != "APPROVE_AND_START" {
		return nil
	}
	b := intent.Content.Execution
	if b == nil {
		return fe.ErrConflict
	}
	var result string
	if err := tx.QueryRowContext(ctx, `SELECT result_text FROM review_approval_decisions WHERE decision_id=? AND review_id=? AND task_id=? AND project_id=?`, b.DecisionID, b.ReviewID, b.TaskID, intent.ProjectID).Scan(&result); err != nil {
		return err
	}
	proposal, err := parsePersistedApprovalResult(result)
	if err != nil {
		return err
	}
	// Scalar v1 approvals use their original approval/start command sequence;
	// they do not acquire a v2 workset qualification fence.
	if proposal.Version == 1 && proposal.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
		return nil
	}
	// This producer is the runtime-qualified Coding branch only. Other modes
	// have different explicit business contracts, never this T1 authority.
	if proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
		return nil
	}
	if proposal.Version != 2 || proposal.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return fe.ErrConflict
	}
	if b.TaskDefinitionRevisionID == "" {
		return fe.ErrParentMissing
	}
	var raw, parentHash string
	if err = tx.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, b.TaskDefinitionRevisionID, b.TaskID).Scan(&raw, &parentHash); err != nil {
		return err
	}
	parent, err := task.DecodeDefinition(raw, parentHash)
	if err != nil {
		return err
	}
	var parentSchema *formalapproval.SchemaReference
	if parent.Version == 1 {
		v := formalapproval.DraftParentSchema()
		parentSchema = &v
	}
	reviewed, err := formalReviewRevisionTx(ctx, tx, intent.ProjectID, b.TaskID, b.ReviewID)
	if err != nil {
		return err
	}
	if reviewed != b.TaskDefinitionRevisionID {
		return fe.ErrConflict
	}
	decision, err := reviewDecisionSnapshotTx(ctx, tx, b.DecisionID)
	if err != nil {
		return err
	}
	if decision.ProjectID != intent.ProjectID || decision.TaskID != b.TaskID || decision.ReviewID != b.ReviewID || decision.CurrentReviewID != b.ReviewID || decision.Status != reviewDecisionPending || !decision.Durable || decision.Consumed {
		return fe.ErrConflict
	}
	frozen, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	f := fe.PrepareFence{AISessionID: intent.AISessionID, IntentID: intent.IntentID, IntentVersion: intent.Version, ProjectID: intent.ProjectID, TaskID: b.TaskID, ReviewID: b.ReviewID, DecisionID: b.DecisionID, DecisionVersion: decision.Version, ParentRevisionID: b.TaskDefinitionRevisionID, ParentRevisionHash: parentHash, ContentHash: intent.ContentHash, FrozenPrepareJSON: frozen, ParentDefinitionJSON: json.RawMessage(raw), Schema: formalapproval.SchemaReference{SchemaID: "TASK_DEFINITION", DefinitionVersion: 2, FactSchemaID: "FORMAL_TASK_DEFINITION"}, ParentSchema: parentSchema}
	if err = f.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_execution_fences(ai_session_id,intent_id,intent_version,project_id,task_id,review_id,decision_id,parent_revision_id,content_hash,fence_json,fence_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, f.AISessionID, f.IntentID, f.IntentVersion, f.ProjectID, f.TaskID, f.ReviewID, f.DecisionID, f.ParentRevisionID, f.ContentHash, string(encoded), fe.HashBytes(encoded))
	return err
}
func readFormalExecutionFenceTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (f fe.PrepareFence, err error) {
	var raw, hash, project, taskID, review, decision, parent, content string
	var version int
	err = q.QueryRowContext(ctx, `SELECT fence_json,fence_sha256,project_id,task_id,review_id,decision_id,parent_revision_id,content_hash,intent_version FROM formal_execution_fences WHERE ai_session_id=? AND intent_id=?`, id.AISessionID, id.IntentID).Scan(&raw, &hash, &project, &taskID, &review, &decision, &parent, &content, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fe.ErrMissing
	}
	if err != nil {
		return f, err
	}
	if fe.Decode([]byte(raw), hash, &f) != nil || f.Validate() != nil || f.AISessionID != id.AISessionID || f.IntentID != id.IntentID || f.IntentVersion != version || f.ProjectID != project || f.TaskID != taskID || f.ReviewID != review || f.DecisionID != decision || f.ParentRevisionID != parent || f.ContentHash != content {
		return f, fe.ErrCorrupt
	}
	if f.IntentVersion != id.IntentVersion || f.ContentHash != id.ContentHash {
		return f, fe.ErrConflict
	}
	return f, nil
}

type formalExecutionT1Input struct {
	Identity fe.ReadIdentity
	Platform fe.PlatformReference
}

func prepareFormalExecutionT1Tx(ctx context.Context, tx *sql.Tx, in *formalExecutionT1Input, current task.Task) (*fe.Archive, error) {
	if in == nil {
		return nil, nil
	}
	id := in.Identity
	if err := id.Validate(); err != nil {
		return nil, err
	}
	f, err := readFormalExecutionFenceTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, err = validateFormalExecutionContextTx(ctx, tx, f); err != nil {
		return nil, err
	}
	if current.ID != f.TaskID || current.ProjectID != f.ProjectID || current.CurrentDefinitionRevisionID != f.ParentRevisionID || current.CurrentReviewID != f.ReviewID || current.Phase != task.PhaseReviewApprovalPending || current.Status != task.StatusActive {
		return nil, fe.ErrConflict
	}
	d, err := reviewDecisionSnapshotTx(ctx, tx, f.DecisionID)
	if err != nil {
		return nil, err
	}
	if d.Version != f.DecisionVersion || d.Status != reviewDecisionPending || d.Consumed || !d.Durable {
		return nil, fe.ErrConflict
	}
	intent, err := formalConfirmationTx(ctx, tx, id.AISessionID, id.IntentID)
	if err != nil {
		return nil, err
	}
	if intent.Commit == nil || intent.State != "CONFIRMING" || intent.Version != f.IntentVersion || intent.ContentHash != f.ContentHash || intent.Action != "APPROVE_AND_START" {
		return nil, fe.ErrConflict
	}
	c := *intent.Commit
	stepRequest := "confirmation_" + fe.Hash([]string{f.IntentID, "APPROVE"})[:32]
	count := 0
	for _, step := range intent.Steps {
		if step.Kind == "APPROVE" && step.RequestID == stepRequest && step.State == "UNKNOWN" {
			count++
		}
	}
	if count != 1 || c.Selection.RuntimeConfig == nil || c.BackendSelection == nil || c.Selection.Backend == nil || c.RequestID != id.RequestID || fe.HashCommit(c) != id.TypedPayloadHash {
		return nil, fe.ErrConflict
	}
	var original, submitted, typed string
	err = tx.QueryRowContext(ctx, `SELECT request_json,submitted_sha256,payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, id.AISessionID, id.RequestID, id.IntentID).Scan(&original, &submitted, &typed)
	if err != nil {
		return nil, err
	}
	if submitted != id.SubmittedHash || typed != id.TypedPayloadHash {
		return nil, fe.ErrConflict
	}
	bound, err := readBackendPlatformReference(ctx, tx, *c.BackendSelection, "IMPLEMENTATION", in.Platform.DescriptorID)
	if err != nil {
		return nil, err
	}
	if bound != in.Platform {
		return nil, fe.ErrConflict
	}
	descriptor, err := validateRuntimeSelectionPlatformTx(ctx, tx, in.Platform, "IMPLEMENTATION", true)
	if err != nil {
		return nil, err
	}
	evidence, err := readPlatformEvidence(ctx, tx, in.Platform.CompatibilityEvidenceHash)
	if err != nil {
		return nil, err
	}
	qualification, err := readPlatformQualification(ctx, tx, in.Platform.QualificationID, in.Platform.QualificationGeneration)
	if err != nil {
		return nil, err
	}
	core := formalapproval.CoreSnapshot{ProjectID: current.ProjectID, TaskID: current.ID, TaskStatus: string(current.Status), TaskPhase: string(current.Phase), CurrentRevisionID: current.CurrentDefinitionRevisionID, CurrentExecutionID: current.CurrentExecutionID, CurrentReviewID: current.CurrentReviewID, DecisionID: f.DecisionID, DecisionState: d.Status, CurrentConfirmationIntentID: f.IntentID, CurrentConfirmationSessionID: f.AISessionID, CurrentConfirmationVersionHash: fe.Hash([]any{f.IntentVersion, f.ContentHash})}
	err = tx.QueryRowContext(ctx, `SELECT v.id,v.project_id,v.task_id,m.outcome FROM reviews v JOIN review_result_materializations m ON m.review_id=v.id AND m.run_id=v.linked_run_id WHERE v.id=? AND v.task_id=? AND v.project_id=? AND m.outcome='VALID'`, f.ReviewID, f.TaskID, f.ProjectID).Scan(&core.ReviewID, &core.ReviewProjectID, &core.ReviewTaskID, &core.ReviewOutcome)
	if err != nil {
		return nil, err
	}
	core.ReviewRevisionID, err = formalReviewRevisionTx(ctx, tx, f.ProjectID, f.TaskID, f.ReviewID)
	if err != nil || core.ReviewRevisionID != f.ParentRevisionID {
		return nil, errors.Join(err, fe.ErrConflict)
	}
	return &fe.Archive{Fence: f, Identity: id, Commit: c, OriginalRequestJSON: json.RawMessage(original), ApprovalStepRequestID: stepRequest, Core: core, PlatformReference: in.Platform, Descriptor: descriptor, Evidence: evidence, Qualification: qualification, Runtime: *c.Selection.RuntimeConfig, Backend: *c.BackendSelection}, nil
}
func sealFormalExecutionT1Tx(ctx context.Context, tx *sql.Tx, a *fe.Archive, t task.Task, at string) error {
	if a == nil {
		return nil
	}
	a.RevisionID = t.CurrentDefinitionRevisionID
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, a.RevisionID, t.ID).Scan(&raw, &a.RevisionHash); err != nil {
		return err
	}
	a.DefinitionJSON = json.RawMessage(raw)
	f := a.Fence
	a.FrozenLineage = formalapproval.Lineage{ProjectID: f.ProjectID, TaskID: f.TaskID, ReviewID: f.ReviewID, DecisionID: f.DecisionID, DecisionVersion: f.DecisionVersion, IntentID: f.IntentID, IntentVersion: f.IntentVersion, VariantID: a.Commit.VariantID, CommittedResultID: f.DecisionID, TaskDefinitionRevisionID: a.RevisionID}
	a.ResultLineage = a.FrozenLineage
	generation, err := eventID()
	if err != nil {
		return err
	}
	t1, err := fe.NewT1(*a, generation, runtimeselection.SourceIdentity(f.AISessionID, a.Commit.RequestID, f.DecisionID))
	if err != nil {
		return err
	}
	b1, err := json.Marshal(t1)
	if err != nil {
		return err
	}
	archive, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_execution_t1(ai_session_id,request_id,intent_id,project_id,task_id,review_id,decision_id,approval_step_request_id,revision_id,operation_generation,source_origin_id,identity_hash,t1_json,t1_sha256,archive_json,archive_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, f.AISessionID, a.Commit.RequestID, f.IntentID, f.ProjectID, f.TaskID, f.ReviewID, f.DecisionID, a.ApprovalStepRequestID, a.RevisionID, generation, t1.Identity.SourceOriginID, t1.Identity.ImmutableBinding, string(b1), fe.HashBytes(b1), string(archive), fe.HashBytes(archive), at)
	return err
}

// These readers use immutable producer records only, never current Task,
// inventory, runtime epoch or qualification state as historical replay gates.
func validateFormalExecutionArchiveSourcesTx(ctx context.Context, q definitionQuerier, a fe.Archive) error {
	f, err := readFormalExecutionFenceTx(ctx, q, a.Identity)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(f, a.Fence) {
		return fe.ErrCorrupt
	}
	var raw, hash string
	for _, v := range []struct {
		id, hash string
		raw      json.RawMessage
	}{{f.ParentRevisionID, f.ParentRevisionHash, f.ParentDefinitionJSON}, {a.RevisionID, a.RevisionHash, a.DefinitionJSON}} {
		if err = q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, v.id, f.TaskID).Scan(&raw, &hash); err != nil {
			return err
		}
		if raw != string(v.raw) || hash != v.hash {
			return fe.ErrCorrupt
		}
	}
	var original, submitted, typed, intent string
	err = q.QueryRowContext(ctx, `SELECT request_json,submitted_sha256,payload_sha256,intent_id FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND outcome='CONSUMED'`, a.Identity.AISessionID, a.Identity.RequestID).Scan(&original, &submitted, &typed, &intent)
	if err != nil {
		return err
	}
	if original != string(a.OriginalRequestJSON) || submitted != a.Identity.SubmittedHash || typed != a.Identity.TypedPayloadHash || intent != a.Identity.IntentID {
		return fe.ErrCorrupt
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts d JOIN review_approval_materializations m ON m.decision_id=d.decision_id JOIN confirmation_approval_sources c ON c.decision_id=d.decision_id WHERE d.decision_id=? AND d.actor_kind='AI_SESSION' AND d.actor_id=? AND d.request_id=? AND d.project_id=? AND d.option='APPROVE' AND d.result_kind=? AND d.result_id=? AND m.task_id=d.result_id AND m.initial_revision_id=? AND c.intent_id=? AND c.revision_id=m.initial_revision_id)`, f.DecisionID, f.AISessionID, a.ApprovalStepRequestID, f.ProjectID, decisionresolution.ResultTask, f.TaskID, a.RevisionID, f.IntentID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return fe.ErrCorrupt
	}
	return nil
}
func readFormalExecutionProgressTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (p fe.Progress, err error) {
	if err = id.Validate(); err != nil {
		return p, err
	}
	var t1, t1hash, archive, archivehash, project, taskID, review, decision, intent, revision, generation, source, binding, step string
	err = q.QueryRowContext(ctx, `SELECT t1_json,t1_sha256,archive_json,archive_sha256,project_id,task_id,review_id,decision_id,intent_id,revision_id,operation_generation,source_origin_id,identity_hash,approval_step_request_id FROM formal_execution_t1 WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&t1, &t1hash, &archive, &archivehash, &project, &taskID, &review, &decision, &intent, &revision, &generation, &source, &binding, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fe.ErrMissing
	}
	if err != nil {
		return p, err
	}
	if fe.Decode([]byte(t1), t1hash, &p.T1) != nil || fe.Decode([]byte(archive), archivehash, &p.Archive) != nil {
		return p, fe.ErrCorrupt
	}
	a := p.Archive
	i := p.T1.Identity
	if a.Identity != id {
		return p, fe.ErrConflict
	}
	if a.Fence.ProjectID != project || a.Fence.TaskID != taskID || a.Fence.ReviewID != review || a.Fence.DecisionID != decision || a.Fence.IntentID != intent || a.RevisionID != revision || i.OperationGeneration != generation || i.SourceOriginID != source || i.ImmutableBinding != binding || a.ApprovalStepRequestID != step {
		return p, fe.ErrCorrupt
	}
	p.NextTransaction = fe.T2Transaction
	if err = p.Validate(); err != nil {
		return p, err
	}
	if err = validateFormalExecutionArchiveSourcesTx(ctx, q, a); err != nil {
		return p, err
	}
	var t2, t2hash, plan, planhash, execution, planID, contentHash, request, payload string
	err = q.QueryRowContext(ctx, `SELECT t2_json,t2_sha256,plan_json,plan_sha256,execution_id,plan_id,plan_hash,admission_request_id,admission_payload_hash FROM formal_execution_t2 WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&t2, &t2hash, &plan, &planhash, &execution, &planID, &contentHash, &request, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.T2 = &fe.T2DurableReadback{}
	p.Plan = &runtimepreparation.Plan{}
	p.NextTransaction = fe.T3Transaction
	if fe.Decode([]byte(t2), t2hash, p.T2) != nil || fe.Decode([]byte(plan), planhash, p.Plan) != nil || p.T2.ExecutionID != execution || p.Plan.ID != planID || p.Plan.ContentHash != contentHash || p.Plan.PreparationRequestID != request || p.Plan.PreparationPayloadHash != payload {
		return p, fe.ErrCorrupt
	}
	if err = p.Validate(); err != nil {
		return p, err
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN execution_proposals e ON e.id=a.proposal_id JOIN runtime_scope_plans s ON s.plan_id=a.runtime_scope_plan_id WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND a.admission_request_id=? AND a.payload_sha256=? AND a.runtime_scope_plan_id=? AND s.content_hash=? AND s.owner_id=e.id AND s.task_definition_revision_id=e.task_definition_revision_id)`, execution, project, taskID, revision, request, payload, planID, contentHash).Scan(&exact)
	if err != nil {
		return p, err
	}
	if !exact {
		return p, fe.ErrCorrupt
	}
	storedPlan, planErr := readArchivedRuntimeSelectionPlanTx(ctx, q, p.Plan.ID)
	if planErr != nil {
		return p, planErr
	}
	if !reflect.DeepEqual(storedPlan, *p.Plan) {
		return p, fe.ErrCorrupt
	}
	return p, nil
}
func readFormalExecutionReceiptTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (r fe.Receipt, err error) {
	if err = id.Validate(); err != nil {
		return r, err
	}
	var raw, hash, key, authority string
	err = q.QueryRowContext(ctx, `SELECT receipt_json,receipt_sha256,receipt_identity,preparation_authority_id FROM formal_execution_receipts WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&raw, &hash, &key, &authority)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fe.ErrMissing
	}
	if err != nil {
		return r, err
	}
	if fe.Decode([]byte(raw), hash, &r) != nil || r.Validate() != nil || r.ReceiptIdentity != key || r.PreparationAuthorityID != authority {
		return r, fe.ErrCorrupt
	}
	if err = id.MatchesReceipt(r); err != nil {
		return r, err
	}
	// The self-contained final archive does not query partial progress. Original
	// ledger/revisions are immutable evidence, not T1/T2 continuation authority.
	if err = validateFormalExecutionArchiveSourcesTx(ctx, q, r.Archive); err != nil {
		return r, err
	}
	storedPlan, planErr := readArchivedRuntimeSelectionPlanTx(ctx, q, r.Plan.ID)
	if planErr != nil {
		return r, planErr
	}
	if !reflect.DeepEqual(storedPlan, r.Plan) {
		return r, fe.ErrCorrupt
	}
	var complete bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN runtime_preparation_authorities p ON p.plan_id=a.runtime_preparation_authority_id JOIN execution_proposals e ON e.id=a.proposal_id WHERE a.proposal_id=? AND a.task_id=? AND a.task_definition_revision_id=? AND a.runtime_scope_plan_id=? AND a.runtime_preparation_authority_id=? AND a.admission_request_id=? AND a.payload_sha256=? AND e.project_id=? AND e.task_id=a.task_id AND e.task_definition_revision_id=a.task_definition_revision_id AND p.owner_id=e.id AND p.generation=? AND p.request_id=a.admission_request_id AND p.payload_hash=a.payload_sha256)`, r.T2.ExecutionID, r.Archive.Fence.TaskID, r.Archive.RevisionID, r.Plan.ID, r.PreparationAuthorityID, r.Plan.PreparationRequestID, r.Plan.PreparationPayloadHash, r.Archive.Fence.ProjectID, r.Plan.Generation).Scan(&complete)
	if err != nil {
		return r, err
	}
	if !complete {
		return r, fe.ErrCorrupt
	}
	return r, nil
}
func (s *WorkflowProgressionStore) ReadApproveAndStart(ctx context.Context, id fe.ReadIdentity) (r fe.Receipt, err error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	r, err = readFormalExecutionReceiptTx(ctx, tx, id)
	if err != nil {
		return r, err
	}
	_, err = readRuntimeSelectionSourceTx(ctx, tx, r.T1.Identity.SourceOriginID)
	return r, err
}
func (s *WorkflowProgressionStore) ReadApproveAndStartProgress(ctx context.Context, id fe.ReadIdentity) (fe.Progress, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fe.Progress{}, err
	}
	defer tx.Rollback()
	// A corrupt exact-key final result cannot fall through to partial/fresh.
	if _, err = readFormalExecutionReceiptTx(ctx, tx, id); err == nil {
		return fe.Progress{}, fe.ErrConflict
	} else if !errors.Is(err, fe.ErrMissing) {
		return fe.Progress{}, err
	}
	return readFormalExecutionProgressTx(ctx, tx, id)
}
func (s *WorkflowProgressionStore) CommitApproveAndStart(ctx context.Context, id fe.ReadIdentity, platform fe.PlatformReference) (fe.Receipt, error) {
	ctx = taskaction.WithIntent(ctx, id.IntentID)
	if r, err := s.ReadApproveAndStart(ctx, id); err == nil {
		return r, nil
	} else if !errors.Is(err, fe.ErrMissing) {
		return r, err
	}
	p, err := s.ReadApproveAndStartProgress(ctx, id)
	if errors.Is(err, fe.ErrMissing) {
		intent, readErr := formalConfirmationTx(ctx, s.db.sql, id.AISessionID, id.IntentID)
		if readErr != nil {
			return fe.Receipt{}, readErr
		}
		if intent.Commit == nil || intent.Content.Execution == nil {
			return fe.Receipt{}, fe.ErrConflict
		}
		b, c := intent.Content.Execution, *intent.Commit
		_, err = s.resolve(ctx, wp.ResolveInput{AISessionID: id.AISessionID, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, ReviewID: b.ReviewID, DecisionID: b.DecisionID, Option: "APPROVE", RequestID: "confirmation_" + fe.Hash([]string{id.IntentID, "APPROVE"})[:32], ConfirmationIntentID: id.IntentID, NextRunRuntimeConfig: c.Selection.RuntimeConfig, BackendSelection: c.BackendSelection, StartEffect: wp.StartEffectNoStart}, &formalExecutionT1Input{Identity: id, Platform: platform})
		if err != nil {
			// A concurrent exact caller may have committed the same T1. Only
			// its complete identity can recover the winner; never rerun a
			// failed first transaction from inferred business state.
			if final, readErr := s.ReadApproveAndStart(ctx, id); readErr == nil {
				return final, nil
			} else if !errors.Is(readErr, fe.ErrMissing) {
				return fe.Receipt{}, readErr
			}
			if winner, readErr := s.ReadApproveAndStartProgress(ctx, id); readErr == nil {
				p, err = winner, nil
			} else {
				return fe.Receipt{}, err
			}
		} else {
			p, err = s.ReadApproveAndStartProgress(ctx, id)
		}
	}
	if err != nil {
		// Final completion may win between the read-first and progress reads.
		// Its exact archive remains the only success authority.
		if final, readErr := s.ReadApproveAndStart(ctx, id); readErr == nil {
			return final, nil
		} else if !errors.Is(readErr, fe.ErrMissing) {
			return fe.Receipt{}, readErr
		}
		return fe.Receipt{}, err
	}
	if p.T2 == nil {
		if err = s.commitFormalExecutionT2(ctx, id); err != nil {
			return fe.Receipt{}, err
		}
	}
	if err = s.commitFormalExecutionT3(ctx, id); err != nil {
		return fe.Receipt{}, err
	}
	return s.ReadApproveAndStart(ctx, id)
}
func formalExecutionPrefixOwnerTx(ctx context.Context, tx *sql.Tx, p fe.Progress) (task.Task, error) {
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, p.Archive.Fence.TaskID, p.Archive.Fence.ProjectID))
	if err != nil {
		return t, err
	}
	want := p.Archive.Core.CurrentExecutionID
	if p.T2 != nil {
		want = p.T2.ExecutionID
	}
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationPreparing || t.CurrentDefinitionRevisionID != p.Archive.RevisionID || t.CurrentReviewID != p.Archive.Fence.ReviewID || t.ReviewApprovalID != p.Archive.Fence.DecisionID || t.CurrentExecutionID != want {
		return t, fe.ErrConflict
	}
	var available bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=? AND f.project_id=? AND f.status='ACTIVE' AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=f.id) AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id=? OR (?<>'' AND c.owner_id=?)))`, t.FeatureAreaID, t.ProjectID, t.ID, func() string {
		if p.T2 != nil {
			return p.T2.ExecutionID
		}
		return ""
	}(), func() string {
		if p.T2 != nil {
			return p.T2.ExecutionID
		}
		return ""
	}()).Scan(&available)
	if err != nil {
		return t, err
	}
	if !available {
		return t, fe.ErrConflict
	}
	return t, nil
}
func (s *WorkflowProgressionStore) commitFormalExecutionT2(ctx context.Context, id fe.ReadIdentity) error {
	pinned, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return err
	}
	defer pinned.Rollback()
	tx := pinned.Tx
	p, err := readFormalExecutionProgressTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if p.T2 != nil {
		return nil
	}
	t, err := formalExecutionPrefixOwnerTx(ctx, tx, p)
	if err != nil {
		return err
	}
	if err = createFormalApprovedExecutionTx(ctx, tx, t, p, s.now()); err != nil {
		return err
	}
	t.CurrentExecutionID = approvedExecutionID(t)
	plan, err := formalExecutionPlanTx(ctx, tx, t, p, s.now())
	if err != nil {
		return err
	}
	if err = freezeRuntimeScopePlanOnlyTx(ctx, tx, plan); err != nil {
		return err
	}
	r := fe.T2DurableReadback{T1ResultID: p.T1.Identity.BusinessResultID, OperationGeneration: p.T1.Identity.OperationGeneration, OperationIdentityBinding: p.T1.Identity.ImmutableBinding, ExecutionID: t.CurrentExecutionID, ExecutionBinding: fe.CanonicalHash([]string{t.CurrentExecutionID, p.Archive.RevisionID, p.T1.Identity.ImmutableBinding}), ScopePlanID: plan.ID, ScopePlanBinding: plan.ContentHash, Producer: "CORE", Transaction: fe.T2Transaction, CommitState: "T2_COMMITTED"}
	if err = fe.ValidateT2(p.T1, r, plan); err != nil {
		return err
	}
	rb, _ := json.Marshal(r)
	pb, _ := json.Marshal(plan)
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_execution_t2(ai_session_id,request_id,execution_id,plan_id,plan_hash,admission_request_id,admission_payload_hash,t2_json,t2_sha256,plan_json,plan_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id.AISessionID, id.RequestID, t.CurrentExecutionID, plan.ID, plan.ContentHash, plan.PreparationRequestID, plan.PreparationPayloadHash, string(rb), fe.HashBytes(rb), string(pb), fe.HashBytes(pb), formatTime(s.now()))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposal_admissions(proposal_id,admission_request_id,payload_sha256,source_decision_id,source_confirmation_intent_id,task_id,task_definition_revision_id,runtime_scope_plan_id,runtime_preparation_authority_id,admitted_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, t.CurrentExecutionID, plan.PreparationRequestID, plan.PreparationPayloadHash, t.ReviewApprovalID, id.IntentID, t.ID, t.CurrentDefinitionRevisionID, plan.ID, plan.ID, formatTime(s.now()))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED',updated_at=? WHERE id=? AND status='CONFIRMED' AND run_id IS NULL AND outcome='PENDING'`, formatTime(s.now()), t.CurrentExecutionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fe.ErrConflict
	}
	return pinned.Commit()
}
func (s *WorkflowProgressionStore) commitFormalExecutionT3(ctx context.Context, id fe.ReadIdentity) error {
	// Read-before-write preserves completed replay under stale writer epochs.
	if _, err := s.ReadApproveAndStart(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, fe.ErrMissing) {
		return err
	}
	pinned, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return err
	}
	defer pinned.Rollback()
	tx := pinned.Tx
	if _, err = readFormalExecutionReceiptTx(ctx, tx, id); err == nil {
		return nil
	} else if !errors.Is(err, fe.ErrMissing) {
		return err
	}
	p, err := readFormalExecutionProgressTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if p.T2 == nil {
		return fe.ErrConflict
	}
	if _, err = formalExecutionPrefixOwnerTx(ctx, tx, p); err != nil {
		return err
	}
	if err = writeRuntimePreparationAuthorityTx(ctx, tx, *p.Plan); err != nil {
		return err
	}
	r, err := fe.NewReceipt(p)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_execution_receipts(ai_session_id,request_id,receipt_identity,receipt_sha256,receipt_json,preparation_authority_id,created_at) VALUES(?,?,?,?,?,?,?)`, id.AISessionID, id.RequestID, r.ReceiptIdentity, fe.HashBytes(raw), string(raw), r.PreparationAuthorityID, formatTime(s.now()))
	if err != nil {
		return err
	}
	if _, err = persistRuntimeSelectionSourceTx(ctx, tx, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTATION_READY',updated_at=? WHERE id=? AND project_id=? AND status='ACTIVE' AND phase='IMPLEMENTATION_PREPARING' AND current_definition_revision_id=? AND current_execution_id=?`, formatTime(s.now()), p.Archive.Fence.TaskID, p.Archive.Fence.ProjectID, p.Archive.RevisionID, p.T2.ExecutionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fe.ErrConflict
	}
	return pinned.Commit()
}

// The returned IDs are receipt evidence only; no Run is allocated here.
func (s *WorkflowProgressionStore) FormalExecutionIdentity(ctx context.Context, session, intent, request string) (fe.ReadIdentity, error) {
	i, err := formalConfirmationTx(ctx, s.db.sql, session, intent)
	if err != nil {
		return fe.ReadIdentity{}, err
	}
	if i.Commit == nil || i.Commit.RequestID != request {
		return fe.ReadIdentity{}, fe.ErrConflict
	}
	id := fe.ReadIdentity{AISessionID: session, IntentID: intent, IntentVersion: i.Version, RequestID: request, ContentHash: i.ContentHash, TypedPayloadHash: fe.HashCommit(*i.Commit)}
	err = s.db.sql.QueryRowContext(ctx, `SELECT submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, session, request, intent).Scan(&id.SubmittedHash)
	return id, err
}

// ReadApproveAndStartRequired distinguishes newly armed formal confirmations
// from historical completed business receipts without creating new evidence.
func (s *WorkflowProgressionStore) ReadApproveAndStartRequired(ctx context.Context, id fe.ReadIdentity) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, err = readFormalExecutionFenceTx(ctx, tx, id)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, fe.ErrMissing) {
		return false, err
	}
	var partial bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM formal_execution_t1 WHERE ai_session_id=? AND (request_id=? OR intent_id=?))`, id.AISessionID, id.RequestID, id.IntentID).Scan(&partial); err != nil {
		return false, err
	}
	if partial {
		return false, fe.ErrCorrupt
	}
	return false, nil
}
