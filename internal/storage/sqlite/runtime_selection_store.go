package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

type RuntimeSelectionStore struct{ db *DB }

func NewRuntimeSelectionStore(db *DB) *RuntimeSelectionStore { return &RuntimeSelectionStore{db: db} }

var _ runtimeselection.Store = (*RuntimeSelectionStore)(nil)

// Validate an explicitly named generation. Fresh callers additionally compare
// the lineage version under their own writer boundary; historical replay never
// calls this lifecycle gate.
func validateRuntimeSelectionPlatformTx(ctx context.Context, q definitionQuerier, ref runtimeselection.PlatformReference, role string, requireCurrent bool) (runtimeplatform.Descriptor, error) {
	var empty runtimeplatform.Descriptor
	if ref.Validate() != nil || !runtimeplatform.ValidRole(role) {
		return empty, runtimeselection.ErrCorrupt
	}
	qualification, err := readPlatformQualification(ctx, q, ref.QualificationID, ref.QualificationGeneration)
	if err != nil {
		return empty, err
	}
	if qualification.State == runtimeplatform.StateInvalidated {
		return empty, runtimeplatform.ErrQualificationInvalidated
	}
	if requireCurrent && qualification.State != runtimeplatform.StateCurrent {
		return empty, runtimeplatform.ErrQualificationNotCurrent
	}
	if qualification.State != runtimeplatform.StateCurrent && qualification.State != runtimeplatform.StateSuperseded {
		return empty, runtimeselection.ErrCorrupt
	}
	d, err := readPlatformDescriptor(ctx, q, ref.DescriptorID)
	if err != nil {
		return empty, err
	}
	e, err := readPlatformEvidence(ctx, q, ref.CompatibilityEvidenceHash)
	if err != nil {
		return empty, err
	}
	payload := runtimeplatform.QualificationInput{QualificationID: qualification.QualificationID, PlatformLineageID: qualification.PlatformLineageID, DescriptorID: qualification.DescriptorID, CompatibilityPolicyVersion: qualification.CompatibilityPolicyVersion, CompatibilityEvidenceHash: qualification.CompatibilityEvidenceHash, Generation: qualification.Generation}
	if d.Validate() != nil || e.ValidateDescriptor(d) != nil || d.ComputeHash() != ref.DescriptorHash || d.RuntimeRole != role || d.PlatformLineageID != ref.PlatformLineageID || qualification.PlatformLineageID != ref.PlatformLineageID || qualification.DescriptorID != ref.DescriptorID || qualification.CompatibilityEvidenceHash != ref.CompatibilityEvidenceHash || qualification.CompatibilityPolicyVersion != ref.CompatibilityPolicyVersion || e.CompatibilityPolicyVersion != ref.CompatibilityPolicyVersion || qualification.Outcome != runtimeplatform.OutcomeQualified || qualification.IssuancePayloadHash != ref.QualificationPayloadHash || qualification.IssuancePayloadHash != payload.ComputePayloadHash() {
		return empty, runtimeselection.ErrCorrupt
	}
	return d, nil
}

func runtimeSelectionWriteError(err error) error {
	if err != nil && containsConstraint(err.Error()) {
		return fmt.Errorf("%w: %v", runtimeselection.ErrBindingConflict, err)
	}
	return err
}

func (s *RuntimeSelectionStore) ReadSource(ctx context.Context, sourceID string) (runtimeselection.ApprovedSource, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	defer tx.Rollback()
	return readRuntimeSelectionSourceTx(ctx, tx, sourceID)
}

func readRuntimeSelectionBindingTx(ctx context.Context, q definitionQuerier, query string, args ...any) (runtimeselection.Binding, error) {
	var raw, id, project, taskID, revision, role, sourceID, sourceHash, entry, request, payload, qid, schema, snapshot, created string
	var generation int
	err := q.QueryRowContext(ctx, `SELECT binding_json,binding_id,project_id,task_id,revision_id,runtime_role,source_id,source_payload_hash,entry,issuance_request_id,issuance_payload_hash,qualification_id,qualification_generation,snapshot_schema_id,authority_snapshot_hash,created_at FROM runtime_selection_bindings WHERE `+query, args...).Scan(&raw, &id, &project, &taskID, &revision, &role, &sourceID, &sourceHash, &entry, &request, &payload, &qid, &generation, &schema, &snapshot, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeselection.Binding{}, runtimeselection.ErrBindingMissing
	}
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	b, err := runtimeselection.DecodeBinding([]byte(raw))
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	if b.BindingID != id || b.Owner != (runtimeselection.Owner{ProjectID: project, TaskID: taskID, TaskDefinitionRevisionID: revision, RuntimeRole: role}) || b.SourceID != sourceID || b.SourcePayloadHash != sourceHash || string(b.Entry) != entry || b.IssuanceRequestID != request || b.IssuancePayloadHash != payload || b.Platform.QualificationID != qid || b.Platform.QualificationGeneration != generation || b.SnapshotSchemaID != schema || b.AuthoritySnapshotHash != snapshot || formatTime(b.CreatedAt) != created {
		return runtimeselection.Binding{}, runtimeselection.ErrCorrupt
	}
	source, err := readRuntimeSelectionSourceTx(ctx, q, b.SourceID)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	// Immutable descriptor read only. Revocation affects ReadCurrent, not an
	// exact original issuance receipt that was already observed as committed.
	d, err := readPlatformDescriptor(ctx, q, b.Platform.DescriptorID)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	if err = b.Validate(source, d); err != nil {
		return runtimeselection.Binding{}, err
	}
	return b, nil
}

func replayRuntimeSelectionBindingTx(ctx context.Context, q definitionQuerier, in runtimeselection.BindingInput) (runtimeselection.Binding, error) {
	if err := in.Validate(); err != nil {
		return runtimeselection.Binding{}, err
	}
	b, err := readRuntimeSelectionBindingTx(ctx, q, "issuance_request_id=?", in.RequestID)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	if b.Owner != in.Owner || b.SourceID != in.SourceID || b.SourcePayloadHash != in.SourcePayloadHash || b.Entry != in.Entry {
		return runtimeselection.Binding{}, runtimeselection.ErrBindingConflict
	}
	return b, nil
}

func (s *RuntimeSelectionStore) ReplayBinding(ctx context.Context, in runtimeselection.BindingInput) (runtimeselection.Binding, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	defer tx.Rollback()
	return replayRuntimeSelectionBindingTx(ctx, tx, in)
}

func (s *RuntimeSelectionStore) MaterializeBinding(ctx context.Context, in runtimeselection.BindingInput) (runtimeselection.Binding, error) {
	if err := in.Validate(); err != nil {
		return runtimeselection.Binding{}, err
	}
	// Exact replay precedes writer epoch, current owner, lifecycle and CAS gates.
	prior, err := s.ReplayBinding(ctx, in)
	if err == nil || !errors.Is(err, runtimeselection.ErrBindingMissing) {
		return prior, err
	}
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	defer tx.Rollback()
	prior, err = replayRuntimeSelectionBindingTx(ctx, tx, in)
	if err == nil || !errors.Is(err, runtimeselection.ErrBindingMissing) {
		return prior, err
	}
	source, err := readRuntimeSelectionSourceTx(ctx, tx, in.SourceID)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	if source.Owner != in.Owner || source.SourcePayloadHash != in.SourcePayloadHash || source.Entry != in.Entry {
		return runtimeselection.Binding{}, runtimeselection.ErrBindingConflict
	}
	if err = runtimeSelectionOwnerCurrentTx(ctx, tx, in.Owner); err != nil {
		return runtimeselection.Binding{}, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_selection_bindings WHERE project_id=? AND task_id=? AND revision_id=? AND runtime_role=?)`, in.Owner.ProjectID, in.Owner.TaskID, in.Owner.TaskDefinitionRevisionID, in.Owner.RuntimeRole).Scan(&exists); err != nil {
		return runtimeselection.Binding{}, err
	}
	if exists {
		return runtimeselection.Binding{}, runtimeselection.ErrBindingConflict
	}
	d, err := validateRuntimeSelectionPlatformTx(ctx, tx, source.Platform, in.Owner.RuntimeRole, true)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	lineage, err := readPlatformLineage(ctx, tx, source.Platform.PlatformLineageID)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	if lineage.Version != in.ExpectedLineageVersion {
		return runtimeselection.Binding{}, runtimeplatform.ErrSourceConflict
	}
	values, err := source.Values(d)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	b := runtimeselection.Binding{BindingID: "runtime_binding_" + runtimeselection.HashValue([]string{"CORE_TASK_RUNTIME_SELECTION_V1", in.RequestID}), Owner: source.Owner, Platform: source.Platform, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, IssuanceRequestID: in.RequestID, IssuancePayloadHash: source.BindingPayload(), SnapshotSchemaID: runtimeselection.SnapshotSchemaID, AuthoritySnapshotHash: runtimeselection.HashValue(values), Values: values, CreatedAt: time.Now().UTC()}
	if err = b.Validate(source, d); err != nil {
		return runtimeselection.Binding{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return runtimeselection.Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_selection_bindings(binding_id,project_id,task_id,revision_id,runtime_role,source_id,source_payload_hash,entry,issuance_request_id,issuance_payload_hash,qualification_id,qualification_generation,expected_lineage_version,snapshot_schema_id,authority_snapshot_hash,binding_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.BindingID, b.Owner.ProjectID, b.Owner.TaskID, b.Owner.TaskDefinitionRevisionID, b.Owner.RuntimeRole, b.SourceID, b.SourcePayloadHash, b.Entry, b.IssuanceRequestID, b.IssuancePayloadHash, b.Platform.QualificationID, b.Platform.QualificationGeneration, in.ExpectedLineageVersion, b.SnapshotSchemaID, b.AuthoritySnapshotHash, string(raw), formatTime(b.CreatedAt))
	if err != nil {
		return runtimeselection.Binding{}, runtimeSelectionWriteError(err)
	}
	if err = tx.Commit(); err != nil {
		return runtimeselection.Binding{}, runtimeSelectionWriteError(err)
	}
	return b, nil
}

func runtimeSelectionOwnerCurrentTx(ctx context.Context, q definitionQuerier, owner runtimeselection.Owner) error {
	if owner.Validate() != nil {
		return runtimeselection.ErrNotCurrentRevision
	}
	var current bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_definition_revisions r ON r.id=t.current_definition_revision_id AND r.task_id=t.id WHERE t.id=? AND t.project_id=? AND r.id=?)`, owner.TaskID, owner.ProjectID, owner.TaskDefinitionRevisionID).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return runtimeselection.ErrNotCurrentRevision
	}
	return nil
}

func readCurrentRuntimeSelectionTx(ctx context.Context, q definitionQuerier, owner runtimeselection.Owner) (runtimeselection.CurrentAuthority, error) {
	var empty runtimeselection.CurrentAuthority
	if err := runtimeSelectionOwnerCurrentTx(ctx, q, owner); err != nil {
		return empty, err
	}
	b, err := readRuntimeSelectionBindingTx(ctx, q, "project_id=? AND task_id=? AND revision_id=? AND runtime_role=?", owner.ProjectID, owner.TaskID, owner.TaskDefinitionRevisionID, owner.RuntimeRole)
	if err != nil {
		return empty, err
	}
	d, err := validateRuntimeSelectionPlatformTx(ctx, q, b.Platform, owner.RuntimeRole, false)
	if err != nil {
		return empty, err
	}
	source, err := readRuntimeSelectionSourceTx(ctx, q, b.SourceID)
	if err != nil {
		return empty, err
	}
	values, err := source.Values(d)
	if err != nil || !reflect.DeepEqual(values, b.Values) {
		return empty, runtimeselection.ErrCorrupt
	}
	return runtimeselection.CurrentAuthority{Owner: owner, BindingID: b.BindingID, Platform: b.Platform, Values: b.Values, AuthoritySnapshotHash: b.AuthoritySnapshotHash}, nil
}

func (s *RuntimeSelectionStore) ReadCurrent(ctx context.Context, owner runtimeselection.Owner) (runtimeselection.CurrentAuthority, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtimeselection.CurrentAuthority{}, err
	}
	defer tx.Rollback()
	return readCurrentRuntimeSelectionTx(ctx, tx, owner)
}

// ReadBindingLineageVersion supplies only a fresh CAS token for the lineage
// already fixed by the archived source. It cannot select a qualification or
// change any archived platform field; MaterializeBinding repeats eligibility.
func (s *RuntimeSelectionStore) ReadBindingLineageVersion(ctx context.Context, sourceID string) (int, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	source, err := readRuntimeSelectionSourceTx(ctx, tx, sourceID)
	if err != nil {
		return 0, err
	}
	lineage, err := readPlatformLineage(ctx, tx, source.Platform.PlatformLineageID)
	if err != nil {
		return 0, err
	}
	if lineage.RuntimeRole != source.Owner.RuntimeRole {
		return 0, runtimeselection.ErrCorrupt
	}
	return lineage.Version, nil
}
