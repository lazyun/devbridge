package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
)

type clientBindingEntry struct {
	handle, scope, requestID, host string
	selection, session, project    sql.NullString
	version                        int64
}

const clientBindingColumns = `selection_handle,caller_scope,presentation_request_id,host_reference,selection_id,ai_session_id,project_id,version`

func scanClientBinding(row rowScanner) (clientBindingEntry, error) {
	var value clientBindingEntry
	err := row.Scan(&value.handle, &value.scope, &value.requestID, &value.host, &value.selection, &value.session, &value.project, &value.version)
	return value, err
}

// resolveClientBindingTx never chooses a latest or first association. A host
// reference with multiple candidates is ambiguous, even if they share Project.
func resolveClientBindingTx(ctx context.Context, tx *sql.Tx, ref aiprojectselection.ClientBindingReference) (clientBindingEntry, error) {
	var byHandle clientBindingEntry
	if ref.SelectionHandle != "" {
		value, err := scanClientBinding(tx.QueryRowContext(ctx, `SELECT `+clientBindingColumns+` FROM client_binding_entries WHERE selection_handle=? AND caller_scope=?`, ref.SelectionHandle, ref.CallerScope))
		if err != nil {
			return clientBindingEntry{}, err
		}
		byHandle = value
	}
	if ref.HostReference != "" {
		rows, err := tx.QueryContext(ctx, `SELECT `+clientBindingColumns+` FROM client_binding_entries WHERE caller_scope=? AND host_reference=? LIMIT 2`, ref.CallerScope, ref.HostReference)
		if err != nil {
			return clientBindingEntry{}, err
		}
		defer rows.Close()
		var byHost clientBindingEntry
		count := 0
		for rows.Next() {
			value, scanErr := scanClientBinding(rows)
			if scanErr != nil {
				return clientBindingEntry{}, scanErr
			}
			byHost = value
			count++
		}
		if err := rows.Err(); err != nil {
			return clientBindingEntry{}, err
		}
		if count > 1 || (byHandle.handle != "" && (count != 1 || byHandle.handle != byHost.handle)) {
			return clientBindingEntry{}, aiprojectselection.ErrBindingConflict
		}
		if count == 0 {
			return clientBindingEntry{}, sql.ErrNoRows
		}
		return byHost, nil
	}
	if byHandle.handle != "" {
		return byHandle, nil
	}
	return clientBindingEntry{}, sql.ErrNoRows
}

func clientBindingResultTx(ctx context.Context, tx *sql.Tx, entry clientBindingEntry) (aiprojectselection.BindingResult, error) {
	result := aiprojectselection.BindingResult{SelectionHandle: entry.handle, Version: entry.version, Source: "EXACT_SELECTION"}
	if entry.version == 1 && !entry.selection.Valid && !entry.session.Valid && !entry.project.Valid {
		result.State = aiprojectselection.BindingUnbound
		return result, nil
	}
	result.State, result.Reason = aiprojectselection.BindingStale, "BOUND_IDENTITY_UNAVAILABLE"
	if entry.version != 2 || !entry.selection.Valid || !entry.session.Valid || !entry.project.Valid {
		return result, nil
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM ai_sessions s JOIN projects p ON p.id=s.project_id
 JOIN ai_project_selections c ON c.id=? AND c.ai_session_id=s.id AND c.project_id=s.project_id
 WHERE s.id=? AND s.project_id=? AND c.status='CONSUMED' AND c.consumed_at IS NOT NULL)`, entry.selection.String, entry.session.String, entry.project.String).Scan(&valid)
	if err != nil {
		return aiprojectselection.BindingResult{}, err
	}
	if valid {
		result.State, result.Reason = aiprojectselection.BindingBound, ""
		result.AISessionID, result.ProjectID = entry.session.String, entry.project.String
	}
	return result, nil
}

// Legacy handle creation is disabled. Historical v50 entries remain readable.
func (s *AISessionStore) EnsureBindingEntry(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID, handle string, at time.Time) (aiprojectselection.BindingResult, error) {
	return aiprojectselection.BindingResult{}, aisession.ErrInitializationRequired
}

// ReadBinding uses a read-only transaction and performs no writer authority
// update, selection consumption, usage refresh, or Project Context bootstrap.
func (s *AISessionStore) ReadBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error) {
	if !ref.Valid() {
		return aiprojectselection.BindingResult{}, aiprojectselection.ErrBindingInvalid
	}
	if ref.SelectionHandle == "" && ref.HostReference == "" {
		return aiprojectselection.BindingResult{State: aiprojectselection.BindingUnavailable, Source: "EXACT_SELECTION", Reason: "NO_BINDING_REFERENCE"}, nil
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return aiprojectselection.BindingResult{}, err
	}
	defer tx.Rollback()
	entry, err := resolveClientBindingTx(ctx, tx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		state, reason := aiprojectselection.BindingStale, "HOST_REFERENCE_UNAVAILABLE"
		if ref.SelectionHandle != "" {
			state, reason = aiprojectselection.BindingStale, "SELECTION_ENTRY_UNAVAILABLE"
		}
		return aiprojectselection.BindingResult{State: state, SelectionHandle: ref.SelectionHandle, Source: "EXACT_SELECTION", Reason: reason}, nil
	}
	if err != nil {
		return aiprojectselection.BindingResult{}, err
	}
	return clientBindingResultTx(ctx, tx, entry)
}

func boundSessionTx(ctx context.Context, tx *sql.Tx, id string) (aisession.Session, error) {
	var result aisession.Session
	var created, updated string
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,created_at,updated_at FROM ai_sessions WHERE id=?`, id).Scan(&result.ID, &result.ProjectID, &created, &updated)
	if err != nil {
		return aisession.Session{}, err
	}
	result.CreatedAt, err = parseTime(created)
	if err != nil {
		return aisession.Session{}, err
	}
	result.UpdatedAt, err = parseTime(updated)
	return result, err
}

// OpenBinding only returns an already completed exact v50 association. It never
// creates a Session, consumes a selection, or prepares the Project.
func (s *AISessionStore) OpenBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference, existingSessionID, projectID, selectionID, sessionID string, at time.Time) (aisession.Session, bool, error) {
	if !ref.Valid() {
		return aisession.Session{}, false, aiprojectselection.ErrBindingInvalid
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return aisession.Session{}, false, err
	}
	defer tx.Rollback()
	entry, err := resolveClientBindingTx(ctx, tx, ref)
	if err != nil {
		return aisession.Session{}, false, err
	}
	binding, err := clientBindingResultTx(ctx, tx, entry)
	if err != nil {
		return aisession.Session{}, false, err
	}
	if binding.State != aiprojectselection.BindingBound {
		return aisession.Session{}, false, aisession.ErrInitializationRequired
	}
	if binding.ProjectID != projectID || (existingSessionID != "" && existingSessionID != binding.AISessionID) {
		return aisession.Session{}, false, aiprojectselection.ErrBindingConflict
	}
	value, err := boundSessionTx(ctx, tx, binding.AISessionID)
	return value, false, err
}
