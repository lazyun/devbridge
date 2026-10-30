package sqlite

import (
	"context"

	"github.com/lazyun/devbridge/internal/workset"
)

// A runtime workset cannot grant its own cooperative mode. The direct binding
// path (used for reviews and verification) must match the immutable runtime
// selection of its formal owner, including absence on historical selections.
// Prepared bindings instead compare their exact immutable scope plan.
func validateDirectAgentPolicyAuthorityTx(ctx context.Context, q definitionQuerier, w workset.RuntimeWorkset) error {
	var agentID, policy, fingerprint string
	err := q.QueryRowContext(ctx, `SELECT
 COALESCE(json_extract(NULLIF(e.runtime_config_json,''),'$.agentId'),json_extract(NULLIF(v.runtime_config_json,''),'$.agentId'),''),
 COALESCE(json_extract(NULLIF(e.runtime_config_json,''),'$.executionPolicy'),json_extract(NULLIF(v.runtime_config_json,''),'$.executionPolicy'),''),
 COALESCE(json_extract(NULLIF(e.runtime_config_json,''),'$.agentConfigFingerprint'),json_extract(NULLIF(v.runtime_config_json,''),'$.agentConfigFingerprint'),'')
 FROM runs r LEFT JOIN execution_proposals e ON e.id=r.execution_proposal_id
 LEFT JOIN reviews v ON v.id=r.review_id WHERE r.id=?`, w.RunID).Scan(&agentID, &policy, &fingerprint)
	if err != nil {
		return err
	}
	if fingerprint != w.AgentConfigFingerprint || agentID != w.AgentID || policy != w.ExecutionPolicy {
		return ErrConflict
	}
	return nil
}
