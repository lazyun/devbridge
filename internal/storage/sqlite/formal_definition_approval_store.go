package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalapproval"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// formalConfirmationTx validates original immutable confirmation evidence
// without querying current Session permissions or Task/Review eligibility.
// Authentication belongs to the ingress; archived identities remain readable
// after ownership changes. Use the same ambiguity predicate as the live reader.
func formalConfirmationTx(ctx context.Context, q definitionQuerier, session, id string) (out confirmation.Intent, err error) {
	var raw, project, prepareHash string
	err = q.QueryRowContext(ctx, `SELECT intent_json,project_id,prepare_hash FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, id, session).Scan(&raw, &project, &prepareHash)
	if err != nil {
		return out, err
	}
	var ambiguous bool
	err = q.QueryRowContext(ctx, `SELECT `+confirmationAmbiguousJSON("document")+` FROM (SELECT ? AS document)`, raw).Scan(&ambiguous)
	if err != nil {
		return out, err
	}
	if ambiguous || json.Unmarshal([]byte(raw), &out) != nil || out.IntentID != id || out.AISessionID != session || out.ProjectID != project || out.Version < 1 || out.ContentHash != prepareHash || out.Content.AISessionID != session || out.Content.Purpose != out.Purpose || out.Content.Action != out.Action {
		return out, formalapproval.ErrCorrupt
	}
	frozen, err := json.Marshal(out.Content)
	if err != nil || formalapproval.HashBytes(frozen) != prepareHash {
		return out, formalapproval.ErrCorrupt
	}
	if out.Commit != nil {
		if !confirmationCommitIdentity(out, *out.Commit) {
			return out, formalapproval.ErrCorrupt
		}
		var typed string
		err = q.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, session, out.Commit.RequestID, id).Scan(&typed)
		if err != nil || typed != formalapproval.HashCommit(*out.Commit) {
			return out, errors.Join(err, formalapproval.ErrCorrupt)
		}
	}
	return out, nil
}

// formalReviewRevisionTx follows an actual durable Review admission source.
// A current Task revision alone is not evidence of what an old Review read.
func formalReviewRevisionTx(ctx context.Context, q definitionQuerier, project, taskID, reviewID string) (string, error) {
	var revision string
	var payload, hash, admittedHash string
	err := q.QueryRowContext(ctx, `SELECT a.revision_id,r.payload_json,r.payload_sha256,a.revision_hash
 FROM review_definition_admissions a JOIN reviews v ON v.id=a.review_id AND v.project_id=a.project_id AND v.task_id=a.task_id
 JOIN task_definition_revisions r ON r.id=a.revision_id AND r.task_id=a.task_id
 WHERE a.review_id=? AND a.task_id=? AND a.project_id=?`, reviewID, taskID, project).Scan(&revision, &payload, &hash, &admittedHash)
	if err == nil {
		definition, decodeErr := task.DecodeDefinition(payload, hash)
		if decodeErr != nil || admittedHash != hash || definition.ProjectID != project {
			return "", formalapproval.ErrCorrupt
		}
		return revision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = q.QueryRowContext(ctx, `SELECT req.source_revision_id FROM task_supplemental_review_starts started
 JOIN task_supplemental_review_requests req ON req.id=started.request_id
 JOIN reviews v ON v.id=started.review_id AND v.task_id=req.task_id AND v.project_id=req.project_id
 WHERE v.id=? AND v.task_id=? AND v.project_id=?`, reviewID, taskID, project).Scan(&revision)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return revision, err
	}
	var intentID, session, startRequest string
	err = q.QueryRowContext(ctx, `SELECT i.intent_id,i.ai_session_id,admitted.request_id
 FROM reviews v JOIN confirmation_intents i ON i.intent_id=v.runtime_confirmation_id AND i.ai_session_id=v.created_by_ai_session_id AND i.project_id=v.project_id
 JOIN feature_area_review_start_requests admitted ON admitted.review_id=v.id AND admitted.run_id=v.linked_run_id AND admitted.outcome='ACCEPTED'
 WHERE v.id=? AND v.task_id=? AND v.project_id=?
 `, reviewID, taskID, project).Scan(&intentID, &session, &startRequest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	intent, err := formalConfirmationTx(ctx, q, session, intentID)
	if err != nil {
		return "", err
	}
	b := intent.Content.Review
	if intent.Purpose != confirmation.Review || intent.Action != "START_REVIEW" || intent.Commit == nil || intent.ProjectID != project || b == nil || b.TaskID != taskID || b.ExpectedTarget == nil {
		return "", nil
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil || formalapproval.HashBytes(raw) != intent.ContentHash {
		return "", formalapproval.ErrCorrupt
	}
	matched := 0
	for _, step := range intent.Steps {
		if step.Kind == "START_REVIEW" && step.RequestID == startRequest {
			matched++
		}
	}
	if matched != 1 {
		return "", formalapproval.ErrCorrupt
	}
	return b.ExpectedTarget.TaskDefinitionRevisionID, nil
}

// captureDefinitionApprovalPrepareTx arms only new, fully sourced v2 Coding
// definition approvals. It is called once by the original prepare producer;
// replay never creates a missing fence for historical confirmations.
func captureDefinitionApprovalPrepareTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	b := intent.Content.Execution
	if intent.Purpose != confirmation.Execution || intent.Action != "APPROVE" || b == nil || b.TaskDefinitionRevisionID == "" || len(intent.Content.Variants) == 0 {
		return nil
	}
	var parent, parentHash string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, b.TaskDefinitionRevisionID, b.TaskID).Scan(&parent, &parentHash); err != nil {
		return err
	}
	def, err := task.DecodeDefinition(parent, parentHash)
	if err != nil {
		return err
	}
	var parentSchema *formalapproval.SchemaReference
	if def.Version != 2 {
		draft := formalapproval.DraftParentSchema()
		if _, err := formalapproval.DecodeParentDefinition(json.RawMessage(parent), parentHash, &draft, formalapproval.SchemaReference{SchemaID: "TASK_DEFINITION", DefinitionVersion: 2, FactSchemaID: "FORMAL_TASK_DEFINITION"}); err != nil {
			return nil
		}
		parentSchema = &draft
	}
	// Formal applicability belongs to the durable Review proposal. An unused
	// alternate client variant must not turn a Coding approval into an unarmed
	// legacy path. Resolve independently checks the actually selected variant.
	var resultJSON string
	err = tx.QueryRowContext(ctx, `SELECT result_text FROM review_approval_decisions WHERE decision_id=? AND review_id=? AND task_id=? AND project_id=?`, b.DecisionID, b.ReviewID, b.TaskID, intent.ProjectID).Scan(&resultJSON)
	if err != nil {
		return err
	}
	proposal, err := parsePersistedApprovalResult(resultJSON)
	if err != nil {
		return err
	}
	if proposal.Version != 2 || proposal.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return nil
	}
	fromReview, err := formalReviewRevisionTx(ctx, tx, intent.ProjectID, b.TaskID, b.ReviewID)
	if err != nil || fromReview == "" {
		return err
	}
	if fromReview != b.TaskDefinitionRevisionID {
		return formalapproval.ErrConflict
	}
	decision, err := reviewDecisionSnapshotTx(ctx, tx, b.DecisionID)
	if err != nil {
		return err
	}
	if decision.ProjectID != intent.ProjectID || decision.TaskID != b.TaskID || decision.ReviewID != b.ReviewID || decision.CurrentReviewID != b.ReviewID || decision.Status != reviewDecisionPending || !decision.Durable || decision.Consumed {
		return formalapproval.ErrConflict
	}
	frozen, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	if err = validateFormalConfirmationContextTx(ctx, tx, intent); err != nil {
		return err
	}
	f := formalapproval.PrepareFence{AISessionID: intent.AISessionID, IntentID: intent.IntentID, ProjectID: intent.ProjectID, TaskID: b.TaskID, ReviewID: b.ReviewID, DecisionID: b.DecisionID, DecisionVersion: decision.Version, ParentRevisionID: b.TaskDefinitionRevisionID, ParentRevisionHash: parentHash, ContentHash: intent.ContentHash, IntentVersion: intent.Version, FrozenPrepareJSON: frozen, ParentDefinitionJSON: json.RawMessage(parent), Schema: formalapproval.SchemaReference{SchemaID: "TASK_DEFINITION", DefinitionVersion: 2, FactSchemaID: "FORMAL_TASK_DEFINITION"}}
	f.ParentSchema = parentSchema
	if _, err = formalapproval.LookupSchema(f.Schema, string(intent.Purpose), intent.Action); err != nil {
		return err
	}
	if err = f.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_definition_approval_fences(intent_id,ai_session_id,intent_version,project_id,task_id,review_id,decision_id,parent_revision_id,content_hash,fence_json,fence_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, f.IntentID, f.AISessionID, f.IntentVersion, f.ProjectID, f.TaskID, f.ReviewID, f.DecisionID, f.ParentRevisionID, f.ContentHash, string(encoded), formalapproval.HashBytes(encoded))
	return err
}

func readDefinitionApprovalFenceTx(ctx context.Context, q definitionQuerier, session, intentID string) (f formalapproval.PrepareFence, err error) {
	var raw, digest, project, taskID, reviewID, decisionID, parent, content string
	var version int
	err = q.QueryRowContext(ctx, `SELECT fence_json,fence_sha256,project_id,task_id,review_id,decision_id,parent_revision_id,content_hash,intent_version FROM formal_definition_approval_fences WHERE intent_id=? AND ai_session_id=?`, intentID, session).Scan(&raw, &digest, &project, &taskID, &reviewID, &decisionID, &parent, &content, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return f, formalapproval.ErrNotApplicable
	}
	if err != nil {
		return f, err
	}
	f, err = formalapproval.DecodeFence([]byte(raw), digest)
	if err != nil || f.IntentID != intentID || f.AISessionID != session || f.ProjectID != project || f.TaskID != taskID || f.ReviewID != reviewID || f.DecisionID != decisionID || f.ParentRevisionID != parent || f.ContentHash != content || f.IntentVersion != version {
		return f, formalapproval.ErrCorrupt
	}
	return f, f.Validate()
}

// prepareDefinitionApprovalBusinessTx runs before Decision consumption. It
// obtains the original submitted fingerprint from the immutable request ledger,
// while independently recomputing the normalized typed commit fingerprint.
func prepareDefinitionApprovalBusinessTx(ctx context.Context, tx *sql.Tx, in wp.ResolveInput, current task.Task) (*formalapproval.BusinessSnapshot, error) {
	if in.ConfirmationIntentID == "" {
		return nil, nil
	}
	f, err := readDefinitionApprovalFenceTx(ctx, tx, in.AISessionID, in.ConfirmationIntentID)
	if errors.Is(err, formalapproval.ErrNotApplicable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = formalapproval.LookupSchema(f.Schema, "EXECUTION", "APPROVE"); err != nil {
		return nil, err
	}
	if in.Option != "APPROVE" || in.StartEffect != wp.StartEffectNoStart || in.NextRunRuntimeConfig != nil || in.BackendSelection != nil || in.OperatorPrincipalID != "" || current.ID != f.TaskID || current.ProjectID != f.ProjectID || current.CurrentReviewID != f.ReviewID || current.CurrentDefinitionRevisionID != f.ParentRevisionID || in.DecisionID != f.DecisionID {
		return nil, formalapproval.ErrConflict
	}
	decision, err := reviewDecisionSnapshotTx(ctx, tx, f.DecisionID)
	if err != nil {
		return nil, err
	}
	if decision.Version != f.DecisionVersion || decision.Status != reviewDecisionPending || decision.Consumed || !decision.Durable {
		return nil, formalapproval.ErrConflict
	}
	intent, err := confirmationRead(ctx, tx, f.AISessionID, f.IntentID)
	if err != nil {
		return nil, err
	}
	if intent.Commit == nil || intent.State != "CONFIRMING" || intent.Version != f.IntentVersion || intent.ContentHash != f.ContentHash {
		return nil, formalapproval.ErrConflict
	}
	if err = validateFormalConfirmationContextTx(ctx, tx, intent); err != nil {
		return nil, err
	}
	count := 0
	for _, step := range intent.Steps {
		if step.Kind == "APPROVE" && step.RequestID == in.RequestID && step.State == "UNKNOWN" {
			count++
		}
	}
	if count != 1 {
		return nil, formalapproval.ErrConflict
	}
	var original, submitted, typed string
	err = tx.QueryRowContext(ctx, `SELECT request_json,submitted_sha256,payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, f.AISessionID, intent.Commit.RequestID, f.IntentID).Scan(&original, &submitted, &typed)
	if err != nil {
		return nil, err
	}
	if typed != formalapproval.HashCommit(*intent.Commit) {
		return nil, formalapproval.ErrCorrupt
	}
	core := formalapproval.CoreSnapshot{ProjectID: current.ProjectID, TaskID: current.ID, TaskStatus: string(current.Status), TaskPhase: string(current.Phase), CurrentRevisionID: current.CurrentDefinitionRevisionID, CurrentExecutionID: current.CurrentExecutionID, CurrentReviewID: current.CurrentReviewID, DecisionID: f.DecisionID, DecisionState: decision.Status, CurrentConfirmationIntentID: f.IntentID, CurrentConfirmationSessionID: f.AISessionID, CurrentConfirmationVersionHash: confirmation.ValueHash([]any{f.IntentVersion, f.ContentHash})}
	err = tx.QueryRowContext(ctx, `SELECT v.id,v.project_id,v.task_id,m.outcome FROM reviews v JOIN review_result_materializations m ON m.review_id=v.id AND m.run_id=v.linked_run_id WHERE v.id=? AND v.task_id=? AND v.project_id=? AND m.outcome='VALID'`, f.ReviewID, f.TaskID, f.ProjectID).Scan(&core.ReviewID, &core.ReviewProjectID, &core.ReviewTaskID, &core.ReviewOutcome)
	if err != nil {
		return nil, err
	}
	core.ReviewRevisionID, err = formalReviewRevisionTx(ctx, tx, f.ProjectID, f.TaskID, f.ReviewID)
	if err != nil || core.ReviewRevisionID != f.ParentRevisionID {
		return nil, errors.Join(err, formalapproval.ErrConflict)
	}
	return &formalapproval.BusinessSnapshot{Fence: f, Commit: *intent.Commit, TypedPayloadHash: typed, SubmittedHash: submitted, OriginalRequestJSON: json.RawMessage(original), ApprovalStepRequestID: in.RequestID, Core: core, ResultLineage: formalapproval.Lineage{DecisionVersion: decision.Version}}, nil
}

// sealDefinitionApprovalBusinessTx seals the business archive, final receipt and
// confirmation completion in the original APPROVE writer transaction.
func sealDefinitionApprovalBusinessTx(ctx context.Context, tx *sql.Tx, prepared *formalapproval.BusinessSnapshot, value task.Task) error {
	if prepared == nil {
		return nil
	}
	s := *prepared
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, value.CurrentDefinitionRevisionID, s.Fence.TaskID).Scan(&raw, &s.RevisionHash); err != nil {
		return err
	}
	s.DefinitionJSON, s.RevisionID = json.RawMessage(raw), value.CurrentDefinitionRevisionID
	// decision_id is the immutable approval business receipt's primary key.
	s.ResultID = s.Fence.DecisionID
	s.FrozenLineage = formalapproval.Lineage{ProjectID: s.Fence.ProjectID, TaskID: s.Fence.TaskID, ReviewID: s.Fence.ReviewID, DecisionID: s.Fence.DecisionID, DecisionVersion: s.Fence.DecisionVersion, IntentID: s.Fence.IntentID, IntentVersion: s.Fence.IntentVersion, VariantID: s.Commit.VariantID, CommittedResultID: s.ResultID, TaskDefinitionRevisionID: s.RevisionID}
	// Read the committed producer identity independently, rather than copying
	// the frozen lineage and thereby making its equality check tautological.
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.task_id,d.review_id,d.decision_id,a.intent_id,a.revision_id FROM review_approval_decisions d JOIN confirmation_approval_sources a ON a.decision_id=d.decision_id JOIN decision_resolution_receipts r ON r.decision_id=d.decision_id WHERE d.decision_id=? AND r.request_id=? AND r.actor_kind='AI_SESSION' AND r.actor_id=? AND r.option='APPROVE'`, s.ResultID, s.ApprovalStepRequestID, s.Fence.AISessionID).Scan(&s.ResultLineage.ProjectID, &s.ResultLineage.TaskID, &s.ResultLineage.ReviewID, &s.ResultLineage.CommittedResultID, &s.ResultLineage.IntentID, &s.ResultLineage.TaskDefinitionRevisionID)
	if err != nil {
		return err
	}
	s.ResultLineage.DecisionID = s.ResultID
	// DecisionVersion was independently captured before consuming the Decision.
	s.ResultLineage.IntentVersion = s.Commit.Version
	s.ResultLineage.VariantID = s.Commit.VariantID
	if err = s.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_definition_approval_business(ai_session_id,request_id,intent_id,approval_step_request_id,decision_id,revision_id,snapshot_hash,snapshot_json) VALUES(?,?,?,?,?,?,?,?)`, s.Fence.AISessionID, s.Commit.RequestID, s.Fence.IntentID, s.ApprovalStepRequestID, s.ResultID, s.RevisionID, s.ComputeHash(), string(encoded))
	if err != nil {
		return err
	}
	r, err := formalapproval.NewReceipt(s)
	if err != nil {
		return err
	}
	receiptJSON, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO formal_definition_approval_receipts(ai_session_id,request_id,snapshot_hash,receipt_identity,receipt_json,receipt_sha256) VALUES(?,?,?,?,?,?)`, s.Fence.AISessionID, s.Commit.RequestID, r.SnapshotHash, r.ReceiptIdentity, string(receiptJSON), r.ComputeHash()); err != nil {
		return err
	}
	return completeFormalConfirmationTx(ctx, tx, s.Fence.AISessionID, s.Fence.IntentID, s.Commit.RequestID, "APPROVE", s.Fence.TaskID, s.RevisionID, "")
}

func readDefinitionApprovalBusinessTx(ctx context.Context, q definitionQuerier, id formalapproval.ReadIdentity) (s formalapproval.BusinessSnapshot, err error) {
	var raw, digest, intent, step, decision, revision string
	err = q.QueryRowContext(ctx, `SELECT snapshot_json,snapshot_hash,intent_id,approval_step_request_id,decision_id,revision_id FROM formal_definition_approval_business WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&raw, &digest, &intent, &step, &decision, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return s, formalapproval.ErrMissing
	}
	if err != nil {
		return s, err
	}
	s, err = formalapproval.DecodeSnapshot([]byte(raw), digest)
	if err != nil || s.Fence.IntentID != intent || s.ApprovalStepRequestID != step || s.ResultID != decision || s.RevisionID != revision {
		return s, formalapproval.ErrCorrupt
	}
	if err = id.MatchesSnapshot(s); err != nil {
		return s, err
	}
	f, err := readDefinitionApprovalFenceTx(ctx, q, id.AISessionID, id.IntentID)
	if err != nil || f.ComputeHash() != s.Fence.ComputeHash() {
		return s, errors.Join(err, formalapproval.ErrCorrupt)
	}
	if err = validateDefinitionApprovalSourcesTx(ctx, q, s); err != nil {
		return s, err
	}
	return s, nil
}

// Every join here is historical and immutable. Current ownership, catalog
// eligibility and Decision state deliberately do not participate in readback.
func validateDefinitionApprovalSourcesTx(ctx context.Context, q definitionQuerier, s formalapproval.BusinessSnapshot) error {
	f := s.Fence
	commitJSON, err := json.Marshal(s.Commit)
	if err != nil {
		return err
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_commit_requests c
 JOIN confirmation_intents i ON i.intent_id=c.intent_id AND i.ai_session_id=c.ai_session_id
 JOIN review_approval_decisions d ON d.decision_id=? AND d.project_id=i.project_id
 JOIN decision_resolution_receipts r ON r.decision_id=d.decision_id AND r.project_id=d.project_id AND r.subject_id=d.task_id AND r.result_id=d.task_id
 JOIN confirmation_approval_sources a ON a.decision_id=d.decision_id AND a.intent_id=i.intent_id
 JOIN review_approval_materializations m ON m.decision_id=d.decision_id AND m.task_id=d.task_id AND m.initial_revision_id=a.revision_id
 JOIN task_definition_revisions parent ON parent.id=? AND parent.task_id=d.task_id
 JOIN task_definition_revisions output ON output.id=a.revision_id AND output.task_id=d.task_id
 JOIN review_result_materializations materialized ON materialized.review_id=d.review_id AND materialized.outcome='VALID'
 WHERE c.ai_session_id=? AND c.request_id=? AND c.intent_id=? AND c.outcome='CONSUMED'
 AND c.payload_sha256=? AND c.submitted_sha256=? AND c.request_json=?
 AND i.project_id=? AND json_extract(i.intent_json,'$.version')=? AND i.prepare_hash=?
 AND json_extract(i.intent_json,'$.content')=? AND json_extract(i.intent_json,'$.commit')=?
 AND d.task_id=? AND d.review_id=?
 AND r.decision_kind='REVIEW_APPROVAL' AND r.subject_kind='TASK_REVIEW_GATE' AND r.result_kind='DEVELOPMENT_TASK'
 AND r.option='APPROVE' AND r.request_id=? AND r.actor_kind='AI_SESSION' AND r.actor_id=c.ai_session_id
 AND m.actor_kind='AI_SESSION' AND m.actor_id=c.ai_session_id AND m.ai_session_id=c.ai_session_id
 AND parent.payload_json=? AND parent.payload_sha256=?
 AND output.id=? AND output.payload_json=? AND output.payload_sha256=?)`,
		s.ResultID, f.ParentRevisionID, f.AISessionID, s.Commit.RequestID, f.IntentID, s.TypedPayloadHash, s.SubmittedHash, string(s.OriginalRequestJSON), f.ProjectID, f.IntentVersion, f.ContentHash, string(f.FrozenPrepareJSON), string(commitJSON), f.TaskID, f.ReviewID, s.ApprovalStepRequestID, string(f.ParentDefinitionJSON), f.ParentRevisionHash, s.RevisionID, string(s.DefinitionJSON), s.RevisionHash).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return formalapproval.ErrCorrupt
	}
	// Check the shared strict reader as well: SQL JSON extraction alone must
	// never promote a historical alias/duplicate-key document into authority.
	intent, err := formalConfirmationTx(ctx, q, f.AISessionID, f.IntentID)
	if err != nil || intent.Commit == nil || formalapproval.HashCommit(*intent.Commit) != s.TypedPayloadHash {
		return errors.Join(err, formalapproval.ErrCorrupt)
	}
	revision, err := formalReviewRevisionTx(ctx, q, f.ProjectID, f.TaskID, f.ReviewID)
	if err != nil || revision != f.ParentRevisionID {
		return errors.Join(err, formalapproval.ErrCorrupt)
	}
	return nil
}

func readDefinitionApprovalReceiptTx(ctx context.Context, q definitionQuerier, id formalapproval.ReadIdentity) (r formalapproval.Receipt, err error) {
	var raw, identity, snapshotHash, receiptHash string
	err = q.QueryRowContext(ctx, `SELECT receipt_json,receipt_identity,snapshot_hash,receipt_sha256 FROM formal_definition_approval_receipts WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID).Scan(&raw, &identity, &snapshotHash, &receiptHash)
	if errors.Is(err, sql.ErrNoRows) {
		return r, formalapproval.ErrMissing
	}
	if err != nil {
		return r, err
	}
	r, err = formalapproval.DecodeReceipt([]byte(raw), receiptHash)
	if err != nil || r.ReceiptIdentity != identity || r.SnapshotHash != snapshotHash {
		return r, formalapproval.ErrCorrupt
	}
	if err = r.Validate(); err != nil {
		return r, err
	}
	if err = id.MatchesReceipt(r); err != nil {
		return r, err
	}
	basis, err := readDefinitionApprovalBusinessTx(ctx, q, id)
	if err != nil || basis.ComputeHash() != r.SnapshotHash {
		return r, errors.Join(err, formalapproval.ErrCorrupt)
	}
	return r, nil
}

// ReadDefinitionApproval reads only immutable archived identity. Permission
// checks belong to the caller; current Task/Review/session state is irrelevant
// to a historical receipt and is intentionally not queried here.
func (s *ConfirmationStore) ReadDefinitionApproval(ctx context.Context, id formalapproval.ReadIdentity) (formalapproval.Receipt, error) {
	if err := id.Validate(); err != nil {
		return formalapproval.Receipt{}, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return formalapproval.Receipt{}, err
	}
	defer tx.Rollback()
	return readDefinitionApprovalReceiptTx(ctx, tx, id)
}

// FinalizeDefinitionApproval is retained as an immutable readback port. A
// receipt-less committed business prefix is corruption, never backfill authority.
func (s *ConfirmationStore) FinalizeDefinitionApproval(ctx context.Context, id formalapproval.ReadIdentity) (formalapproval.Receipt, error) {
	prior, err := s.ReadDefinitionApproval(ctx, id)
	if err == nil || !errors.Is(err, formalapproval.ErrMissing) {
		return prior, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return prior, err
	}
	defer tx.Rollback()
	fence, fenceErr := readDefinitionApprovalFenceTx(ctx, tx, id.AISessionID, id.IntentID)
	if fenceErr != nil {
		return prior, fenceErr
	}
	if fence.IntentVersion != id.IntentVersion || fence.ContentHash != id.ContentHash {
		return prior, formalapproval.ErrConflict
	}
	if _, err = readDefinitionApprovalBusinessTx(ctx, tx, id); err == nil {
		return prior, formalapproval.ErrCorrupt
	}
	return prior, err
}

func definitionApprovalIdentityTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent) (formalapproval.ReadIdentity, error) {
	if intent.Commit == nil {
		return formalapproval.ReadIdentity{}, formalapproval.ErrMissing
	}
	id := formalapproval.ReadIdentity{AISessionID: intent.AISessionID, IntentID: intent.IntentID, IntentVersion: intent.Version, RequestID: intent.Commit.RequestID, ContentHash: intent.ContentHash, TypedPayloadHash: formalapproval.HashCommit(*intent.Commit)}
	var typed string
	err := q.QueryRowContext(ctx, `SELECT submitted_sha256,payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, id.AISessionID, id.RequestID, id.IntentID).Scan(&id.SubmittedHash, &typed)
	if err != nil {
		return id, err
	}
	if typed != id.TypedPayloadHash {
		return id, formalapproval.ErrCorrupt
	}
	return id, id.Validate()
}

// recoverDefinitionApprovalTx never finalizes or repairs an archive. It only
// distinguishes a committed business basis from its final formal receipt.
func recoverDefinitionApprovalTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent, step *confirmation.Step) (bool, error) {
	_, err := readDefinitionApprovalFenceTx(ctx, q, intent.AISessionID, intent.IntentID)
	if errors.Is(err, formalapproval.ErrNotApplicable) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	id, err := definitionApprovalIdentityTx(ctx, q, intent)
	if err != nil {
		return true, err
	}
	basis, err := readDefinitionApprovalBusinessTx(ctx, q, id)
	if errors.Is(err, formalapproval.ErrMissing) {
		return true, sql.ErrNoRows
	}
	if err != nil {
		return true, err
	}
	if step.RequestID != basis.ApprovalStepRequestID {
		return true, formalapproval.ErrConflict
	}
	if step.State == "COMPLETED" && (step.TaskID != basis.Fence.TaskID || step.RevisionID != basis.RevisionID || step.ExecutionID != "" || step.RunID != "" || step.WorkflowID != "") {
		return true, formalapproval.ErrCorrupt
	}
	step.TaskID, step.RevisionID = basis.Fence.TaskID, basis.RevisionID
	_, err = readDefinitionApprovalReceiptTx(ctx, q, id)
	if errors.Is(err, formalapproval.ErrMissing) {
		return true, formalapproval.ErrCorrupt
	}
	return true, err
}
