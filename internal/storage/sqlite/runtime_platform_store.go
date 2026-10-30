package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

// RuntimePlatformStore persists trusted Core facts. It has no transport or
// runtime-adapter entry point and does not create facts from configuration,
// callbacks, environment variables, or observed Runs.
type RuntimePlatformStore struct{ db *DB }

func NewRuntimePlatformStore(db *DB) *RuntimePlatformStore { return &RuntimePlatformStore{db: db} }

var _ runtimeplatform.Store = (*RuntimePlatformStore)(nil)

// Deferred foreign-key failures can leave SQLite's transaction open even after
// database/sql marks its Tx done. Pin the connection until commit cleanup has
// finished so a failed platform write can never leak into another operation.
type runtimePlatformTx struct {
	*sql.Tx
	conn   *sql.Conn
	closed bool
}

func (d *DB) beginRuntimePlatformWrite(ctx context.Context) (*runtimePlatformTx, error) {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	out := &runtimePlatformTx{Tx: tx, conn: conn}
	if err = d.checkWriterTx(ctx, tx); err == nil {
		if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.RunID != "" {
			err = d.checkRunTx(ctx, tx, fence.RunID)
		}
	}
	if err != nil {
		_ = out.Rollback()
		return nil, err
	}
	return out, nil
}

func (tx *runtimePlatformTx) close() error {
	if tx.closed {
		return nil
	}
	tx.closed = true
	return tx.conn.Close()
}

func (tx *runtimePlatformTx) rollbackPinned() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := tx.conn.ExecContext(ctx, "ROLLBACK")
	if err != nil {
		// A failed cleanup connection must never return to the pool, including
		// when cancellation or an I/O failure obscures its transaction state.
		_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err
}

func (tx *runtimePlatformTx) Commit() error {
	if tx.closed {
		return sql.ErrTxDone
	}
	err := tx.Tx.Commit()
	if err != nil {
		return errors.Join(err, tx.rollbackPinned(), tx.close())
	}
	return tx.close()
}

func (tx *runtimePlatformTx) Rollback() error {
	if tx.closed {
		return sql.ErrTxDone
	}
	err := tx.Tx.Rollback()
	if err != nil {
		return errors.Join(err, tx.rollbackPinned(), tx.close())
	}
	return tx.close()
}

const platformDescriptorSelect = `SELECT descriptor_id,platform_lineage_id,runtime_role,adapter_id,adapter_implementation_digest,protocol_id,protocol_schema_digest,capability_digest,descriptor_version FROM runtime_platform_descriptors WHERE descriptor_id=?`
const platformEvidenceSelect = `SELECT evidence_hash,descriptor_id,platform_lineage_id,runtime_role,adapter_implementation_digest,protocol_schema_digest,compatibility_policy_version,measured_compatibility,collector_identity,collection_artifact_hash,source FROM runtime_platform_compatibility_evidence WHERE evidence_hash=?`
const platformQualificationSelect = `SELECT qualification_id,platform_lineage_id,generation,state,descriptor_id,compatibility_policy_version,compatibility_evidence_hash,outcome,issuance_request_id,issuance_payload_hash,created_at,superseded_at,invalidated_at,invalidation_reason,lineage_version,COALESCE(invalidation_request_id,''),invalidation_payload_hash FROM runtime_platform_qualifications WHERE qualification_id=? AND generation=?`

func readPlatformDescriptor(ctx context.Context, q definitionQuerier, id string) (d runtimeplatform.Descriptor, err error) {
	err = q.QueryRowContext(ctx, platformDescriptorSelect, id).Scan(&d.DescriptorID, &d.PlatformLineageID, &d.RuntimeRole, &d.AdapterID, &d.AdapterImplementationDigest, &d.ProtocolID, &d.ProtocolSchemaDigest, &d.CapabilityDigest, &d.DescriptorVersion)
	if errors.Is(err, sql.ErrNoRows) {
		err = runtimeplatform.ErrDescriptorMissing
	}
	return
}
func readPlatformEvidence(ctx context.Context, q definitionQuerier, hash string) (e runtimeplatform.CompatibilityEvidence, err error) {
	err = q.QueryRowContext(ctx, platformEvidenceSelect, hash).Scan(&e.EvidenceHash, &e.DescriptorID, &e.PlatformLineageID, &e.RuntimeRole, &e.AdapterImplementationDigest, &e.ProtocolSchemaDigest, &e.CompatibilityPolicyVersion, &e.MeasuredCompatibility, &e.CollectorIdentity, &e.CollectionArtifactHash, &e.Source)
	if errors.Is(err, sql.ErrNoRows) {
		err = runtimeplatform.ErrBindingCorrupt
	}
	return
}
func readPlatformQualification(ctx context.Context, q definitionQuerier, id string, generation int) (v runtimeplatform.Qualification, err error) {
	var created, superseded, invalidated string
	err = q.QueryRowContext(ctx, platformQualificationSelect, id, generation).Scan(&v.QualificationID, &v.PlatformLineageID, &v.Generation, &v.State, &v.DescriptorID, &v.CompatibilityPolicyVersion, &v.CompatibilityEvidenceHash, &v.Outcome, &v.IssuanceRequestID, &v.IssuancePayloadHash, &created, &superseded, &invalidated, &v.InvalidationReason, &v.LineageVersion, &v.InvalidationRequestID, &v.InvalidationPayloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return v, runtimeplatform.ErrQualificationMissing
	}
	if err != nil {
		return v, err
	}
	if v.CreatedAt, err = parseTime(created); err != nil {
		return v, err
	}
	if superseded != "" {
		if v.SupersededAt, err = parseTime(superseded); err != nil {
			return v, err
		}
	}
	if invalidated != "" {
		v.InvalidatedAt, err = parseTime(invalidated)
	}
	return
}
func readPlatformLineage(ctx context.Context, q definitionQuerier, id string) (v runtimeplatform.Lineage, err error) {
	err = q.QueryRowContext(ctx, `SELECT platform_lineage_id,runtime_role,high_water,version FROM runtime_platform_lineages WHERE platform_lineage_id=?`, id).Scan(&v.PlatformLineageID, &v.RuntimeRole, &v.HighWater, &v.Version)
	if errors.Is(err, sql.ErrNoRows) {
		err = runtimeplatform.ErrQualificationMissing
	}
	return
}
func (s *RuntimePlatformStore) GetDescriptor(ctx context.Context, id string) (runtimeplatform.Descriptor, error) {
	return readPlatformDescriptor(ctx, s.db.sql, id)
}
func (s *RuntimePlatformStore) GetEvidence(ctx context.Context, hash string) (runtimeplatform.CompatibilityEvidence, error) {
	return readPlatformEvidence(ctx, s.db.sql, hash)
}
func (s *RuntimePlatformStore) GetQualification(ctx context.Context, id string, generation int) (runtimeplatform.Qualification, error) {
	return readPlatformQualification(ctx, s.db.sql, id, generation)
}
func (s *RuntimePlatformStore) GetLineage(ctx context.Context, id string) (runtimeplatform.Lineage, error) {
	return readPlatformLineage(ctx, s.db.sql, id)
}

func platformWriteError(err error) error {
	if err != nil && containsConstraint(err.Error()) {
		return fmt.Errorf("%w: %v", runtimeplatform.ErrSourceConflict, err)
	}
	return err
}

func (s *RuntimePlatformStore) RegisterDescriptor(ctx context.Context, d runtimeplatform.Descriptor) (runtimeplatform.Descriptor, error) {
	if err := d.Validate(); err != nil {
		return runtimeplatform.Descriptor{}, err
	}
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return runtimeplatform.Descriptor{}, err
	}
	defer tx.Rollback()
	old, err := readPlatformDescriptor(ctx, tx, d.DescriptorID)
	if err == nil {
		if old != d {
			return runtimeplatform.Descriptor{}, runtimeplatform.ErrSourceConflict
		}
		return old, nil
	}
	if !errors.Is(err, runtimeplatform.ErrDescriptorMissing) {
		return runtimeplatform.Descriptor{}, err
	}
	lineage, err := readPlatformLineage(ctx, tx, d.PlatformLineageID)
	if errors.Is(err, runtimeplatform.ErrQualificationMissing) {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_lineages(platform_lineage_id,runtime_role) VALUES(?,?)`, d.PlatformLineageID, d.RuntimeRole)
	} else if err == nil && lineage.RuntimeRole != d.RuntimeRole {
		return runtimeplatform.Descriptor{}, runtimeplatform.ErrIdentityMismatch
	}
	if err != nil {
		return runtimeplatform.Descriptor{}, platformWriteError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_descriptors(descriptor_id,platform_lineage_id,runtime_role,adapter_id,adapter_implementation_digest,protocol_id,protocol_schema_digest,capability_digest,descriptor_version) VALUES(?,?,?,?,?,?,?,?,?)`, d.DescriptorID, d.PlatformLineageID, d.RuntimeRole, d.AdapterID, d.AdapterImplementationDigest, d.ProtocolID, d.ProtocolSchemaDigest, d.CapabilityDigest, d.DescriptorVersion)
	if err != nil {
		return runtimeplatform.Descriptor{}, platformWriteError(err)
	}
	if err = tx.Commit(); err != nil {
		return runtimeplatform.Descriptor{}, platformWriteError(err)
	}
	return d, nil
}

func (s *RuntimePlatformStore) MaterializeCompatibilityEvidence(ctx context.Context, e runtimeplatform.CompatibilityEvidence) (runtimeplatform.CompatibilityEvidence, error) {
	if err := e.Validate(); err != nil {
		return runtimeplatform.CompatibilityEvidence{}, err
	}
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return runtimeplatform.CompatibilityEvidence{}, err
	}
	defer tx.Rollback()
	d, err := readPlatformDescriptor(ctx, tx, e.DescriptorID)
	if err != nil {
		return runtimeplatform.CompatibilityEvidence{}, err
	}
	if err = e.ValidateDescriptor(d); err != nil {
		return runtimeplatform.CompatibilityEvidence{}, err
	}
	old, err := readPlatformEvidence(ctx, tx, e.EvidenceHash)
	if err == nil {
		if old != e {
			return runtimeplatform.CompatibilityEvidence{}, runtimeplatform.ErrSourceConflict
		}
		return old, nil
	}
	if !errors.Is(err, runtimeplatform.ErrBindingCorrupt) {
		return runtimeplatform.CompatibilityEvidence{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_compatibility_evidence(evidence_hash,descriptor_id,platform_lineage_id,runtime_role,adapter_implementation_digest,protocol_schema_digest,compatibility_policy_version,measured_compatibility,collector_identity,collection_artifact_hash,source) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, e.EvidenceHash, e.DescriptorID, e.PlatformLineageID, e.RuntimeRole, e.AdapterImplementationDigest, e.ProtocolSchemaDigest, e.CompatibilityPolicyVersion, boolInt(e.MeasuredCompatibility), e.CollectorIdentity, e.CollectionArtifactHash, e.Source)
	if err != nil {
		return runtimeplatform.CompatibilityEvidence{}, platformWriteError(err)
	}
	if err = tx.Commit(); err != nil {
		return runtimeplatform.CompatibilityEvidence{}, platformWriteError(err)
	}
	return e, nil
}

// A single request namespace prevents an issuance request from being reused to
// invalidate another generation. Replay never runs live source or CAS checks.
func platformRequestReplay(ctx context.Context, tx definitionQuerier, request, operation, payload string) (runtimeplatform.Qualification, bool, error) {
	var op, hash, id, lineage string
	var generation int
	err := tx.QueryRowContext(ctx, `SELECT operation,payload_hash,qualification_id,platform_lineage_id,generation FROM runtime_platform_requests WHERE request_id=?`, request).Scan(&op, &hash, &id, &lineage, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeplatform.Qualification{}, false, nil
	}
	if err != nil {
		return runtimeplatform.Qualification{}, false, err
	}
	if op != operation || hash != payload {
		return runtimeplatform.Qualification{}, false, runtimeplatform.ErrSourceConflict
	}
	v, err := readPlatformQualification(ctx, tx, id, generation)
	if err != nil {
		return runtimeplatform.Qualification{}, false, err
	}
	if v.PlatformLineageID != lineage {
		return runtimeplatform.Qualification{}, false, runtimeplatform.ErrSourceConflict
	}
	if operation == "INVALIDATE" {
		identity := runtimeplatform.InvalidationInput{QualificationID: v.QualificationID, Generation: v.Generation, Reason: v.InvalidationReason}
		if v.State != runtimeplatform.StateInvalidated || v.InvalidationRequestID != request || v.InvalidationPayloadHash != hash || identity.ComputePayloadHash() != hash {
			return runtimeplatform.Qualification{}, false, runtimeplatform.ErrSourceConflict
		}
	} else {
		identity := runtimeplatform.QualificationInput{QualificationID: v.QualificationID, PlatformLineageID: v.PlatformLineageID, Generation: v.Generation, DescriptorID: v.DescriptorID, CompatibilityPolicyVersion: v.CompatibilityPolicyVersion, CompatibilityEvidenceHash: v.CompatibilityEvidenceHash}
		if v.IssuanceRequestID != request || v.IssuancePayloadHash != hash || v.Outcome != runtimeplatform.OutcomeQualified || identity.ComputePayloadHash() != hash {
			return runtimeplatform.Qualification{}, false, runtimeplatform.ErrSourceConflict
		}
	}
	return v, true, nil
}

func (s *RuntimePlatformStore) Qualify(ctx context.Context, in runtimeplatform.QualificationInput) (runtimeplatform.Qualification, error) {
	return s.issue(ctx, in, false)
}
func (s *RuntimePlatformStore) Supersede(ctx context.Context, in runtimeplatform.QualificationInput) (runtimeplatform.Qualification, error) {
	return s.issue(ctx, in, true)
}
func (s *RuntimePlatformStore) issue(ctx context.Context, in runtimeplatform.QualificationInput, supersede bool) (runtimeplatform.Qualification, error) {
	var zero runtimeplatform.Qualification
	if err := in.Validate(); err != nil {
		return zero, err
	}
	operation := "QUALIFY"
	if supersede {
		operation = "SUPERSEDE"
	}
	payload := in.ComputePayloadHash()
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if old, found, err := platformRequestReplay(ctx, tx, in.IssuanceRequestID, operation, payload); err != nil || found {
		return old, err
	}
	d, err := readPlatformDescriptor(ctx, tx, in.DescriptorID)
	if err != nil {
		return zero, err
	}
	e, err := readPlatformEvidence(ctx, tx, in.CompatibilityEvidenceHash)
	if err != nil {
		return zero, err
	}
	if err = e.ValidateDescriptor(d); err != nil {
		return zero, err
	}
	if d.PlatformLineageID != in.PlatformLineageID || e.CompatibilityPolicyVersion != in.CompatibilityPolicyVersion {
		return zero, runtimeplatform.ErrBindingCorrupt
	}
	l, err := readPlatformLineage(ctx, tx, in.PlatformLineageID)
	if err != nil {
		return zero, err
	}
	if l.Version != in.ExpectedLineageVersion || in.Generation != l.HighWater+1 {
		return zero, runtimeplatform.ErrSourceConflict
	}
	var currentID string
	var currentGeneration int
	err = tx.QueryRowContext(ctx, `SELECT qualification_id,generation FROM runtime_platform_qualifications WHERE platform_lineage_id=? AND state='CURRENT'`, in.PlatformLineageID).Scan(&currentID, &currentGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if supersede {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, runtimeplatform.ErrQualificationMissing
		}
		if currentID != in.ExpectedQualificationID || currentGeneration != in.ExpectedGeneration {
			return zero, runtimeplatform.ErrSourceConflict
		}
	} else if err == nil || in.ExpectedQualificationID != "" || in.ExpectedGeneration != 0 {
		return zero, runtimeplatform.ErrSourceConflict
	}
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_requests(request_id,operation,payload_hash,qualification_id,platform_lineage_id,generation,expected_lineage_version,predecessor_qualification_id,predecessor_generation,created_at,issuance_binding) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, in.IssuanceRequestID, operation, payload, in.QualificationID, in.PlatformLineageID, in.Generation, l.Version, in.ExpectedQualificationID, in.ExpectedGeneration, now, in.IssuanceRequestID)
	if err != nil {
		return zero, platformWriteError(err)
	}
	if supersede {
		result, e := tx.ExecContext(ctx, `UPDATE runtime_platform_qualifications SET state='SUPERSEDED',superseded_at=?,lineage_version=? WHERE qualification_id=? AND generation=? AND state='CURRENT'`, now, l.Version+1, currentID, currentGeneration)
		if e != nil {
			return zero, platformWriteError(e)
		}
		if count, e := result.RowsAffected(); e != nil || count != 1 {
			return zero, runtimeplatform.ErrSourceConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_qualifications(qualification_id,platform_lineage_id,generation,state,descriptor_id,compatibility_policy_version,compatibility_evidence_hash,outcome,issuance_request_id,issuance_payload_hash,created_at,lineage_version) VALUES(?,?,?,'CURRENT',?,?,?,'QUALIFIED',?,?,?,?)`, in.QualificationID, in.PlatformLineageID, in.Generation, in.DescriptorID, in.CompatibilityPolicyVersion, in.CompatibilityEvidenceHash, in.IssuanceRequestID, payload, now, l.Version+1)
	if err != nil {
		return zero, platformWriteError(err)
	}
	out, err := readPlatformQualification(ctx, tx, in.QualificationID, in.Generation)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, platformWriteError(err)
	}
	return out, nil
}

func (s *RuntimePlatformStore) Invalidate(ctx context.Context, in runtimeplatform.InvalidationInput) (runtimeplatform.Qualification, error) {
	var zero runtimeplatform.Qualification
	if err := in.Validate(); err != nil {
		return zero, err
	}
	payload := in.ComputePayloadHash()
	tx, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if old, found, err := platformRequestReplay(ctx, tx, in.RequestID, "INVALIDATE", payload); err != nil || found {
		return old, err
	}
	old, err := readPlatformQualification(ctx, tx, in.QualificationID, in.Generation)
	if err != nil {
		return zero, err
	}
	if old.State != runtimeplatform.StateCurrent && old.State != runtimeplatform.StateSuperseded {
		return zero, runtimeplatform.ErrSourceConflict
	}
	l, err := readPlatformLineage(ctx, tx, old.PlatformLineageID)
	if err != nil {
		return zero, err
	}
	if l.Version != in.ExpectedLineageVersion {
		return zero, runtimeplatform.ErrSourceConflict
	}
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_platform_requests(request_id,operation,payload_hash,qualification_id,platform_lineage_id,generation,expected_lineage_version,created_at,invalidation_binding) VALUES(?,'INVALIDATE',?,?,?,?,?,?,?)`, in.RequestID, payload, in.QualificationID, old.PlatformLineageID, in.Generation, l.Version, now, in.RequestID)
	if err != nil {
		return zero, platformWriteError(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_platform_qualifications SET state='INVALIDATED',invalidated_at=?,invalidation_reason=?,invalidation_request_id=?,invalidation_payload_hash=?,lineage_version=? WHERE qualification_id=? AND generation=? AND state=?`, now, in.Reason, in.RequestID, payload, l.Version+1, in.QualificationID, in.Generation, old.State)
	if err != nil {
		return zero, platformWriteError(err)
	}
	if count, e := result.RowsAffected(); e != nil || count != 1 {
		return zero, runtimeplatform.ErrSourceConflict
	}
	out, err := readPlatformQualification(ctx, tx, in.QualificationID, in.Generation)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, platformWriteError(err)
	}
	return out, nil
}
