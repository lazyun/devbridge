package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

// Only a successful formal receipt reader may call this projection. Its input
// is not itself an issuance port, and no caller-supplied source is persisted.
func runtimeSelectionSourceFromReceipt(r formalexecution.Receipt) (runtimeselection.ApprovedSource, error) {
	if err := r.Validate(); err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	a, id := r.Archive, r.Archive.Identity
	if r.ProducerKind != formalexecution.ProducerKind || r.TransitionID != formalexecution.TransitionID || a.Commit.Action != "APPROVE_AND_START" {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrCorrupt
	}
	resultHash := formalexecution.Hash(r.T1)
	// These independent archived facts define the context domain. Mutable Task,
	// qualification state, configuration inventory and produced source are absent.
	contextHash := runtimeselection.HashValue(map[string]any{"domain": "devbridge.runtime-formal-producer-context/v1", "intentKey": []any{id.AISessionID, id.IntentID, id.IntentVersion}, "intentHash": id.ContentHash, "commitKey": []any{id.AISessionID, id.RequestID, id.IntentID, id.IntentVersion}, "receiptKey": r.ReceiptIdentity, "payloadHash": id.TypedPayloadHash, "submittedHash": id.SubmittedHash, "purpose": a.Commit.Purpose, "action": a.Commit.Action, "variantId": a.Commit.VariantID, "variantHash": r.T1.Identity.VariantPayloadIdentity, "selectionHash": runtimeselection.HashValue(a.Commit.Selection), "backendHash": runtimeselection.HashValue(a.Backend), "resultKey": r.T1.Identity.BusinessResultID, "resultHash": resultHash, "revisionKey": []string{a.Fence.ProjectID, a.Fence.TaskID, a.RevisionID}, "revisionHash": a.RevisionHash, "platformHash": runtimeselection.HashValue(a.PlatformReference), "producerKind": r.ProducerKind, "entry": runtimeselection.EntryRuntimeAuthorized, "t2Hash": formalexecution.Hash(r.T2), "planHash": r.Plan.ContentHash, "preparationAuthorityId": r.PreparationAuthorityID})
	s := runtimeselection.ApprovedSource{SourceID: r.T1.Identity.SourceOriginID, Owner: runtimeselection.Owner{ProjectID: a.Fence.ProjectID, TaskID: a.Fence.TaskID, TaskDefinitionRevisionID: a.RevisionID, RuntimeRole: "IMPLEMENTATION"}, Platform: runtimeselection.PlatformReference(a.PlatformReference), Backend: runtimeselection.BackendConfiguration{Identity: a.Backend.ConfigID, Fingerprint: a.Backend.ConfigFingerprint, Name: a.Backend.ConfigName, Source: a.Backend.Source, Kind: "CODEX", PlatformLineageID: a.PlatformReference.PlatformLineageID}, Runtime: a.Runtime, Current: true, Entry: runtimeselection.EntryRuntimeAuthorized, SourceReceiptIdentity: r.ReceiptIdentity, SourceReceiptHash: formalexecution.Hash(r), SourceReceiptPayloadHash: id.TypedPayloadHash, SourceReceiptSubmittedHash: id.SubmittedHash, SourceFormalContextDigest: contextHash, AISessionID: id.AISessionID, RequestID: id.RequestID, IntentID: id.IntentID, IntentVersion: id.IntentVersion, ContentHash: id.ContentHash, ResultID: r.T1.Identity.BusinessResultID, ResultHash: resultHash, RevisionHash: a.RevisionHash, ProducerKind: r.ProducerKind, OriginalPurpose: string(a.Commit.Purpose), OriginalAction: a.Commit.Action, VariantID: a.Commit.VariantID, NormalizedIssuanceClass: "RUNTIME_AUTHORIZED_REVIEW_APPROVAL"}
	s.SourcePayloadHash = s.ComputeHash()
	if err := s.Validate(); err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	values, err := s.Values(a.Descriptor)
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	p := r.Plan
	planValues := runtimeselection.AuthorityValues{BackendIdentity: p.BackendIdentity, BackendConfigFingerprint: p.BackendConfigFingerprint, BackendSource: p.BackendSource, BackendKind: p.BackendKind, Model: p.Model, ReasoningEffort: p.ReasoningEffort, CodexFastMode: p.CodexFastMode, ServiceTier: p.ServiceTier, AgentID: p.AgentID, AgentConfigFingerprint: p.AgentConfigFingerprint, ExecutionPolicy: p.ExecutionPolicy, RuntimeAdapterIdentity: p.RuntimeAdapterIdentity, RuntimeProtocolIdentity: p.RuntimeProtocolIdentity}
	if values != planValues || p.AuthoritySnapshotHash != runtimeselection.HashValue(values) {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrCorrupt
	}
	return s, nil
}

func readRuntimeSelectionSourceTx(ctx context.Context, q definitionQuerier, sourceID string) (runtimeselection.ApprovedSource, error) {
	var raw, project, taskID, revision, role, entry, session, request, receipt, receiptHash, qid, lineage, descriptor, sourceHash string
	var generation int
	err := q.QueryRowContext(ctx, `SELECT source_json,project_id,task_id,revision_id,runtime_role,entry,ai_session_id,request_id,receipt_identity,receipt_sha256,qualification_id,qualification_generation,platform_lineage_id,descriptor_id,source_payload_hash FROM runtime_selection_sources WHERE source_id=?`, sourceID).Scan(&raw, &project, &taskID, &revision, &role, &entry, &session, &request, &receipt, &receiptHash, &qid, &generation, &lineage, &descriptor, &sourceHash)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrSourceMissing
	}
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	s, err := runtimeselection.DecodeSource([]byte(raw))
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	if s.SourceID != sourceID || s.Owner != (runtimeselection.Owner{ProjectID: project, TaskID: taskID, TaskDefinitionRevisionID: revision, RuntimeRole: role}) || string(s.Entry) != entry || s.AISessionID != session || s.RequestID != request || s.SourceReceiptIdentity != receipt || s.SourceReceiptHash != receiptHash || s.Platform.QualificationID != qid || s.Platform.QualificationGeneration != generation || s.Platform.PlatformLineageID != lineage || s.Platform.DescriptorID != descriptor || s.SourcePayloadHash != sourceHash {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrCorrupt
	}
	id := formalexecution.ReadIdentity{AISessionID: s.AISessionID, IntentID: s.IntentID, RequestID: s.RequestID, IntentVersion: s.IntentVersion, ContentHash: s.ContentHash, TypedPayloadHash: s.SourceReceiptPayloadHash, SubmittedHash: s.SourceReceiptSubmittedHash}
	var want runtimeselection.ApprovedSource
	switch s.OriginalAction {
	case "APPROVE_AND_START":
		r, e := readFormalExecutionReceiptTx(ctx, q, id)
		if e != nil {
			return s, e
		}
		want, err = runtimeSelectionSourceFromReceipt(r)
	case "START_APPROVED":
		r, e := readFormalAdoptionReceiptTx(ctx, q, id)
		if e != nil {
			return s, e
		}
		want, err = runtimeSelectionSourceFromAdoption(r)
	default:
		return s, runtimeselection.ErrCorrupt
	}
	if err != nil {
		return s, err
	}
	if !reflect.DeepEqual(s, want) {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrCorrupt
	}
	return s, nil
}

// This helper is private to the final producer transaction. It re-reads the
// exact newly written formal archive through its full independent validator.
// It writes no binding, Run, Task phase, compatibility fact or qualification.
func persistRuntimeSelectionSourceTx(ctx context.Context, tx *sql.Tx, id formalexecution.ReadIdentity) (runtimeselection.ApprovedSource, error) {
	r, err := readFormalExecutionReceiptTx(ctx, tx, id)
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	s, err := runtimeSelectionSourceFromReceipt(r)
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	return persistVerifiedRuntimeSelectionSourceTx(ctx, tx, s)
}

func persistRuntimeSelectionAdoptionSourceTx(ctx context.Context, tx *sql.Tx, id formalexecution.ReadIdentity) (runtimeselection.ApprovedSource, error) {
	r, err := readFormalAdoptionReceiptTx(ctx, tx, id)
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	s, err := runtimeSelectionSourceFromAdoption(r)
	if err != nil {
		return s, err
	}
	return persistVerifiedRuntimeSelectionSourceTx(ctx, tx, s)
}

// Only the exact private formal archive readers above call this transaction helper.
func persistVerifiedRuntimeSelectionSourceTx(ctx context.Context, tx *sql.Tx, s runtimeselection.ApprovedSource) (runtimeselection.ApprovedSource, error) {
	prior, err := readRuntimeSelectionSourceTx(ctx, tx, s.SourceID)
	if err == nil {
		if !reflect.DeepEqual(prior, s) {
			return runtimeselection.ApprovedSource{}, runtimeselection.ErrBindingConflict
		}
		return prior, nil
	}
	if !errors.Is(err, runtimeselection.ErrSourceMissing) {
		return runtimeselection.ApprovedSource{}, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return runtimeselection.ApprovedSource{}, err
	}
	var approval, adoption any
	if s.OriginalAction == "APPROVE_AND_START" {
		approval = s.RequestID
	} else if s.OriginalAction == "START_APPROVED" {
		adoption = s.RequestID
	} else {
		return s, runtimeselection.ErrCorrupt
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_selection_sources(source_id,project_id,task_id,revision_id,runtime_role,entry,ai_session_id,request_id,receipt_identity,receipt_sha256,qualification_id,qualification_generation,platform_lineage_id,descriptor_id,source_payload_hash,source_json,approval_request_id,adoption_request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.SourceID, s.Owner.ProjectID, s.Owner.TaskID, s.Owner.TaskDefinitionRevisionID, s.Owner.RuntimeRole, s.Entry, s.AISessionID, s.RequestID, s.SourceReceiptIdentity, s.SourceReceiptHash, s.Platform.QualificationID, s.Platform.QualificationGeneration, s.Platform.PlatformLineageID, s.Platform.DescriptorID, s.SourcePayloadHash, string(raw), approval, adoption)
	if err != nil {
		return runtimeselection.ApprovedSource{}, runtimeSelectionWriteError(err)
	}
	return s, nil
}

func runtimeSelectionSourceFromAdoption(r formalexecution.AdoptionReceipt) (runtimeselection.ApprovedSource, error) {
	if r.Validate() != nil {
		return runtimeselection.ApprovedSource{}, runtimeselection.ErrCorrupt
	}
	a, id := r.Archive, r.Archive.Identity
	var prior any = a.PriorApproval
	priorIdentity := a.PriorApproval.ReceiptIdentity
	entry, issuanceClass := runtimeselection.EntryDefinitionOnlyAdoption, "DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION"
	if a.PriorRuntimeApproval != nil {
		prior, priorIdentity = *a.PriorRuntimeApproval, a.PriorRuntimeApproval.ReceiptIdentity
		entry, issuanceClass = runtimeselection.EntryRuntimeAuthorized, "RUNTIME_AUTHORIZED_EXECUTION_CONFIRMATION"
	}
	contextHash := runtimeselection.HashValue(map[string]any{"domain": "devbridge.runtime-formal-adoption-context/v1", "identity": id, "commit": a.Commit, "priorApprovalHash": formalexecution.Hash(prior), "priorApprovalRole": a.PriorApprovalRole, "frozenLineage": a.FrozenLineage, "resultLineage": a.ResultLineage, "platform": a.PlatformReference, "planHash": a.Plan.ContentHash, "businessHash": r.BusinessHash, "preparationAuthorityId": a.PreparationAuthorityID})
	s := runtimeselection.ApprovedSource{SourceID: r.SourceID(), Owner: runtimeselection.Owner{ProjectID: a.ProjectID, TaskID: a.TaskID, TaskDefinitionRevisionID: a.RevisionID, RuntimeRole: "IMPLEMENTATION"}, Platform: a.PlatformReference, Backend: runtimeselection.BackendConfiguration{Identity: a.Backend.ConfigID, Fingerprint: a.Backend.ConfigFingerprint, Name: a.Backend.ConfigName, Source: a.Backend.Source, Kind: "CODEX", PlatformLineageID: a.PlatformReference.PlatformLineageID}, Runtime: a.Runtime, Current: true, Entry: entry, SourceReceiptIdentity: r.ReceiptIdentity, SourceReceiptHash: formalexecution.Hash(r), SourceReceiptPayloadHash: id.TypedPayloadHash, SourceReceiptSubmittedHash: id.SubmittedHash, SourceFormalContextDigest: contextHash, AISessionID: id.AISessionID, RequestID: id.RequestID, IntentID: id.IntentID, IntentVersion: id.IntentVersion, ContentHash: id.ContentHash, ResultID: a.ExecutionID, ResultHash: r.BusinessHash, RevisionHash: a.RevisionHash, ProducerKind: r.ProducerKind, OriginalPurpose: string(a.Commit.Purpose), OriginalAction: a.Commit.Action, VariantID: a.Commit.VariantID, NormalizedIssuanceClass: issuanceClass, PriorApprovalReceiptIdentity: priorIdentity, PriorApprovalProofHash: runtimeselection.HashValue(map[string]any{"role": a.PriorApprovalRole, "receipt": prior})}
	s.SourcePayloadHash = s.ComputeHash()
	if err := s.Validate(); err != nil {
		return s, err
	}
	values, err := s.Values(a.Descriptor)
	if err != nil {
		return s, err
	}
	p := a.Plan
	planValues := runtimeselection.AuthorityValues{BackendIdentity: p.BackendIdentity, BackendConfigFingerprint: p.BackendConfigFingerprint, BackendSource: p.BackendSource, BackendKind: p.BackendKind, Model: p.Model, ReasoningEffort: p.ReasoningEffort, CodexFastMode: p.CodexFastMode, ServiceTier: p.ServiceTier, AgentID: p.AgentID, AgentConfigFingerprint: p.AgentConfigFingerprint, ExecutionPolicy: p.ExecutionPolicy, RuntimeAdapterIdentity: p.RuntimeAdapterIdentity, RuntimeProtocolIdentity: p.RuntimeProtocolIdentity}
	if values != planValues || p.AuthoritySnapshotHash != runtimeselection.HashValue(values) {
		return s, runtimeselection.ErrCorrupt
	}
	return s, nil
}
