package sqlite

import (
	"context"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// Network is granted by exactly two independent sources: the immutable task
// definition admitted for this Execution, or an explicit project-level
// authorization frozen into this Run's TaskPermissions at first binding.
// Neither a model's native permission request nor an environment manifest can
// issue it. Any non-nil project snapshot — trusted, network-only, or riding on
// a task definition that already granted network — must match an immutable
// authorization revision row for the exact project; a forged or superseded
// grant is rejected here no matter how the network booleans compare.
func validateTaskPermissionAuthorityTx(ctx context.Context, q definitionQuerier, w workset.RuntimeWorkset) error {
	p := w.TaskPermissions
	if p == nil || p.Purpose == "SCOPE_REVIEW" {
		return nil
	}
	var raw, hash, revision string
	if err := q.QueryRowContext(ctx, `SELECT d.payload_json,d.payload_sha256,d.id FROM execution_proposals e JOIN task_definition_revisions d ON d.id=e.task_definition_revision_id AND d.task_id=e.task_id WHERE e.id=? AND e.task_id=? AND e.project_id=?`, w.ExecutionID, w.TaskID, w.ProjectID).Scan(&raw, &hash, &revision); err != nil {
		return err
	}
	if revision != w.TaskDefinitionRevisionID {
		return ErrConflict
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return err
	}
	definitionNetwork := d.WorkSpec.CommandPermissions != nil && d.WorkSpec.CommandPermissions.NetworkAccess
	t := p.TrustedExecution
	if t == nil {
		// No project snapshot: network must come from the frozen definition.
		if definitionNetwork != p.NetworkAccess {
			return ErrConflict
		}
		return nil
	}
	// A project snapshot exists. The frozen snapshot must name this exact
	// project and revision, and the durable authorization history must still
	// contain that revision with exactly the frozen capabilities. The check
	// below runs for every snapshot, including trusted-only (network=false)
	// and snapshots on runs whose definition already granted network: network
	// equality never bypasses the project-grant verification.
	if t.ProjectID != w.ProjectID {
		return ErrConflict
	}
	// Effective network is the union of the two independent sources; the
	// manifest may never claim network the sources do not carry.
	if p.NetworkAccess != (definitionNetwork || t.NetworkFromAuthorization) {
		return ErrConflict
	}
	bound := 0
	if err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs WHERE id=? AND runtime_workset_json IS NOT NULL) + (SELECT count(*) FROM runtime_workset_bindings WHERE run_id=?)`, w.RunID, w.RunID).Scan(&bound); err != nil {
		return ErrConflict
	}
	headRequired := bound == 0
	// Read the historical revision row r — never the mutable head flags — so
	// an exact replay of an already-bound Run stays valid after the operator
	// revokes trusted execution or network. First bindings additionally
	// require the revision to be the current head, closing the
	// prepare->revoke->bind race; a new payload under an already-bound Run
	// gains nothing because it still names this exact historical revision.
	query := `SELECT r.trusted_execution=1,r.network_access=1 FROM project_execution_authorization_revisions r WHERE r.project_id=? AND r.revision=?`
	if headRequired {
		query += ` AND r.revision=(SELECT a.head_revision FROM project_execution_authorizations a WHERE a.project_id=r.project_id)`
	}
	var trusted, network bool
	if err := q.QueryRowContext(ctx, query, t.ProjectID, t.AuthorizationRevision).Scan(&trusted, &network); err != nil {
		return ErrConflict
	}
	if trusted != t.Trusted || network != t.NetworkFromAuthorization {
		return ErrConflict
	}
	return nil
}
