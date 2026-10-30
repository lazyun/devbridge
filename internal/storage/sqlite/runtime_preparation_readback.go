package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// ReadExecutionPreparationPlan reads the immutable admission for this exact
// current implementation Run. It grants no workspace or dispatch authority.
func (s *RunStore) ReadExecutionPreparationPlan(ctx context.Context, runID string) (runtimepreparation.Plan, error) {
	var p runtimepreparation.Plan
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var targets, refs, heads, scopes, created string
	var fast int
	err = tx.QueryRowContext(ctx, `SELECT p.plan_id,p.generation,p.owner_kind,p.owner_id,p.project_id,COALESCE(p.feature_area_id,''),COALESCE(p.task_id,''),p.source_command,p.source_receipt_id,p.selected_variant_id,p.proposal_hash,p.task_definition_revision_id,p.source_execution_id,p.source_review_id,p.successor_execution_id,p.successor_review_id,p.repository_targets_json,p.primary_member_id,p.backend_identity,p.model,p.reasoning_effort,p.runtime_adapter_identity,p.topology_revision,p.development_context_hash,p.development_refs_json,p.expected_source_heads_json,p.write_scopes_json,p.preparation_request_id,p.preparation_payload_hash,p.content_hash,p.created_at,p.backend_config_fingerprint,p.backend_source,p.backend_kind,p.codex_fast_mode,p.service_tier,p.runtime_protocol_identity,p.authority_snapshot_hash,p.agent_id,p.execution_policy,p.agent_config_fingerprint FROM runtime_scope_plans p JOIN runtime_preparation_attempts a ON a.plan_id=p.plan_id WHERE a.run_id=?`, runID).Scan(&p.ID, &p.Generation, &p.OwnerKind, &p.OwnerID, &p.ProjectID, &p.FeatureAreaID, &p.TaskID, &p.SourceCommand, &p.SourceReceiptID, &p.SelectedVariantID, &p.ProposalHash, &p.TaskDefinitionRevisionID, &p.SourceExecutionID, &p.SourceReviewID, &p.SuccessorExecutionID, &p.SuccessorReviewID, &targets, &p.PrimaryMemberID, &p.BackendIdentity, &p.Model, &p.ReasoningEffort, &p.RuntimeAdapterIdentity, &p.TopologyRevision, &p.DevelopmentContextHash, &refs, &heads, &scopes, &p.PreparationRequestID, &p.PreparationPayloadHash, &p.ContentHash, &created, &p.BackendConfigFingerprint, &p.BackendSource, &p.BackendKind, &fast, &p.ServiceTier, &p.RuntimeProtocolIdentity, &p.AuthoritySnapshotHash, &p.AgentID, &p.ExecutionPolicy, &p.AgentConfigFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return p, run.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.CodexFastMode = fast != 0
	for _, v := range []struct {
		raw string
		dst any
	}{{targets, &p.RepositoryTargets}, {refs, &p.DevelopmentRefs}, {heads, &p.ExpectedSourceHEADs}, {scopes, &p.WriteScopes}} {
		if err = json.Unmarshal([]byte(v.raw), v.dst); err != nil {
			return p, err
		}
	}
	p.CreatedAt, err = parseTime(created)
	if err != nil {
		return p, err
	}
	if err = p.Validate(); err != nil {
		return p, err
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN execution_proposals e ON e.id=r.execution_proposal_id JOIN tasks t ON t.id=r.task_id JOIN execution_proposal_admissions a ON a.proposal_id=e.id JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_scope_plan_id WHERE r.id=? AND r.runtime_epoch=? AND r.status IN ('CREATED','PREPARING') AND r.purpose='IMPLEMENTATION' AND e.id=? AND e.run_id=r.id AND e.status='CONSUMED' AND e.outcome='RUNNING' AND t.current_execution_id=e.id AND t.current_definition_revision_id=? AND t.status='ACTIVE' AND a.runtime_scope_plan_id=? AND auth.disposition='CURRENT' AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id)))`, runID, s.db.epoch, p.OwnerID, p.TaskDefinitionRevisionID, p.ID).Scan(&current)
	if err != nil {
		return p, err
	}
	if !current || p.OwnerKind != runtimepreparation.OwnerExecution && p.OwnerKind != runtimepreparation.OwnerImplementation {
		return p, ErrConflict
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, p.ProjectID)
	if err != nil {
		return p, err
	}
	if topology != p.TopologyRevision || development != p.DevelopmentContextHash {
		return p, ErrConflict
	}
	return p, tx.Commit()
}

// ReadWorkspaceObservations supports binding recovery without recapturing or
// replacing an already receipted observation set.
func (s *RunStore) ReadWorkspaceObservations(ctx context.Context, planID, runID string) ([]runtimepreparation.WorkspaceObservation, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT observation_id,workspace_id,repository_id,member_id,worktree_id,canonical_root,root_identity,start_head,start_snapshot,branch,metadata_digest,observation_generation,observed_at FROM runtime_workspace_observations WHERE plan_id=? AND run_id=? ORDER BY member_id`, planID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runtimepreparation.WorkspaceObservation
	for rows.Next() {
		o := runtimepreparation.WorkspaceObservation{PlanID: planID, RunID: runID}
		var at string
		if err = rows.Scan(&o.ID, &o.WorkspaceID, &o.RepositoryID, &o.MemberID, &o.WorktreeID, &o.CanonicalRoot, &o.RootIdentity, &o.StartHEAD, &o.StartSnapshot, &o.Branch, &o.MetadataDigest, &o.Generation, &at); err != nil {
			return nil, err
		}
		o.ObservedAt, err = parseTime(at)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
