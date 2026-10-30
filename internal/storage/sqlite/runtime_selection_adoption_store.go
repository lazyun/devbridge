package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/lazyun/devbridge/internal/formalapproval"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

var _ fe.AdoptionStore = (*WorkflowProgressionStore)(nil)

func readPriorDefinitionApprovalTx(ctx context.Context, q definitionQuerier, revision string) (formalapproval.Receipt, error) {
	var raw, hash string
	err := q.QueryRowContext(ctx, `SELECT r.receipt_json,r.receipt_sha256 FROM formal_definition_approval_receipts r JOIN formal_definition_approval_business b ON b.ai_session_id=r.ai_session_id AND b.request_id=r.request_id WHERE b.revision_id=?`, revision).Scan(&raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return formalapproval.Receipt{}, fe.ErrParentMissing
	}
	if err != nil {
		return formalapproval.Receipt{}, err
	}
	r, err := formalapproval.DecodeReceipt([]byte(raw), hash)
	if err != nil {
		return r, err
	}
	a := r.Snapshot
	return readDefinitionApprovalReceiptTx(ctx, q, formalapproval.ReadIdentity{AISessionID: a.Fence.AISessionID, IntentID: a.Fence.IntentID, RequestID: a.Commit.RequestID, IntentVersion: a.Fence.IntentVersion, ContentHash: a.Fence.ContentHash, TypedPayloadHash: a.TypedPayloadHash, SubmittedHash: a.SubmittedHash})
}

// Exact prior runtime authorization is read through the immutable formal reader.
func readPriorRuntimeApprovalTx(ctx context.Context, q definitionQuerier, execution string) (fe.Receipt, error) {
	var raw, hash string
	err := q.QueryRowContext(ctx, `SELECT r.receipt_json,r.receipt_sha256 FROM formal_execution_receipts r JOIN formal_execution_t2 t ON t.ai_session_id=r.ai_session_id AND t.request_id=r.request_id WHERE t.execution_id=?`, execution).Scan(&raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return fe.Receipt{}, fe.ErrParentMissing
	}
	if err != nil {
		return fe.Receipt{}, err
	}
	var r fe.Receipt
	if fe.Decode([]byte(raw), hash, &r) != nil || r.Validate() != nil {
		return r, fe.ErrCorrupt
	}
	r, err = readFormalExecutionReceiptTx(ctx, q, r.Archive.Identity)
	if err != nil {
		return r, err
	}
	if _, err = readRuntimeSelectionSourceTx(ctx, q, r.T1.Identity.SourceOriginID); err != nil {
		return r, err
	}
	return r, nil
}

// Reads only the original immutable admission and archive. No lifecycle or
// current-owner predicate participates in historical readback.
func validateFormalAdoptionSourcesTx(ctx context.Context, q definitionQuerier, a fe.AdoptionArchive) error {
	required, err := readFormalAdoptionRequiredTx(ctx, q, a.Identity)
	if err != nil {
		return err
	}
	if !required {
		return fe.ErrCorrupt
	}
	if a.PriorRuntimeApproval != nil {
		prior, e := readPriorRuntimeApprovalTx(ctx, q, a.ExecutionID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(prior, *a.PriorRuntimeApproval) {
			return fe.ErrCorrupt
		}
	} else {
		expectedPayload, e := executionStartEffectPayload(executionStartEffectRecord{ProjectID: a.ProjectID, ActorKind: "AI_SESSION", ActorID: a.Identity.AISessionID, RequestID: a.AdmissionRequestID, Action: "START_EXECUTION", Effect: wp.StartEffectStartAuthorized, TaskID: a.TaskID, FeatureAreaID: a.FeatureAreaID, RevisionID: a.RevisionID, ExecutionID: a.ExecutionID, Phase: task.PhaseImplementationReady})
		if e != nil {
			return e
		}
		if expectedPayload != a.AdmissionPayloadHash {
			return fe.ErrCorrupt
		}
		prior, e := readPriorDefinitionApprovalTx(ctx, q, a.RevisionID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(prior, a.PriorApproval) {
			return fe.ErrCorrupt
		}
	}
	intent, err := formalConfirmationTx(ctx, q, a.Identity.AISessionID, a.Identity.IntentID)
	if err != nil {
		return err
	}
	frozen, _ := json.Marshal(intent.Content)
	if intent.Commit == nil || fe.HashCommit(*intent.Commit) != a.Identity.TypedPayloadHash || string(frozen) != string(a.FrozenPrepareJSON) {
		return fe.ErrCorrupt
	}
	var original, typed, submitted string
	err = q.QueryRowContext(ctx, `SELECT request_json,payload_sha256,submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, a.Identity.AISessionID, a.Identity.RequestID, a.Identity.IntentID).Scan(&original, &typed, &submitted)
	if err != nil {
		return err
	}
	if original != string(a.OriginalRequestJSON) || typed != a.Identity.TypedPayloadHash || submitted != a.Identity.SubmittedHash {
		return fe.ErrCorrupt
	}
	var exact bool
	if a.PriorRuntimeApproval != nil {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions i JOIN execution_proposals e ON e.id=i.proposal_id JOIN runtime_preparation_authorities p ON p.plan_id=i.runtime_preparation_authority_id WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND i.admission_request_id=? AND i.payload_sha256=? AND i.runtime_scope_plan_id=? AND i.runtime_preparation_authority_id=? AND p.owner_id=e.id AND p.request_id=i.admission_request_id AND p.payload_hash=i.payload_sha256)`, a.ExecutionID, a.ProjectID, a.TaskID, a.RevisionID, a.AdmissionRequestID, a.AdmissionPayloadHash, a.Plan.ID, a.PreparationAuthorityID).Scan(&exact)
	} else {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions i JOIN execution_proposals e ON e.id=i.proposal_id JOIN execution_start_effects s ON s.execution_id=e.id JOIN runtime_preparation_authorities p ON p.plan_id=i.runtime_preparation_authority_id WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND i.admission_request_id=? AND i.payload_sha256=? AND i.runtime_scope_plan_id=? AND i.runtime_preparation_authority_id=? AND s.actor_kind='AI_SESSION' AND s.actor_id=? AND s.request_id=i.admission_request_id AND s.action='START_EXECUTION' AND s.effect='START_AUTHORIZED' AND s.task_id=e.task_id AND s.project_id=e.project_id AND s.task_definition_revision_id=e.task_definition_revision_id AND s.payload_sha256=i.payload_sha256 AND s.expected_phase='IMPLEMENTATION_READY' AND s.feature_area_id=? AND p.owner_id=e.id AND p.request_id=i.admission_request_id AND p.payload_hash=i.payload_sha256)`, a.ExecutionID, a.ProjectID, a.TaskID, a.RevisionID, a.AdmissionRequestID, a.AdmissionPayloadHash, a.Plan.ID, a.PreparationAuthorityID, a.Identity.AISessionID, a.FeatureAreaID).Scan(&exact)
		if err != nil {
			return err
		}
		if !exact {
			return fe.ErrCorrupt
		}
	}
	if err != nil {
		return err
	}
	if !exact {
		return fe.ErrCorrupt
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, q, a.Plan.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(plan, a.Plan) {
		return fe.ErrCorrupt
	}
	return nil
}
func readFormalAdoptionBusinessTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (a fe.AdoptionArchive, err error) {
	if err = id.Validate(); err != nil {
		return
	}
	var raw, hash, intent, project, taskID, revision, execution, admission string
	err = q.QueryRowContext(ctx, `SELECT business_json,business_sha256,intent_id,project_id,task_id,revision_id,execution_id,admission_request_id FROM formal_adoption_business WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&raw, &hash, &intent, &project, &taskID, &revision, &execution, &admission)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fe.ErrMissing
	}
	if err != nil {
		return
	}
	if fe.Decode([]byte(raw), hash, &a) != nil || a.Validate() != nil || a.Identity.IntentID != intent || a.ProjectID != project || a.TaskID != taskID || a.RevisionID != revision || a.ExecutionID != execution || a.AdmissionRequestID != admission {
		return a, fmt.Errorf("formal adoption archive decode/identity: %w", fe.ErrCorrupt)
	}
	if a.Identity != id {
		return a, fe.ErrConflict
	}
	if err = validateFormalAdoptionSourcesTx(ctx, q, a); err != nil {
		return a, fmt.Errorf("formal adoption immutable sources: %w", err)
	}
	return a, nil
}
func readFormalAdoptionReceiptTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (r fe.AdoptionReceipt, err error) {
	if err = id.Validate(); err != nil {
		return
	}
	var raw, hash, business, identity string
	err = q.QueryRowContext(ctx, `SELECT receipt_json,receipt_sha256,business_sha256,receipt_identity FROM formal_adoption_receipts WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&raw, &hash, &business, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fe.ErrMissing
	}
	if err != nil {
		return
	}
	if fe.Decode([]byte(raw), hash, &r) != nil || r.Validate() != nil || r.BusinessHash != business || r.ReceiptIdentity != identity {
		return r, fe.ErrCorrupt
	}
	if err = id.MatchesAdoption(r); err != nil {
		return
	}
	a, err := readFormalAdoptionBusinessTx(ctx, q, id)
	if err != nil {
		return r, err
	}
	if !reflect.DeepEqual(a, r.Archive) {
		return r, fe.ErrCorrupt
	}
	return r, nil
}
func (s *WorkflowProgressionStore) ReadAdoption(ctx context.Context, id fe.ReadIdentity) (r fe.AdoptionReceipt, err error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return
	}
	defer tx.Rollback()
	r, err = readFormalAdoptionReceiptTx(ctx, tx, id)
	if err != nil {
		return
	}
	_, err = readRuntimeSelectionSourceTx(ctx, tx, r.SourceID())
	return
}
func (s *WorkflowProgressionStore) CommitAdoption(ctx context.Context, id fe.ReadIdentity, ref runtimeselection.PlatformReference) (fe.AdoptionReceipt, error) {
	ctx = taskaction.WithIntent(ctx, id.IntentID)
	if r, err := s.ReadAdoption(ctx, id); err == nil {
		return r, nil
	} else if !errors.Is(err, fe.ErrMissing) {
		return r, err
	}
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return fe.AdoptionReceipt{}, err
	}
	defer tx.Rollback()
	if r, e := readFormalAdoptionReceiptTx(ctx, tx, id); e == nil {
		_, e = readRuntimeSelectionSourceTx(ctx, tx, r.SourceID())
		return r, e
	} else if !errors.Is(e, fe.ErrMissing) {
		return r, e
	}
	// There is no continuation stage: a committed prefix is corruption, never
	// authority to issue a missing final receipt or source.
	if _, e := readFormalAdoptionBusinessTx(ctx, tx, id); e == nil {
		return fe.AdoptionReceipt{}, fe.ErrCorrupt
	} else if !errors.Is(e, fe.ErrMissing) {
		return fe.AdoptionReceipt{}, e
	}
	if err = s.commitFormalAdoptionBusinessTx(ctx, tx.Tx, id, ref); err != nil {
		return fe.AdoptionReceipt{}, err
	}
	a, err := readFormalAdoptionBusinessTx(ctx, tx, id)
	if err != nil {
		return fe.AdoptionReceipt{}, err
	}
	r, err := fe.NewAdoptionReceipt(a)
	if err != nil {
		return r, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_adoption_receipts(ai_session_id,request_id,business_sha256,receipt_identity,receipt_sha256,receipt_json) VALUES(?,?,?,?,?,?)`, id.AISessionID, id.RequestID, r.BusinessHash, r.ReceiptIdentity, fe.HashBytes(raw), string(raw))
	if err != nil {
		return fe.AdoptionReceipt{}, err
	}
	if _, err = persistRuntimeSelectionAdoptionSourceTx(ctx, tx.Tx, id); err != nil {
		return fe.AdoptionReceipt{}, err
	}
	if err = completeFormalConfirmationTx(ctx, tx.Tx, id.AISessionID, id.IntentID, id.RequestID, "START_IMPLEMENTATION", a.TaskID, a.RevisionID, a.ExecutionID); err != nil {
		return fe.AdoptionReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return fe.AdoptionReceipt{}, err
	}
	return r, nil
}
func (s *WorkflowProgressionStore) commitFormalAdoptionBusinessTx(ctx context.Context, tx *sql.Tx, id fe.ReadIdentity, ref runtimeselection.PlatformReference) error {
	required, err := readFormalAdoptionRequiredTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if !required {
		return fe.ErrConflict
	}
	intent, err := formalConfirmationTx(ctx, tx, id.AISessionID, id.IntentID)
	if err != nil {
		return err
	}
	if intent.Commit == nil || intent.Content.Execution == nil || intent.Content.Execution.ExpectedTarget == nil || intent.Version != id.IntentVersion || intent.ContentHash != id.ContentHash || intent.Action != "START_APPROVED" || (intent.State != "CONFIRMING" && intent.State != "PARTIAL" && intent.State != "UNKNOWN") {
		return fe.ErrConflict
	}
	if err = validateFormalConfirmationContextTx(ctx, tx, intent); err != nil {
		return err
	}
	c, b := *intent.Commit, intent.Content.Execution
	if c.RequestID != id.RequestID || fe.HashCommit(c) != id.TypedPayloadHash || c.Selection.RuntimeConfig == nil || c.BackendSelection == nil {
		return fe.ErrConflict
	}
	request := "confirmation_" + fe.Hash([]string{id.IntentID, "START_IMPLEMENTATION"})[:32]
	count := 0
	for _, step := range intent.Steps {
		if step.Kind == "START_IMPLEMENTATION" && step.RequestID == request && (step.State == "PENDING" || step.State == "UNKNOWN") {
			count++
		}
	}
	if count != 1 {
		return fe.ErrConflict
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, b.TaskID, intent.ProjectID))
	if err != nil {
		return err
	}
	expected := *b.ExpectedTarget
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || expected.ExecutionID != t.CurrentExecutionID || expected.Phase != t.Phase || expected.PrimaryMemberID != t.PrimaryMemberID || expected.ApprovedWorksetHash != fe.Hash(t.RepositoryTargets) {
		return fe.ErrConflict
	}
	var prior formalapproval.Receipt
	var locked *fe.Receipt
	if t.CurrentExecutionID != "" {
		var revision string
		if err = tx.QueryRowContext(ctx, `SELECT task_definition_revision_id FROM execution_proposals WHERE id=?`, t.CurrentExecutionID).Scan(&revision); err != nil {
			return err
		}
		if revision == t.CurrentDefinitionRevisionID {
			r, e := readPriorRuntimeApprovalTx(ctx, tx, t.CurrentExecutionID)
			if e != nil {
				return e
			}
			locked = &r
			var eligible bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN execution_proposal_admissions a ON a.proposal_id=e.id JOIN runtime_preparation_authorities p ON p.plan_id=a.runtime_preparation_authority_id WHERE e.id=? AND e.status='ADMITTED' AND e.run_id IS NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE execution_proposal_id=e.id) AND p.disposition='CURRENT')`, t.CurrentExecutionID).Scan(&eligible); err != nil {
				return err
			}
			if !eligible || *c.Selection.RuntimeConfig != r.Archive.Runtime || *c.BackendSelection != r.Archive.Backend {
				return fe.ErrConflict
			}
			if ref == (runtimeselection.PlatformReference{}) {
				ref = r.Archive.PlatformReference
			}
			if ref != r.Archive.PlatformReference {
				return fe.ErrConflict
			}
		}
	}
	if locked == nil {
		if ref == (runtimeselection.PlatformReference{}) {
			return fe.ErrQualificationRequired
		}
		prior, err = readPriorDefinitionApprovalTx(ctx, tx, t.CurrentDefinitionRevisionID)
		if err != nil {
			return err
		}
	}
	bound, err := readBackendPlatformReference(ctx, tx, *c.BackendSelection, "IMPLEMENTATION", ref.DescriptorID)
	if err != nil {
		return err
	}
	if bound != ref {
		return fe.ErrConflict
	}
	descriptor, err := validateRuntimeSelectionPlatformTx(ctx, tx, ref, "IMPLEMENTATION", true)
	if err != nil {
		return err
	}
	lineage, err := readPlatformLineage(ctx, tx, ref.PlatformLineageID)
	if err != nil {
		return err
	}
	if lineage.Version != ref.LineageVersion {
		return fe.ErrConflict
	}
	evidence, err := readPlatformEvidence(ctx, tx, ref.CompatibilityEvidenceHash)
	if err != nil {
		return err
	}
	qualification, err := readPlatformQualification(ctx, tx, ref.QualificationID, ref.QualificationGeneration)
	if err != nil {
		return err
	}
	var original, typed, submitted string
	err = tx.QueryRowContext(ctx, `SELECT request_json,payload_sha256,submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, id.AISessionID, id.RequestID, id.IntentID).Scan(&original, &typed, &submitted)
	if err != nil {
		return err
	}
	if typed != id.TypedPayloadHash || submitted != id.SubmittedHash {
		return fe.ErrConflict
	}
	execution, admissionRequest, payload := "", request, ""
	var plan runtimepreparation.Plan
	if locked != nil {
		execution, admissionRequest, payload, plan = locked.T2.ExecutionID, locked.Plan.PreparationRequestID, locked.Plan.PreparationPayloadHash, locked.Plan
	} else {
		actor := executionStartActor{Kind: "AI_SESSION", ID: id.AISessionID, RequestID: request, Action: "START_EXECUTION"}
		if err = createQualifiedApprovedExecutionForStartTx(ctx, tx, t, id.AISessionID, s.now(), actor, &expected, descriptor); err != nil {
			return err
		}
		execution = approvedExecutionID(t)
		effect := executionStartEffectRecord{ProjectID: t.ProjectID, ActorKind: actor.Kind, ActorID: actor.ID, RequestID: request, Action: actor.Action, Effect: wp.StartEffectStartAuthorized, TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, RevisionID: t.CurrentDefinitionRevisionID, ExecutionID: execution, Phase: t.Phase}
		if err = insertExecutionStartEffectTx(ctx, tx, effect, formatTime(s.now())); err != nil {
			return err
		}
		payload, err = executionStartEffectPayload(effect)
		if err != nil {
			return err
		}
		admission, e := admitQualifiedExecutionTx(ctx, tx, execution, request, payload, s.now(), descriptor)
		if e != nil {
			return e
		}
		plan, err = readArchivedRuntimeSelectionPlanTx(ctx, tx, admission.PlanID)
		if err != nil {
			return err
		}
	}
	frozen, _ := json.Marshal(intent.Content)
	l := formalapproval.Lineage{ProjectID: t.ProjectID, TaskID: t.ID, ReviewID: prior.Snapshot.Fence.ReviewID, DecisionID: prior.Snapshot.Fence.DecisionID, DecisionVersion: prior.Snapshot.Fence.DecisionVersion, IntentID: id.IntentID, IntentVersion: id.IntentVersion, VariantID: c.VariantID, CommittedResultID: execution, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID}
	revisionHash, definitionJSON := prior.Snapshot.RevisionHash, prior.Snapshot.DefinitionJSON
	if locked != nil {
		l.ReviewID, l.DecisionID, l.DecisionVersion = locked.Archive.Fence.ReviewID, locked.Archive.Fence.DecisionID, locked.Archive.Fence.DecisionVersion
		revisionHash, definitionJSON = locked.Archive.RevisionHash, locked.Archive.DefinitionJSON
	}
	a := fe.AdoptionArchive{Identity: id, FrozenPrepareJSON: frozen, Commit: c, OriginalRequestJSON: json.RawMessage(original), PriorApproval: prior, PriorRuntimeApproval: locked, AtomicConfirmation: true, PriorApprovalRole: formalapproval.PriorReceiptRole, ProjectID: t.ProjectID, TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, RevisionID: t.CurrentDefinitionRevisionID, RevisionHash: revisionHash, DefinitionJSON: definitionJSON, ExpectedTarget: expected, FrozenLineage: l, ResultLineage: l, PlatformReference: ref, Descriptor: descriptor, Evidence: evidence, Qualification: qualification, Runtime: *c.Selection.RuntimeConfig, Backend: *c.BackendSelection, ExecutionID: execution, AdmissionRequestID: admissionRequest, AdmissionPayloadHash: payload, Plan: plan, PreparationAuthorityID: plan.ID}
	if err = a.Validate(); err != nil {
		return fmt.Errorf("validate new formal adoption archive: %w", err)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_adoption_business(ai_session_id,request_id,intent_id,project_id,task_id,revision_id,execution_id,admission_request_id,business_sha256,business_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, id.AISessionID, id.RequestID, id.IntentID, t.ProjectID, t.ID, t.CurrentDefinitionRevisionID, execution, admissionRequest, fe.HashBytes(raw), string(raw))
	if err != nil {
		return err
	}
	return nil
}

// This decoder is deliberately independent of mutable preparation disposition,
// Task pointers and Run state, and still recomputes the full immutable plan.
func readArchivedRuntimeSelectionPlanTx(ctx context.Context, q definitionQuerier, id string) (p runtimepreparation.Plan, err error) {
	var targets, refs, heads, scopes, created string
	var fast int
	err = q.QueryRowContext(ctx, `SELECT plan_id,generation,owner_kind,owner_id,project_id,COALESCE(feature_area_id,''),COALESCE(task_id,''),source_command,source_receipt_id,selected_variant_id,proposal_hash,task_definition_revision_id,source_execution_id,source_review_id,successor_execution_id,successor_review_id,repository_targets_json,primary_member_id,backend_identity,model,reasoning_effort,runtime_adapter_identity,topology_revision,development_context_hash,development_refs_json,expected_source_heads_json,write_scopes_json,preparation_request_id,preparation_payload_hash,content_hash,created_at,backend_config_fingerprint,backend_source,backend_kind,codex_fast_mode,service_tier,runtime_protocol_identity,authority_snapshot_hash FROM runtime_scope_plans WHERE plan_id=?`, id).Scan(&p.ID, &p.Generation, &p.OwnerKind, &p.OwnerID, &p.ProjectID, &p.FeatureAreaID, &p.TaskID, &p.SourceCommand, &p.SourceReceiptID, &p.SelectedVariantID, &p.ProposalHash, &p.TaskDefinitionRevisionID, &p.SourceExecutionID, &p.SourceReviewID, &p.SuccessorExecutionID, &p.SuccessorReviewID, &targets, &p.PrimaryMemberID, &p.BackendIdentity, &p.Model, &p.ReasoningEffort, &p.RuntimeAdapterIdentity, &p.TopologyRevision, &p.DevelopmentContextHash, &refs, &heads, &scopes, &p.PreparationRequestID, &p.PreparationPayloadHash, &p.ContentHash, &created, &p.BackendConfigFingerprint, &p.BackendSource, &p.BackendKind, &fast, &p.ServiceTier, &p.RuntimeProtocolIdentity, &p.AuthoritySnapshotHash)
	if err != nil {
		return
	}
	p.CodexFastMode = fast != 0
	for _, v := range []struct {
		raw string
		dst any
	}{{targets, &p.RepositoryTargets}, {refs, &p.DevelopmentRefs}, {heads, &p.ExpectedSourceHEADs}, {scopes, &p.WriteScopes}} {
		if err = json.Unmarshal([]byte(v.raw), v.dst); err != nil {
			return
		}
	}
	p.CreatedAt, err = parseTime(created)
	if err != nil {
		return
	}
	err = p.Validate()
	return
}
