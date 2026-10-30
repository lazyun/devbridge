package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Only bootstrap owns recovery effects. Projection reads use the last durable
// observation and cannot trigger a connection, Thread resume, or answer.
func recoverAppServerRuns(ctx context.Context, dataRoot string, store *sqlite.AgentRuntimeStore, runs *run.Service, supervisor *execution.RuntimeSupervisor, proposals *executionproposal.Service, recoveryStore *sqlite.RecoveryStatusStore) error {
	ids, err := store.ListAppServerCandidates(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		value, err := runs.Get(ctx, id)
		if err != nil {
			return err
		}

		if value.Purpose == run.PurposeRecoveryStatus || value.OperationKind == run.OperationInspection {
			continue
		}
		if _, readErr := recoveryStore.LatestForSource(ctx, id); readErr == nil {
			continue
		}
		binding, err := store.AppServerBinding(ctx, id)
		if err != nil {
			return err
		}
		observation := agentruntime.RuntimeObservation{AppServerInstanceID: binding.ID, InstanceEpoch: int64(binding.Epoch), InstanceStatus: "UNVERIFIED", ConnectionState: "RECONNECTING", ThreadID: value.ThreadID, TurnID: value.TurnID, RequestReattachment: "UNPROVEN", ObservedAt: time.Now().UTC(), SourceMethod: "INSTANCE_IDENTITY", Summary: "正在恢复 Codex 会话"}
		err = appserver.VerifyOwner(binding, dataRoot)
		if err == nil {
			err = appserver.Verify(ctx, binding)
		}
		if err != nil {
			observation.InstanceStatus = "LOST"
			observation.ConnectionState = "RUNTIME_SESSION_LOST"
			observation.Summary = "Codex 实时会话已丢失，原问题无法继续响应"
		} else {
			observation.InstanceStatus = "ALIVE"
			attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
			if value.ExecutionProposalID != "" {
				_, err = proposals.RecoverAppServerExecution(attempt, value.ProjectID, value.ExecutionProposalID, id, binding)
			} else {
				_, err = supervisor.RecoverAppServerRun(attempt, id, binding)
			}
			cancel()
			if err == nil {
				continue
			} // executor persists protocol observations
			var protocolObservation *execution.AppServerObservationError
			if errors.As(err, &protocolObservation) {
				observation = protocolObservation.Observation
			}
			observation.ConnectionState = "UNKNOWN"
			observation.Summary = "Codex 会话状态无法确认，原请求尚未恢复"
		}
		observation.FailureReason = err.Error()
		data, _ := json.Marshal(observation)
		if err := store.RecordAppServerRecoveryFailure(ctx, id, data); err != nil {
			return fmt.Errorf("record App Server recovery failure: %w", err)
		}
	}
	return nil
}
