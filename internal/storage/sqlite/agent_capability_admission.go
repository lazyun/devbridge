package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// Pi's current adapter is threadless-only. Reject a known future resume need
// before durable admission, and recheck under the Run/claim transaction lock.
func validatePiResumeRequirementTx(ctx context.Context, q definitionQuerier, areaID string, runtime codexruntime.NextRunRuntimeConfig, verification task.VerificationMode) error {
	if runtime.AgentID != "pi" || runtime.ExecutionPolicy != agentpolicy.PolicyCooperativeTrustedV1 {
		return nil
	}
	unsupported := func() error {
		return &agent.CapabilityError{AgentID: "pi", Missing: []agent.Capability{agent.CapabilityThreadResume}}
	}
	if verification == task.VerificationModeRun {
		return unsupported()
	}
	var hasThread bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_conversations WHERE feature_area_id=? AND ended_at IS NULL AND COALESCE(backend_thread_id,'')<>'')`, areaID).Scan(&hasThread); err != nil {
		return err
	}
	if hasThread {
		return unsupported()
	}
	return nil
}

func validateRunAgentCapabilitiesTx(ctx context.Context, tx *sql.Tx, value run.Run) error {
	var raw sql.NullString
	verification := task.VerificationModeNone
	switch value.OperationKind {
	case run.OperationExecution:
		var mode string
		if err := tx.QueryRowContext(ctx, `SELECT runtime_config_json,COALESCE(json_extract(NULLIF(verification_spec_json,''),'$.mode'),'') FROM execution_proposals WHERE id=? AND project_id=? AND task_id=?`, value.OperationID, value.ProjectID, value.TaskID).Scan(&raw, &mode); err != nil {
			return err
		}
		verification = task.VerificationMode(mode)
	case run.OperationReview:
		if err := tx.QueryRowContext(ctx, `SELECT runtime_config_json FROM reviews WHERE id=? AND project_id=? AND feature_area_id=?`, value.OperationID, value.ProjectID, value.FeatureAreaID).Scan(&raw); err != nil {
			return err
		}
	default:
		return nil
	}
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var config codexruntime.NextRunRuntimeConfig
	if err := json.Unmarshal([]byte(raw.String), &config); err != nil {
		return err
	}
	return validatePiResumeRequirementTx(ctx, tx, value.FeatureAreaID, config, verification)
}
