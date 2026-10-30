package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/probeexecution"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

// resolveCompleteTx is intentionally separate from ordinary Run.Update: an
// engineering recovery receipt cannot satisfy requireResolvedResponsesTx.
func (s *RecoveryStatusStore) resolveCompleteTx(ctx context.Context, tx *sql.Tx, v *rs.Record, source run.Run) error {
	var facts rs.FactualObservation
	if err := json.Unmarshal(v.Facts, &facts); err != nil {
		return err
	}
	if !facts.OwnerContractValidated || facts.OwnerContractEvidenceHash == "" || len(facts.OwnerResult) == 0 {
		return rs.ErrIneligible
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		return s.resolveInspectionRecoveryTx(ctx, tx, v, source, facts, true)
	}
	now := time.Now().UTC()
	var verification any
	reviewComplete := false
	switch source.Purpose {
	case run.PurposeImplementation:
		var result struct{ ResultDisposition, Summary string }
		if json.Unmarshal(facts.OwnerResult, &result) != nil || result.ResultDisposition != "COMPLETED" || result.Summary == "" {
			return rs.ErrIneligible
		}
	case run.PurposeVerification:
		var report run.VerificationReport
		if json.Unmarshal(facts.OwnerResult, &report) != nil || report.Version != 2 {
			return rs.ErrIneligible
		}
		for _, check := range report.Checks {
			if check.Required && (check.Observed == nil || check.Observed.Status != run.VerificationCheckPassed) {
				return rs.ErrIneligible
			}
		}
		proposal, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, source.ExecutionProposalID))
		if err != nil {
			return err
		}
		if !reportMatchesVerificationSpec(report, proposal.VerificationSpec) || !authoritativeVerificationPasses(report, proposal.VerificationSpec) {
			return rs.ErrIneligible
		}
		if proposal.ApprovedWorksetHash != "" && (source.RuntimeWorkset == nil || !verificationObservationBindingsMatch(report, proposal.VerificationSpec, *source.RuntimeWorkset)) {
			return rs.ErrIneligible
		}
		if source.VerificationReport != nil {
			old, _ := json.Marshal(source.VerificationReport)
			fresh, _ := json.Marshal(report)
			if string(old) != string(fresh) {
				return rs.ErrIneligible
			}
		}
		encoded, err := encodeVerificationReportJSON(report)
		if err != nil {
			return rs.ErrIneligible
		}
		source.VerificationReport = &report
		verification = encoded
	case run.PurposeScopeReview:
		if facts.ReviewReport == nil || facts.ReviewReport.SchemaVersion != 2 || facts.ReviewReport.Validate() != nil {
			return rs.ErrIneligible
		}
		if _, err := review.ParseResult(string(facts.OwnerResult)); err != nil {
			return err
		}
		// Materialize after the source result below: an existing Review Decision
		// makes that result immutable. Both writes remain in this transaction.
		reviewComplete = true
	default:
		return fmt.Errorf("owner-specific recovered result contract required: %w", rs.ErrIneligible)
	}
	if err := materializeRecoveredMembersTx(ctx, tx, source, facts); err != nil {
		return err
	}
	source.Status = run.StatusSucceeded
	source.UpdatedAt = now
	source.FinishedAt = &now
	// Preserve any independently persisted original result; recovered bytes live
	// immutably in Facts.OwnerResult and are materialized only into missing slots.
	result, e := tx.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED',result_text=COALESCE(result_text,?),verification_result_json=COALESCE(verification_result_json,?),finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND turn_id=? AND status IN ('RUNNING','COLLECTING','INFRASTRUCTURE_FAILED','FAILED')`, string(facts.OwnerResult), verification, formatTime(now), formatTime(now), source.ID, source.TurnID)
	if e != nil {
		return e
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	if reviewComplete {
		if e = resolveReviewRecoveryCompleteTx(ctx, tx, v, source, facts, now); e != nil {
			return e
		}
	} else {
		if e = completeRecoveredTaskPhaseTx(ctx, tx, source); e != nil {
			return e
		}
	}
	if source.Purpose == run.PurposeVerification {
		if _, e = tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='SUCCEEDED',updated_at=? WHERE id=? AND outcome='RUNNING'`, formatTime(now), source.ExecutionProposalID); e != nil {
			return e
		}
	}
	if e = finishRecoveryDiagnosticTx(ctx, tx, v, now); e != nil {
		return e
	}
	// Source boundaries/old finalization/transport remain historical. The current
	// claim is released only here after the new child boundary is quiescent.
	if _, e = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_id=? AND operation_kind=?`, source.ID, source.OperationID, source.OperationKind); e != nil {
		return e
	}
	return nil
}

// resolveInspectionRecoveryTx is the owner variant bridge into the one
// canonical Inspection reducer. Its inputs are all durable assessment facts;
// it never treats the recovery Turn as the historical source Turn.
func (s *RecoveryStatusStore) resolveInspectionRecoveryTx(ctx context.Context, tx *sql.Tx, v *rs.Record, source run.Run, facts rs.FactualObservation, complete bool) error {
	var assessment rs.Assessment
	if json.Unmarshal(v.Assessment, &assessment) != nil || assessment.Consistency != "CONSISTENT" {
		return rs.ErrIneligible
	}
	if source.Purpose != run.PurposeInspection || v.Spec.OwnerKind != "PROJECT_INSPECTION" || source.ContextProbeID != v.Spec.OwnerID || source.WorkspaceID != v.Spec.WorkspaceID || source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != v.Spec.RuntimeWorksetHash {
		return rs.ErrIneligible
	}
	now := time.Now().UTC()
	if !complete {
		if assessment.EngineeringDisposition != "RECOVERED_PARTIAL" && assessment.EngineeringDisposition != "REVISION_REQUIRED" {
			return rs.ErrIneligible
		}
		state, err := inspectionRuntimeRead(ctx, tx, source.ID)
		if err != nil || len(facts.OwnerResult) == 0 {
			return rs.ErrIneligible
		}
		// Mark the recovery record resolved before the canonical reducer releases
		// the source claim.  This is still one SQLite transaction; the v83 claim
		// trigger therefore accepts only this assessed, receipt-backed partial.
		if assessment.EngineeringDisposition == "RECOVERED_PARTIAL" {
			v.State = "RESOLVED"
			if err = saveRecoveryTx(ctx, tx, *v); err != nil {
				return err
			}
		}
		in := probeexecution.InspectionFinalization{RunID: source.ID, InspectionID: source.ContextProbeID, RuntimeEpoch: source.RuntimeEpoch, ThreadID: source.ThreadID, TurnID: source.TurnID, WorksetHash: source.RuntimeWorkset.ContentHash, ReportHash: rs.Hash(facts.OwnerResult), Report: facts.OwnerResult, Before: facts.Before, After: facts.After, Terminal: true, TerminalStatus: "completed", Quiesced: true, ObservedAt: now}
		if err = NewInspectionRuntimeStore(s.db).finalizeInspectionTx(ctx, tx, &state, in, true); err != nil {
			return err
		}
		disposition := assessment.EngineeringDisposition
		if err = persistInspectionRecoveryResolutionTx(ctx, tx, v, disposition, now); err != nil {
			return err
		}
		return finishRecoveryDiagnosticTx(ctx, tx, v, now)
	}
	if assessment.EngineeringDisposition != "RECOVERED_COMPLETE" || assessment.InspectionResultHash != rs.Hash(facts.OwnerResult) {
		return rs.ErrIneligible
	}
	state, err := inspectionRuntimeRead(ctx, tx, source.ID)
	if err != nil {
		return err
	}
	in := probeexecution.InspectionFinalization{RunID: source.ID, InspectionID: source.ContextProbeID, RuntimeEpoch: source.RuntimeEpoch, ThreadID: source.ThreadID, TurnID: source.TurnID, WorksetHash: source.RuntimeWorkset.ContentHash, ReportHash: rs.Hash(facts.OwnerResult), Report: facts.OwnerResult, Before: facts.Before, After: facts.After, Terminal: true, TerminalStatus: "completed", Quiesced: true, ObservedAt: now}
	if err = NewInspectionRuntimeStore(s.db).finalizeInspectionTx(ctx, tx, &state, in, true); err != nil {
		return err
	}
	if err = persistInspectionRecoveryResolutionTx(ctx, tx, v, "RECOVERED_COMPLETE", now); err != nil {
		return err
	}
	return finishRecoveryDiagnosticTx(ctx, tx, v, now)
}

func persistInspectionRecoveryResolutionTx(ctx context.Context, tx *sql.Tx, v *rs.Record, disposition string, now time.Time) error {
	var assessment rs.Assessment
	var facts rs.FactualObservation
	if json.Unmarshal(v.Assessment, &assessment) != nil || json.Unmarshal(v.Facts, &facts) != nil || len(facts.OwnerResult) == 0 || assessment.InspectionResultHash == "" || assessment.InspectionResultHash != rs.Hash(facts.OwnerResult) {
		return rs.ErrIneligible
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO inspection_recovery_resolutions(recovery_id,inspection_id,source_run_id,source_turn_id,recovery_run_id,recovery_turn_id,report_hash,assessment_hash,owner_facts_hash,inspection_result_hash,result_source,disposition,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Spec.OwnerID, v.Spec.RunID, v.Spec.TurnID, v.RecoveryRunID, v.RecoveryTurnID, v.ReportHash, v.AssessmentHash, rs.Hash(v.Facts), assessment.InspectionResultHash, "RECOVERY_STATUS", disposition, formatTime(now))
	return err
}
