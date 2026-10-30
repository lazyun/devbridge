package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

func readBackendMapping(ctx context.Context, q definitionQuerier, backend, fingerprint, role, descriptor string) (m runtimequalification.Mapping, err error) {
	var request string
	err = q.QueryRowContext(ctx, `SELECT h.request_id FROM backend_platform_mapping_heads h JOIN backend_platform_qualifications m ON m.request_id=h.request_id AND m.mapping_version=h.version WHERE h.backend_id=? AND h.backend_fingerprint=? AND h.runtime_role=? AND h.descriptor_id=?`, backend, fingerprint, role, descriptor).Scan(&request)
	if errors.Is(err, sql.ErrNoRows) {
		return m, runtimeplatform.ErrQualificationMissing
	}
	if err != nil {
		return m, err
	}
	m, err = readBackendMappingReceipt(ctx, q, request)
	if err == nil && (m.BackendID != backend || m.BackendFingerprint != fingerprint || m.RuntimeRole != role || m.Platform.DescriptorID != descriptor) {
		return m, runtimeplatform.ErrBindingCorrupt
	}
	return m, err
}

func readBackendMappingReceipt(ctx context.Context, q definitionQuerier, request string) (m runtimequalification.Mapping, err error) {
	var raw, hash, qualification, backend, fingerprint, role, descriptor string
	err = q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256,qualification_id,backend_id,backend_fingerprint,runtime_role,descriptor_id FROM backend_platform_qualifications WHERE request_id=?`, request).Scan(&raw, &hash, &qualification, &backend, &fingerprint, &role, &descriptor)
	if errors.Is(err, sql.ErrNoRows) {
		return m, runtimeplatform.ErrQualificationMissing
	}
	if err != nil {
		return m, err
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return m, runtimeplatform.ErrBindingCorrupt
	}
	encoded, _ := json.Marshal(m)
	if !bytes.Equal(encoded, []byte(raw)) || runtimeselection.HashValue(m) != hash || m.BackendID != backend || m.BackendFingerprint != fingerprint || m.RuntimeRole != role || m.Platform.DescriptorID != descriptor || m.RequestID != request || m.Platform.QualificationID != qualification || m.Platform.Validate() != nil {
		return m, runtimeplatform.ErrBindingCorrupt
	}
	return m, nil
}
func (s *RuntimePlatformStore) ReadBackendMapping(ctx context.Context, backend, fingerprint, role, descriptor string) (runtimequalification.Mapping, error) {
	return readBackendMapping(ctx, s.db.sql, backend, fingerprint, role, descriptor)
}
func (s *RuntimePlatformStore) ReadBackendMappingForRequest(ctx context.Context, request string) (runtimequalification.Mapping, error) {
	return readBackendMappingReceipt(ctx, s.db.sql, request)
}
func readQualificationRequest(ctx context.Context, q definitionQuerier, request string) (r runtimequalification.RequestIdentity, err error) {
	r.RequestID = request
	err = q.QueryRowContext(ctx, `SELECT backend_id,backend_fingerprint,runtime_role,descriptor_id,descriptor_hash FROM backend_platform_qualification_requests WHERE request_id=?`, request).Scan(&r.BackendID, &r.BackendFingerprint, &r.RuntimeRole, &r.DescriptorID, &r.DescriptorHash)
	if errors.Is(err, sql.ErrNoRows) {
		return r, runtimeplatform.ErrQualificationMissing
	}
	if err == nil && (r.RequestID == "" || r.BackendID == "" || r.DescriptorID == "" || !runtimeplatform.ValidDigest(r.BackendFingerprint) || !runtimeplatform.ValidDigest(r.DescriptorHash) || !runtimeplatform.ValidRole(r.RuntimeRole)) {
		return r, runtimeplatform.ErrBindingCorrupt
	}
	return r, err
}
func (s *RuntimePlatformStore) ReadQualificationRequest(ctx context.Context, request string) (runtimequalification.RequestIdentity, error) {
	return readQualificationRequest(ctx, s.db.sql, request)
}
func (s *RuntimePlatformStore) ReserveQualificationRequest(ctx context.Context, r runtimequalification.RequestIdentity) error {
	if r.RequestID == "" || r.BackendID == "" || r.DescriptorID == "" || !runtimeplatform.ValidDigest(r.BackendFingerprint) || !runtimeplatform.ValidDigest(r.DescriptorHash) || !runtimeplatform.ValidRole(r.RuntimeRole) {
		return runtimeplatform.ErrIdentityMismatch
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if old, e := readQualificationRequest(ctx, tx, r.RequestID); e == nil {
		if old != r {
			return runtimeplatform.ErrSourceConflict
		}
		return nil
	} else if !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		return e
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO backend_platform_qualification_requests(request_id,backend_id,backend_fingerprint,runtime_role,descriptor_id,descriptor_hash) VALUES(?,?,?,?,?,?)`, r.RequestID, r.BackendID, r.BackendFingerprint, r.RuntimeRole, r.DescriptorID, r.DescriptorHash)
	if err != nil {
		return platformWriteError(err)
	}
	return tx.Commit()
}
func readBackendPlatformReference(ctx context.Context, q definitionQuerier, backend codexconfig.BackendSelection, role, descriptorID string) (runtimeselection.PlatformReference, error) {
	m, err := readBackendMapping(ctx, q, backend.ConfigID, backend.ConfigFingerprint, role, descriptorID)
	if err != nil {
		return m.Platform, err
	}
	lineage, err := readPlatformLineage(ctx, q, m.Platform.PlatformLineageID)
	if err != nil {
		return m.Platform, err
	}
	if lineage.RuntimeRole != role {
		return m.Platform, runtimeplatform.ErrIdentityMismatch
	}
	m.Platform.LineageVersion = lineage.Version
	return m.Platform, err
}

func (s *RuntimePlatformStore) SaveBackendMapping(ctx context.Context, m runtimequalification.Mapping) error {
	if m.Platform.Validate() != nil || m.BackendID == "" || m.RequestID == "" || !runtimeplatform.ValidDigest(m.BackendFingerprint) || !runtimeplatform.ValidRole(m.RuntimeRole) {
		return runtimeplatform.ErrBindingCorrupt
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if old, e := readBackendMappingReceipt(ctx, tx, m.RequestID); e == nil {
		if old != m {
			return runtimeplatform.ErrSourceConflict
		}
		return nil
	} else if !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		return e
	}
	previousRequest, version := "", 1
	if old, e := readBackendMapping(ctx, tx, m.BackendID, m.BackendFingerprint, m.RuntimeRole, m.Platform.DescriptorID); e == nil {
		oldQualification, e := readPlatformQualification(ctx, tx, old.Platform.QualificationID, old.Platform.QualificationGeneration)
		if e != nil {
			return e
		}
		if oldQualification.State != runtimeplatform.StateSuperseded && oldQualification.State != runtimeplatform.StateInvalidated {
			return runtimeplatform.ErrSourceConflict
		}
		previousRequest = old.RequestID
		if e = tx.QueryRowContext(ctx, `SELECT version+1 FROM backend_platform_mapping_heads WHERE request_id=?`, previousRequest).Scan(&version); e != nil {
			return e
		}
	} else if !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		return e
	}
	q, err := readPlatformQualification(ctx, tx, m.Platform.QualificationID, m.Platform.QualificationGeneration)
	if err != nil {
		return err
	}
	d, err := readPlatformDescriptor(ctx, tx, m.Platform.DescriptorID)
	if err != nil {
		return err
	}
	e, err := readPlatformEvidence(ctx, tx, m.Platform.CompatibilityEvidenceHash)
	if err != nil {
		return err
	}
	l, err := readPlatformLineage(ctx, tx, m.Platform.PlatformLineageID)
	if err != nil {
		return err
	}
	if q.State != "CURRENT" || q.PlatformLineageID != m.Platform.PlatformLineageID || q.DescriptorID != d.DescriptorID || q.CompatibilityEvidenceHash != e.EvidenceHash || q.CompatibilityPolicyVersion != m.Platform.CompatibilityPolicyVersion || q.IssuancePayloadHash != m.Platform.QualificationPayloadHash || d.RuntimeRole != m.RuntimeRole || d.ComputeHash() != m.Platform.DescriptorHash || l.Version != m.Platform.LineageVersion || e.ValidateDescriptor(d) != nil {
		return runtimeplatform.ErrSourceConflict
	}
	var current codexconfig.Config
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT id,name,codex_path,enabled FROM codex_configs WHERE id=?`, m.BackendID).Scan(&current.ID, &current.Name, &current.CodexPath, &enabled)
	if err != nil {
		return err
	}
	if !enabled || current.Fingerprint() != m.BackendFingerprint {
		return runtimeplatform.ErrIdentityMismatch
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// Insertion and its trigger advance the explicit head atomically. Prior
	// receipts and all already-issued revision bindings remain untouched.
	_, err = tx.ExecContext(ctx, `INSERT INTO backend_platform_qualifications(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id,qualification_id,payload_json,payload_sha256,previous_request_id,mapping_version) VALUES(?,?,?,?,?,?,?,?,?,?)`, m.BackendID, m.BackendFingerprint, m.RuntimeRole, m.Platform.DescriptorID, m.RequestID, m.Platform.QualificationID, string(raw), runtimeselection.HashValue(m), previousRequest, version)
	if err != nil {
		return platformWriteError(err)
	}
	return tx.Commit()
}
func (s *RuntimePlatformStore) QualificationForRequest(ctx context.Context, request string) (runtimeplatform.Qualification, error) {
	var id string
	var generation int
	err := s.db.sql.QueryRowContext(ctx, `SELECT qualification_id,generation FROM runtime_platform_requests WHERE request_id=? AND operation IN ('QUALIFY','SUPERSEDE')`, request).Scan(&id, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeplatform.Qualification{}, runtimeplatform.ErrQualificationMissing
	}
	if err != nil {
		return runtimeplatform.Qualification{}, err
	}
	return s.GetQualification(ctx, id, generation)
}
func (s *RuntimePlatformStore) CurrentQualification(ctx context.Context, lineage string) (runtimeplatform.Qualification, error) {
	var id string
	var generation int
	err := s.db.sql.QueryRowContext(ctx, `SELECT qualification_id,generation FROM runtime_platform_qualifications WHERE platform_lineage_id=? AND state='CURRENT'`, lineage).Scan(&id, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeplatform.Qualification{}, runtimeplatform.ErrQualificationMissing
	}
	if err != nil {
		return runtimeplatform.Qualification{}, err
	}
	return s.GetQualification(ctx, id, generation)
}
