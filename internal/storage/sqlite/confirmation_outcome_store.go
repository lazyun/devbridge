package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
)

func (s *ConfirmationStore) ReplayPrepare(ctx context.Context, session, request, submittedHash string) (confirmation.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, session); err != nil {
		return confirmation.Intent{}, err
	}
	return replayConfirmationPrepareTx(ctx, tx, session, request, submittedHash, s.db.epoch)
}
func replayConfirmationPrepareTx(ctx context.Context, q definitionQuerier, session, request, submittedHash, epoch string) (out confirmation.Intent, err error) {
	var id string
	var saved sql.NullString
	err = q.QueryRowContext(ctx, `SELECT intent_id,submitted_sha256 FROM confirmation_intents WHERE ai_session_id=? AND prepare_request_id=?`, session, request).Scan(&id, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return out, confirmation.ErrRequestNotObserved
	}
	if err != nil {
		return
	}
	// Old prepare_hash describes the post-validation frozen content. It cannot
	// prove the original field presence or derived-field-free submitted identity.
	if !saved.Valid || saved.String == "" || submittedHash == "" {
		return out, confirmation.ErrLegacyReplayUnproven
	}
	if saved.String != submittedHash {
		return out, confirmation.ErrRequestConflict
	}
	out, err = confirmationRead(ctx, q, session, id)
	if err != nil {
		return
	}
	if out.Commit != nil {
		sub, readErr := confirmationSubmissionTx(ctx, q, out, out.Commit.RequestID)
		if readErr != nil {
			return out, readErr
		}
		out.Submission = &sub
	}
	out, err = recoverConfirmationTx(ctx, q, out, epoch)
	if err == nil && out.CanResume && out.Submission != nil {
		out.Submission.NextAction = confirmation.ContinueOriginalSteps
	}
	return
}
func confirmationCommitIdentity(out confirmation.Intent, in confirmation.CommitInput) bool {
	return out.AISessionID == in.AISessionID && out.IntentID == in.IntentID && out.Version == in.Version && out.Purpose == in.Purpose && out.Action == in.Action && out.ContentHash == in.ContentHash
}
func confirmationOriginalRequest(in confirmation.CommitInput) confirmation.CommitInput {
	if in.OriginalRequest != nil {
		in = *in.OriginalRequest
	}
	in.OriginalRequest = nil
	return in
}
func confirmationSameChoice(a, b confirmation.CommitInput) bool {
	a.RequestID = ""
	b.RequestID = ""
	a.OriginalRequest = nil
	b.OriginalRequest = nil
	// Core-resolved display/provenance metadata is not caller choice identity.
	// The narrow selection still compares config ID/fingerprint exactly.
	a.BackendSelection = nil
	b.BackendSelection = nil
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func confirmationUnknownSubmission(out confirmation.Intent, request string) confirmation.Submission {
	return confirmation.Submission{Request: confirmation.CommitInput{AISessionID: out.AISessionID, IntentID: out.IntentID, Version: out.Version, Purpose: out.Purpose, Action: out.Action, ContentHash: out.ContentHash, RequestID: request}, Outcome: confirmation.Unknown, Recorded: false, NextAction: confirmation.ReadOriginalReceipt}
}
func confirmationConsumedSubmission(out confirmation.Intent, in confirmation.CommitInput) confirmation.Submission {
	receipt := in.RequestID
	if out.Commit != nil {
		receipt = out.Commit.RequestID
	}
	return confirmation.Submission{Request: confirmationOriginalRequest(in), PayloadSHA256: in.SubmittedHash, Outcome: confirmation.Consumed, Recorded: true, NextAction: confirmation.ReadOriginalReceipt, ReceiptRequestID: receipt}
}
func confirmationSubmissionTx(ctx context.Context, q definitionQuerier, out confirmation.Intent, request string) (confirmation.Submission, error) {
	sub := confirmationUnknownSubmission(out, request)
	var intent, payload string
	var raw, hash, outcome, failure sql.NullString
	err := q.QueryRowContext(ctx, `SELECT intent_id,payload_sha256,request_json,submitted_sha256,outcome,failure_json FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=?`, out.AISessionID, request).Scan(&intent, &payload, &raw, &hash, &outcome, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return sub, nil
	}
	if err != nil {
		return sub, err
	}
	if intent != out.IntentID {
		return sub, confirmation.ErrRequestConflict
	}
	if !raw.Valid || !hash.Valid || !outcome.Valid {
		// A legacy row proves consumption, but not the original unnormalized input.
		// Keep its actual stored payload fingerprint; do not fabricate raw evidence.
		if out.Commit == nil {
			return sub, ErrConflict
		}
		old := *out.Commit
		old.RequestID = request
		old.OriginalRequest = nil
		b, _ := json.Marshal(old)
		if workflowHash(string(b)) != payload {
			return sub, confirmation.ErrLegacyReplayUnproven
		}
		sub.Request = old
		sub.PayloadSHA256 = payload
		sub.Outcome = confirmation.Consumed
		sub.Recorded = true
		sub.ReceiptRequestID = out.Commit.RequestID
		return sub, nil
	}
	if err = json.Unmarshal([]byte(raw.String), &sub.Request); err != nil {
		return sub, err
	}
	if !confirmationCommitIdentity(out, sub.Request) || sub.Request.RequestID != request {
		return confirmationUnknownSubmission(out, request), ErrConflict
	}
	sub.PayloadSHA256 = hash.String
	sub.Outcome = confirmation.Outcome(outcome.String)
	sub.Recorded = true
	switch sub.Outcome {
	case confirmation.Consumed:
		if out.Commit == nil {
			return confirmationUnknownSubmission(out, request), ErrConflict
		}
		sub.ReceiptRequestID = out.Commit.RequestID
		if out.CanResume {
			sub.NextAction = confirmation.ContinueOriginalSteps
		}
	case confirmation.RejectedNotConsumed:
		if !failure.Valid {
			return confirmationUnknownSubmission(out, request), ErrConflict
		}
		sub.Failure = &confirmation.Failure{}
		if err = json.Unmarshal([]byte(failure.String), sub.Failure); err != nil {
			return confirmationUnknownSubmission(out, request), err
		}
		sub.NextAction = sub.Failure.NextAction
		if out.State != "PENDING" || out.Commit != nil || !time.Now().Before(out.ExpiresAt) {
			sub.NextAction = confirmation.ReadCurrentTarget
		}
	default:
		return confirmationUnknownSubmission(out, request), ErrConflict
	}
	return sub, nil
}

// latestConfirmationSubmissionTx resolves the intent's own most recent durable
// submission without requiring the caller to know its request id. A fence or
// validation rejection records a receipt without ever setting intent.Commit, so
// reading submissions only through Commit would hide a formal rejection from
// every surface that has no request id in hand.
func latestConfirmationSubmissionTx(ctx context.Context, q definitionQuerier, out confirmation.Intent) (confirmation.Submission, bool, error) {
	var request string
	err := q.QueryRowContext(ctx, `SELECT request_id FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=? ORDER BY rowid DESC LIMIT 1`, out.AISessionID, out.IntentID).Scan(&request)
	if errors.Is(err, sql.ErrNoRows) {
		return confirmation.Submission{}, false, nil
	}
	if err != nil {
		return confirmation.Submission{}, false, err
	}
	sub, err := confirmationSubmissionTx(ctx, q, out, request)
	if err != nil {
		// A row whose original request cannot be proven is not a fact a read may
		// project; an infrastructure failure still surfaces.
		if errors.Is(err, ErrConflict) || errors.Is(err, confirmation.ErrLegacyReplayUnproven) {
			return confirmation.Submission{}, false, nil
		}
		return confirmation.Submission{}, false, err
	}
	return sub, true, nil
}

func (s *ConfirmationStore) ReadSubmission(ctx context.Context, in confirmation.ReadInput) (confirmation.Submission, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.Submission{}, err
	}
	defer tx.Rollback()
	out, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return confirmation.Submission{}, err
	}
	if out.Version != in.Version || out.Purpose != in.Purpose || in.RequestID == "" {
		return confirmation.Submission{}, ErrConflict
	}
	// The request ledger can prove consumption even when a child receipt read
	// fails. Recovery remains a separate read, never a prerequisite for this fact.
	return confirmationSubmissionTx(ctx, tx, out, in.RequestID)
}

// Both write branches call this under beginWrite, sharing the original unique
// (Session,request) key. A recorded rejection is never revalidated into a write.
func confirmationReplayCommitTx(ctx context.Context, tx *sql.Tx, out confirmation.Intent, in confirmation.CommitInput) (confirmation.Submission, bool, error) {
	sub, err := confirmationSubmissionTx(ctx, tx, out, in.RequestID)
	if err != nil {
		return sub, false, err
	}
	if !sub.Recorded {
		return sub, false, nil
	}
	if in.SubmittedHash == "" || in.SubmittedHash != sub.PayloadSHA256 {
		return confirmationUnknownSubmission(out, in.RequestID), false, confirmation.ErrRequestConflict
	}
	if sub.Outcome == confirmation.Consumed {
		if out.Commit == nil || !confirmationSameChoice(*out.Commit, in) {
			return confirmationUnknownSubmission(out, in.RequestID), false, confirmation.ErrRequestConflict
		}
		if in.RequestID == out.Commit.RequestID {
			var typed string
			if err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=?`, in.AISessionID, in.RequestID, in.IntentID).Scan(&typed); err != nil {
				return sub, false, err
			}
			encoded, e := json.Marshal(out.Commit)
			if e != nil {
				return sub, false, e
			}
			if workflowHash(string(encoded)) != typed {
				return confirmationUnknownSubmission(out, in.RequestID), false, confirmation.ErrRequestConflict
			}
		}
	}
	return sub, true, nil
}
func confirmationInsertSubmissionTx(ctx context.Context, tx *sql.Tx, in confirmation.CommitInput, sub confirmation.Submission) error {
	if in.SubmittedHash == "" {
		return confirmation.ErrRequestConflict
	}
	// The original raw hash is supplied before normalization; the old column
	// remains the canonical typed payload hash, for unchanged historical meaning.
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	original, err := json.Marshal(sub.Request)
	if err != nil {
		return err
	}
	var failure any
	if sub.Failure != nil {
		b, e := json.Marshal(sub.Failure)
		if e != nil {
			return e
		}
		failure = string(b)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO confirmation_commit_requests(ai_session_id,request_id,intent_id,payload_sha256,submitted_sha256,request_json,outcome,failure_json) VALUES(?,?,?,?,?,?,?,?)`, in.AISessionID, in.RequestID, in.IntentID, workflowHash(string(raw)), in.SubmittedHash, string(original), string(sub.Outcome), failure)
	return err
}
func (s *ConfirmationStore) RejectCommit(ctx context.Context, in confirmation.CommitInput, failure *confirmation.Failure) (out confirmation.Intent, err error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	out, err = confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return
	}
	if !confirmationCommitIdentity(out, in) || in.RequestID == "" || failure == nil {
		return out, confirmation.ErrRequestConflict
	}
	prior, observed, err := confirmationReplayCommitTx(ctx, tx, out, in)
	if err != nil {
		return out, err
	}
	if observed {
		out.Submission = &prior
		if prior.Outcome == confirmation.RejectedNotConsumed {
			return out, prior.Failure
		}
		return out, nil
	}
	// Another request's consumed choice is durable. This call must not assert
	// non-consumption or reopen that intent, even if its validation just failed.
	if out.Commit != nil {
		return out, confirmation.ErrRequestConflict
	}
	sub := confirmation.Submission{Request: confirmationOriginalRequest(in), PayloadSHA256: in.SubmittedHash, Outcome: confirmation.RejectedNotConsumed, Recorded: true, Failure: failure, NextAction: failure.NextAction}
	if out.State != "PENDING" || !time.Now().Before(out.ExpiresAt) {
		sub.NextAction = confirmation.ReadCurrentTarget
	}
	if err = confirmationInsertSubmissionTx(ctx, tx, in, sub); err != nil {
		return out, err
	}
	if err = recordPurposeRejectionTx(ctx, tx, out, in, failure); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Submission = &sub
	return out, failure
}
