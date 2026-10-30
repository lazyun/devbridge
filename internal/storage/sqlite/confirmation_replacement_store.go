package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
)

const confirmationReplacementV107SQL = `
CREATE TABLE confirmation_replacement_receipts (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 actor_scope TEXT NOT NULL CHECK(actor_scope<>''),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 replacement_kind TEXT NOT NULL CHECK(replacement_kind IN ('DECISION','RECOVERY_PURPOSE')),
 source_intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 source_intent_version INTEGER NOT NULL CHECK(source_intent_version>0),
 source_intent_content_hash TEXT NOT NULL CHECK(length(source_intent_content_hash)=64),
 replacement_intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 replacement_intent_version INTEGER NOT NULL CHECK(replacement_intent_version=1),
 replacement_intent_content_hash TEXT NOT NULL CHECK(length(replacement_intent_content_hash)=64),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
 PRIMARY KEY(ai_session_id,request_id),
 UNIQUE(ai_session_id,source_intent_id),
 CHECK(source_intent_id<>replacement_intent_id)
);
CREATE TRIGGER confirmation_replacement_receipt_no_update BEFORE UPDATE ON confirmation_replacement_receipts BEGIN SELECT RAISE(ABORT,'Confirmation replacement receipt immutable'); END;
CREATE TRIGGER confirmation_replacement_receipt_no_delete BEFORE DELETE ON confirmation_replacement_receipts BEGIN SELECT RAISE(ABORT,'Confirmation replacement receipt immutable'); END;
CREATE TABLE confirmation_current_decisions (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 decision_id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id),
 intent_id TEXT NOT NULL UNIQUE REFERENCES confirmation_intents(intent_id),
 version INTEGER NOT NULL CHECK(version>0), content_hash TEXT NOT NULL CHECK(length(content_hash)=64),
 selection_version INTEGER NOT NULL CHECK(selection_version>0),
 PRIMARY KEY(ai_session_id,decision_id)
);
-- Only unambiguous pending legacy sources gain a selector. Multiple pending
-- candidates require a fresh prepare, never a newest-timestamp authority guess.
INSERT INTO confirmation_current_decisions
SELECT ai_session_id,json_extract(intent_json,'$.content.review.decisionId'),json_extract(intent_json,'$.content.review.taskId'),intent_id,
 json_extract(intent_json,'$.version'),json_extract(intent_json,'$.contentHash'),1
FROM confirmation_intents WHERE json_extract(intent_json,'$.purpose')='REVIEW'
 AND json_extract(intent_json,'$.action') IN ('REVISE','REJECT') AND json_extract(intent_json,'$.state')='PENDING'
 AND COALESCE(json_extract(intent_json,'$.content.review.decisionId'),'')<>''
GROUP BY ai_session_id,json_extract(intent_json,'$.content.review.decisionId') HAVING count(*)=1;
`

type confirmationReplacementBinding struct {
	RecoveryID                                                           string
	RecoveryGeneration                                                   int
	SourceOwnerKind, SourceOwnerID, PredecessorLinkID, ReplacementLinkID string
	PredecessorLinkOrdinal, LinkOrdinal, ExpectedSelectionVersion        int
}

func selectPreparedDecisionTx(ctx context.Context, tx *sql.Tx, next confirmation.Intent) error {
	body := next.Content.Review
	if next.Purpose != confirmation.Review || body == nil || (next.Action != "REVISE" && next.Action != "REJECT") {
		return nil
	}
	if next.Content.SourceIntentID == "" {
		_, err := tx.ExecContext(ctx, `INSERT INTO confirmation_current_decisions(ai_session_id,decision_id,task_id,intent_id,version,content_hash,selection_version) VALUES(?,?,?,?,?,?,1) ON CONFLICT(ai_session_id,decision_id) DO UPDATE SET task_id=excluded.task_id,intent_id=excluded.intent_id,version=excluded.version,content_hash=excluded.content_hash,selection_version=selection_version+1`, next.AISessionID, body.DecisionID, body.TaskID, next.IntentID, next.Version, next.ContentHash)
		return err
	}
	old, err := confirmationRead(ctx, tx, next.AISessionID, next.Content.SourceIntentID)
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE confirmation_current_decisions SET intent_id=?,version=?,content_hash=?,selection_version=selection_version+1 WHERE ai_session_id=? AND decision_id=? AND task_id=? AND intent_id=? AND version=? AND content_hash=?`, next.IntentID, next.Version, next.ContentHash, next.AISessionID, body.DecisionID, body.TaskID, old.IntentID, old.Version, old.ContentHash)
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return confirmation.ErrRequestConflict
	}
	return nil
}

func currentReplacementDecisionTx(ctx context.Context, tx *sql.Tx, source confirmation.Intent) (bool, error) {
	var current bool
	if source.Content.Review == nil {
		return false, nil
	}
	b := source.Content.Review
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_current_decisions WHERE ai_session_id=? AND decision_id=? AND task_id=? AND intent_id=? AND version=? AND content_hash=?)`, source.AISessionID, b.DecisionID, b.TaskID, source.IntentID, source.Version, source.ContentHash).Scan(&current)
	return current, err
}

func (s *ConfirmationStore) ReplayReplacement(ctx context.Context, in confirmation.ReprepareInput) (confirmation.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	r, _, err := readConfirmationReplacementReceiptTx(ctx, tx, in.AISessionID, in.RequestID)
	if errors.Is(err, confirmation.ErrRequestNotObserved) {
		// A generic prepare row without a replacement receipt is not a replay
		// proof. Never turn an incomplete historical prefix into a success.
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_intents WHERE ai_session_id=? AND prepare_request_id=?)`, in.AISessionID, in.RequestID).Scan(&exists); e != nil {
			return confirmation.Intent{}, e
		}
		if exists {
			return confirmation.Intent{}, confirmation.ErrRequestConflict
		}
		return confirmation.Intent{}, err
	}
	if err != nil {
		return confirmation.Intent{}, err
	}
	var binding *confirmationReplacementBinding
	if r.Kind == "RECOVERY_PURPOSE" {
		binding, err = recoveryReplacementReplayBindingTx(ctx, tx, in.SourceIntentID)
		if err != nil {
			return confirmation.Intent{}, err
		}
	}
	out, found, err := replayConfirmationReplacementTx(ctx, tx, in, r.Kind, binding)
	if err == nil && !found {
		err = confirmation.ErrRequestNotObserved
	}
	return out, err
}

func replacementPayloadHash(ctx context.Context, in confirmation.ReprepareInput, source confirmation.Intent, kind string, b *confirmationReplacementBinding) (string, error) {
	actor, ok := aisession.CallerScopeFromContext(ctx)
	if !ok || actor == "" || in.RequestID == "" || source.AISessionID != in.AISessionID || source.IntentID != in.SourceIntentID || source.Version != in.SourceVersion || source.ContentHash != in.SourceContentHash {
		return "", confirmation.ErrRequestConflict
	}
	input := map[string]any{}
	if in.ReviewFeedback != "" {
		input["reviewFeedback"] = in.ReviewFeedback
	}
	if in.RecoveryPurpose != nil {
		input["recoveryPurpose"] = in.RecoveryPurpose
	}
	value := map[string]any{"authenticatedActor": actor, "sourceIntentId": in.SourceIntentID, "sourceIntentVersion": in.SourceVersion, "sourceIntentContentHash": in.SourceContentHash, "sourcePurpose": source.Purpose, "replacementKind": kind, "submittedPreparationInput": input}
	switch kind {
	case "DECISION":
		if b != nil || in.RecoveryPurpose != nil || source.Purpose == confirmation.RecoveryPurpose {
			return "", confirmation.ErrRequestConflict
		}
	case "RECOVERY_PURPOSE":
		if b == nil || source.Purpose != confirmation.RecoveryPurpose || in.ReviewFeedback != "" || b.RecoveryID == "" || b.RecoveryGeneration < 1 || b.PredecessorLinkID == "" || b.PredecessorLinkOrdinal < 1 || b.ExpectedSelectionVersion < 1 {
			return "", confirmation.ErrRequestConflict
		}
		value["recoveryId"], value["recoveryGeneration"] = b.RecoveryID, b.RecoveryGeneration
		value["sourceOwnerKind"], value["sourceOwnerId"] = b.SourceOwnerKind, b.SourceOwnerID
		value["predecessorLinkId"], value["predecessorLinkOrdinal"], value["expectedSelectionVersion"] = b.PredecessorLinkID, b.PredecessorLinkOrdinal, b.ExpectedSelectionVersion
	default:
		return "", confirmation.ErrRequestConflict
	}
	// InputHash recursively sorts object keys and retains JSON number tokens,
	// including integer identities larger than float64's exact range.
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return confirmation.InputHash(raw)
}

func readConfirmationReplacementReceiptTx(ctx context.Context, tx *sql.Tx, session, request string) (*confirmation.ReplacementReceipt, string, error) {
	if err := instructionCaller(ctx, tx, session); err != nil {
		return nil, "", err
	}
	var raw, actor, hash, kind, sourceID, sourceHash, childID, childHash string
	var sourceVersion, childVersion int
	err := tx.QueryRowContext(ctx, `SELECT receipt_json,actor_scope,payload_sha256,replacement_kind,source_intent_id,source_intent_version,source_intent_content_hash,replacement_intent_id,replacement_intent_version,replacement_intent_content_hash FROM confirmation_replacement_receipts WHERE ai_session_id=? AND request_id=?`, session, request).Scan(&raw, &actor, &hash, &kind, &sourceID, &sourceVersion, &sourceHash, &childID, &childVersion, &childHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", confirmation.ErrRequestNotObserved
	}
	if err != nil {
		return nil, "", err
	}
	var r confirmation.ReplacementReceipt
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, "", err
	}
	if r.AISessionID != session || r.RequestID != request || r.PayloadSHA256 != hash || r.Kind != kind || r.SourceIntentID != sourceID || r.SourceVersion != sourceVersion || r.SourceContentHash != sourceHash || r.ReplacementIntentID != childID || r.ReplacementVersion != childVersion || r.ReplacementContentHash != childHash || r.Outcome != "REPLACED" {
		return nil, "", confirmation.ErrRequestConflict
	}
	caller, _ := aisession.CallerScopeFromContext(ctx)
	if caller != actor {
		return nil, "", confirmation.ErrRequestConflict
	}
	return &r, actor, nil
}

func replayConfirmationReplacementTx(ctx context.Context, tx *sql.Tx, in confirmation.ReprepareInput, kind string, b *confirmationReplacementBinding) (confirmation.Intent, bool, error) {
	r, _, err := readConfirmationReplacementReceiptTx(ctx, tx, in.AISessionID, in.RequestID)
	if errors.Is(err, confirmation.ErrRequestNotObserved) {
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_intents WHERE ai_session_id=? AND prepare_request_id=?)`, in.AISessionID, in.RequestID).Scan(&exists); e != nil {
			return confirmation.Intent{}, false, e
		}
		if exists {
			return confirmation.Intent{}, false, confirmation.ErrRequestConflict
		}
		return confirmation.Intent{}, false, nil
	}
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	source, err := confirmationRead(ctx, tx, in.AISessionID, in.SourceIntentID)
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	hash, err := replacementPayloadHash(ctx, in, source, kind, b)
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	if r.Kind != kind || r.PayloadSHA256 != hash || r.SourceIntentID != in.SourceIntentID || r.SourceVersion != in.SourceVersion || r.SourceContentHash != in.SourceContentHash {
		return confirmation.Intent{}, false, confirmation.ErrRequestConflict
	}
	child, err := confirmationRead(ctx, tx, in.AISessionID, r.ReplacementIntentID)
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	if child.Version != r.ReplacementVersion || child.ContentHash != r.ReplacementContentHash || child.Content.SourceIntentID != source.IntentID {
		return confirmation.Intent{}, false, confirmation.ErrRequestConflict
	}
	child.ReplacementReceipt = r
	return child, true, nil
}

func insertConfirmationReplacementReceiptTx(ctx context.Context, tx *sql.Tx, in confirmation.ReprepareInput, kind string, out confirmation.Intent, b *confirmationReplacementBinding) error {
	source, err := confirmationRead(ctx, tx, in.AISessionID, in.SourceIntentID)
	if err != nil {
		return err
	}
	hash, err := replacementPayloadHash(ctx, in, source, kind, b)
	if err != nil {
		return err
	}
	if source.State != "SUPERSEDED" || out.State != "PENDING" || out.Version != 1 || out.Content.SourceIntentID != source.IntentID || out.IntentID == source.IntentID {
		return confirmation.ErrRequestConflict
	}
	r := confirmation.ReplacementReceipt{AISessionID: in.AISessionID, RequestID: in.RequestID, PayloadSHA256: hash, Kind: kind, SourceIntentID: source.IntentID, SourceVersion: source.Version, SourceContentHash: source.ContentHash, ReplacementIntentID: out.IntentID, ReplacementVersion: out.Version, ReplacementContentHash: out.ContentHash, Outcome: "REPLACED"}
	if b != nil {
		if b.ReplacementLinkID == "" || b.LinkOrdinal != b.PredecessorLinkOrdinal+1 {
			return confirmation.ErrRequestConflict
		}
		r.RecoveryID, r.RecoveryGeneration, r.SourceOwnerKind, r.SourceOwnerID = b.RecoveryID, b.RecoveryGeneration, b.SourceOwnerKind, b.SourceOwnerID
		r.PredecessorLinkID, r.ReplacementLinkID, r.LinkOrdinal = b.PredecessorLinkID, b.ReplacementLinkID, b.LinkOrdinal
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	actor, _ := aisession.CallerScopeFromContext(ctx)
	_, err = tx.ExecContext(ctx, `INSERT INTO confirmation_replacement_receipts(ai_session_id,request_id,actor_scope,payload_sha256,replacement_kind,source_intent_id,source_intent_version,source_intent_content_hash,replacement_intent_id,replacement_intent_version,replacement_intent_content_hash,receipt_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, in.AISessionID, in.RequestID, actor, hash, kind, source.IntentID, source.Version, source.ContentHash, out.IntentID, out.Version, out.ContentHash, string(raw))
	return err
}

func (s *ConfirmationStore) ReadReplacementReceipt(ctx context.Context, in confirmation.ReadInput) (*confirmation.ReplacementReceipt, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return nil, err
	}
	if source.Version != in.Version || source.ContentHash != in.ContentHash || source.Purpose != in.Purpose {
		return nil, confirmation.ErrRequestConflict
	}
	r, _, err := readConfirmationReplacementReceiptTx(ctx, tx, in.AISessionID, in.RequestID)
	if err != nil {
		return nil, err
	}
	if r.SourceIntentID != in.IntentID || r.SourceVersion != in.Version || r.SourceContentHash != in.ContentHash {
		return nil, confirmation.ErrRequestConflict
	}
	return r, nil
}

func (s *ConfirmationStore) ReprepareDecision(ctx context.Context, project string, in confirmation.ReprepareInput, candidate confirmation.PrepareInput) (confirmation.Intent, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	if out, found, e := replayConfirmationReplacementTx(ctx, tx, in, "DECISION", nil); e != nil || found {
		return out, e
	}
	source, err := confirmationRead(ctx, tx, in.AISessionID, in.SourceIntentID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	hash, err := replacementPayloadHash(ctx, in, source, "DECISION", nil)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if source.State != "PENDING" && source.State != "STALE" {
		return confirmation.Intent{}, confirmation.NewFailure("SUCCESSOR_ALREADY_EXISTS", "CORE", "REPLACEMENT", "The original confirmation cannot create another replacement.", confirmation.ReadCurrentTarget, nil)
	}
	if source.ProjectID != project || source.Purpose != confirmation.Review || source.Content.Review == nil || (source.Action != "REVISE" && source.Action != "REJECT") {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	if current, e := currentReplacementDecisionTx(ctx, tx, source); e != nil {
		return confirmation.Intent{}, e
	} else if !current {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=?`, in.AISessionID, in.SourceIntentID).Scan(&attempts); err != nil {
		return confirmation.Intent{}, err
	}
	if attempts > 0 {
		var rejected int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=? AND outcome='REJECTED_NOT_CONSUMED'`, in.AISessionID, in.SourceIntentID).Scan(&rejected); err != nil {
			return confirmation.Intent{}, err
		}
		if rejected != attempts || source.Commit != nil {
			return confirmation.Intent{}, confirmation.ErrRequestConflict
		}
		// Rejections that allow selection edits do not establish reprepare authority.
		sub, found, e := latestConfirmationSubmissionTx(ctx, tx, source)
		if e != nil {
			return confirmation.Intent{}, e
		}
		if !found {
			return confirmation.Intent{}, confirmation.ErrRequestConflict
		}
		source.Submission = &sub
		confirmation.NormalizePublicIntent(&source)
		if !sub.Recorded || sub.RetryDisposition != confirmation.RetryReprepareRequired {
			return confirmation.Intent{}, confirmation.ErrRequestConflict
		}
	} else {
		failure, e := confirmationDecisionFenceTx(ctx, tx, source, false)
		if e != nil {
			return confirmation.Intent{}, e
		}
		if source.Commit != nil || (failure == nil && source.State != "STALE" && time.Now().Before(source.ExpiresAt)) {
			return confirmation.Intent{}, confirmation.NewFailure("SOURCE_INTENT_NOT_STALE", "CORE", "REPLACEMENT", "A current unsubmitted confirmation needs no replacement.", confirmation.ReadCurrentTarget, nil)
		}
	}
	if candidate.AISessionID != in.AISessionID || candidate.SourceIntentID != source.IntentID || candidate.RequestID != in.RequestID || candidate.Purpose != source.Purpose || candidate.Action != source.Action || candidate.Review == nil || candidate.Review.DecisionID != source.Content.Review.DecisionID || candidate.Review.TaskID != source.Content.Review.TaskID || candidate.Review.ReviewID != source.Content.Review.ReviewID {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	prospective := source
	prospective.Content = candidate
	if failure, e := confirmationDecisionFenceTx(ctx, tx, prospective, false); e != nil {
		return confirmation.Intent{}, e
	} else if failure != nil {
		return confirmation.Intent{}, failure
	}
	// A stale production projection may be represented without a persisted STALE
	// row. The replacement and supersession writer still serializes on this row.
	if source.State == "STALE" {
		source.State = "PENDING"
		if err = confirmationSave(ctx, tx, source); err != nil {
			return confirmation.Intent{}, err
		}
	}
	candidate.SubmittedHash = hash
	out, err := prepareConfirmationIntentTx(ctx, tx, project, candidate, time.Now().UTC(), s.db.epoch)
	if err != nil {
		return out, err
	}
	if err = insertConfirmationReplacementReceiptTx(ctx, tx, in, "DECISION", out, nil); err != nil {
		return confirmation.Intent{}, err
	}
	out.ReplacementReceipt, _, err = readConfirmationReplacementReceiptTx(ctx, tx, in.AISessionID, in.RequestID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if err = tx.Commit(); err != nil {
		return confirmation.Intent{}, err
	}
	return out, nil
}
