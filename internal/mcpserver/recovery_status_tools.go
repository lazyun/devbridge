package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type recoveryStatusAPI interface {
	PrepareRecoveryStatusForSession(context.Context, string, recoverystatus.Spec) (controlapi.RecoveryStatusResult, error)
	StartRecoveryStatusForSession(context.Context, string, controlapi.RecoveryStatusCommand) (controlapi.RecoveryStatusResult, error)
	ReadRecoveryStatusForSession(context.Context, string, string) (controlapi.RecoveryStatusResult, error)
	ResolveRecoveryStatusForSession(context.Context, string, controlapi.RecoveryStatusResolveCommand) (controlapi.RecoveryStatusResult, error)
}
type inspectionRecoveryContinuationAPI interface {
	ContinueInspectionRecoveryForSession(context.Context, string, probeexecution.InspectionContinuationInput) (contextprobe.Probe, error)
}
type recoveryPrepareInput struct {
	AISessionID string              `json:"aiSessionId"`
	Spec        recoverystatus.Spec `json:"spec"`
}
type recoveryStartInput struct {
	AISessionID string                           `json:"aiSessionId"`
	Command     controlapi.RecoveryStatusCommand `json:"command"`
}
type recoveryReadInput struct {
	AISessionID string `json:"aiSessionId"`
	ID          string `json:"id"`
}
type recoveryResolveInput struct {
	AISessionID string                                  `json:"aiSessionId"`
	Command     controlapi.RecoveryStatusResolveCommand `json:"command"`
}
type inspectionRecoveryContinueInput struct {
	AISessionID string                                     `json:"aiSessionId"`
	Command     probeexecution.InspectionContinuationInput `json:"command"`
}
type recoveryPurposeAPI interface {
	RepairResolvedRecoveryPurposeForSession(context.Context, string, recoverystatus.RepairPurposeInput) (recoverystatus.PurposeRepairReceipt, error)
}
type recoveryPurposeRepairInput struct {
	AISessionID string                            `json:"aiSessionId"`
	Command     recoverystatus.RepairPurposeInput `json:"command"`
}

func recoveryAPI(service BridgeService) (recoveryStatusAPI, error) {
	s, ok := service.(recoveryStatusAPI)
	if !ok {
		return nil, errors.New("recovery status unavailable")
	}
	return s, nil
}
func RegisterRecoveryStatusTools(server *sdkmcp.Server, service BridgeService) {
	const instruction = "Core never authors or appends recovery text. Supply an exact caller-authored read-only status instruction. Recovery is a new Turn in the same original Thread, never a response to an old question. Old responseDisposition UNKNOWN remains UNKNOWN. Any new CODEX_REQUEST requires the user's explicit answer; never answer it yourself. "
	output := json.RawMessage(runtimeDTOSchema[controlapi.RecoveryStatusResult]())
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "recovery_status_prepare", Description: instruction + "Prepare an exact source-bound recovery specification and checksum without starting a model. Copy the returned startCommand to start. Actor is server-derived; omit actorId. Missing payloadHash is computed over the unchanged instruction during preparation.", InputSchema: json.RawMessage(recoveryPrepareSchema()), OutputSchema: output, Annotations: stateChangingAnnotations("Prepare engineering recovery", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recoveryPrepareInput) (*sdkmcp.CallToolResult, controlapi.RecoveryStatusResult, error) {
		api, err := recoveryAPI(service)
		if err != nil {
			return nil, controlapi.RecoveryStatusResult{}, err
		}
		out, err := api.PrepareRecoveryStatusForSession(ctx, in.AISessionID, in.Spec)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "recovery_status_start", Description: instruction + "Start only the exact prepared command. Core revalidates idle original Thread, terminal source Turn, no pending request, workset, roots, revision and writer authority. Exact replay never creates another model Turn; uncertain dispatch remains closed.", InputSchema: json.RawMessage(runtimeDTOSchema[recoveryStartInput]()), OutputSchema: output, Annotations: stateChangingAnnotations("Start engineering recovery", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recoveryStartInput) (*sdkmcp.CallToolResult, controlapi.RecoveryStatusResult, error) {
		api, err := recoveryAPI(service)
		if err != nil {
			return nil, controlapi.RecoveryStatusResult{}, err
		}
		out, err := api.StartRecoveryStatusForSession(ctx, in.AISessionID, in.Command)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "recovery_status_read", Description: "Pure scoped read of durable recovery report, independent objective assessment and Core projection. Does not start/resume a Turn, resolve workflow, or send any answer. Copy resolveCommand only when Core offers RESOLVE_RECOVERY_STATUS.", InputSchema: json.RawMessage(runtimeDTOSchema[recoveryReadInput]()), OutputSchema: output, Annotations: toolAnnotations("Read engineering recovery", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recoveryReadInput) (*sdkmcp.CallToolResult, controlapi.RecoveryStatusResult, error) {
		api, err := recoveryAPI(service)
		if err != nil {
			return nil, controlapi.RecoveryStatusResult{}, err
		}
		out, err := api.ReadRecoveryStatusForSession(ctx, in.AISessionID, in.ID)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "recovery_status_resolve", Description: "Consume an immutable Core Assessment through exact canonical recovery resolution. Copy resolveCommand from read. Never choose an engineering disposition, supply factual admission booleans, or infer answer consumption. This does not approve a Review or accept a Task. Partial continuation still needs a new exact caller instruction; resolved RECOVERED_PARTIAL may use eligible RETRY_IMPLEMENTATION on the same approved revision. Changed scope and REVISION_REQUIRED require the exact RECOVERY_PURPOSE confirmation, not an ordinary REQUEST_CHANGES or blind retry. SCOPE_CONFLICT alone never authorizes a new Review; request supplemental Review only when Core offers its same-Task eligibility. CONFLICT or INSUFFICIENT facts cannot authorize continuation. Preserve all changed files.", InputSchema: json.RawMessage(runtimeDTOSchema[recoveryResolveInput]()), OutputSchema: output, Annotations: stateChangingAnnotations("Resolve engineering recovery", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recoveryResolveInput) (*sdkmcp.CallToolResult, controlapi.RecoveryStatusResult, error) {
		api, err := recoveryAPI(service)
		if err != nil {
			return nil, controlapi.RecoveryStatusResult{}, err
		}
		out, err := api.ResolveRecoveryStatusForSession(ctx, in.AISessionID, in.Command)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "recovery_purpose_repair", Description: "Repair only a legacy resolved revision-required Recovery lacking an exact intent link. Submit a complete typed proposal; Core revalidates the original source and writes a canonical intent/link plus immutable repair receipt.", InputSchema: json.RawMessage(runtimeDTOSchema[recoveryPurposeRepairInput]()), OutputSchema: json.RawMessage(runtimeDTOSchema[recoverystatus.PurposeRepairReceipt]()), Annotations: stateChangingAnnotations("Repair Recovery purpose", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in recoveryPurposeRepairInput) (*sdkmcp.CallToolResult, recoverystatus.PurposeRepairReceipt, error) {
		api, ok := service.(recoveryPurposeAPI)
		if !ok {
			return nil, recoverystatus.PurposeRepairReceipt{}, errors.New("Recovery purpose repair unavailable")
		}
		out, err := api.RepairResolvedRecoveryPurposeForSession(ctx, in.AISessionID, in.Command)
		return nil, out, publicToolError(err)
	})
	continuationOutput := json.RawMessage(runtimeDTOSchema[contextprobe.Probe]())
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_recovery_continue", Description: instruction + "After a resolved RECOVERED_PARTIAL Project Inspection assessment, create the exact canonical continuation on the same operation, conversation, App Server, Thread, Workspace, RuntimeWorkset and target set. Supply the caller-authored instruction unchanged plus the stable request and payload hash. Scope expansion must use a formally confirmed successor Inspection instead.", InputSchema: json.RawMessage(runtimeDTOSchema[inspectionRecoveryContinueInput]()), OutputSchema: continuationOutput, Annotations: stateChangingAnnotations("Continue recovered Project Inspection", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in inspectionRecoveryContinueInput) (*sdkmcp.CallToolResult, contextprobe.Probe, error) {
		api, ok := service.(inspectionRecoveryContinuationAPI)
		if !ok {
			return nil, contextprobe.Probe{}, errors.New("Inspection recovery continuation unavailable")
		}
		out, err := api.ContinueInspectionRecoveryForSession(ctx, in.AISessionID, in.Command)
		return nil, out, publicToolError(err)
	})
}
func (s *StaticService) PrepareRecoveryStatusForSession(ctx context.Context, id string, in recoverystatus.Spec) (controlapi.RecoveryStatusResult, error) {
	if s.control == nil {
		return controlapi.RecoveryStatusResult{}, errors.New("recovery unavailable")
	}
	return s.control.PrepareRecoveryStatusForSession(ctx, id, in)
}
func (s *StaticService) StartRecoveryStatusForSession(ctx context.Context, id string, in controlapi.RecoveryStatusCommand) (controlapi.RecoveryStatusResult, error) {
	if s.control == nil {
		return controlapi.RecoveryStatusResult{}, errors.New("recovery unavailable")
	}
	return s.control.StartRecoveryStatusForSession(ctx, id, in)
}
func (s *StaticService) ReadRecoveryStatusForSession(ctx context.Context, id, record string) (controlapi.RecoveryStatusResult, error) {
	if s.control == nil {
		return controlapi.RecoveryStatusResult{}, errors.New("recovery unavailable")
	}
	return s.control.ReadRecoveryStatusForSession(ctx, id, record)
}
func (s *StaticService) ResolveRecoveryStatusForSession(ctx context.Context, id string, in controlapi.RecoveryStatusResolveCommand) (controlapi.RecoveryStatusResult, error) {
	if s.control == nil {
		return controlapi.RecoveryStatusResult{}, errors.New("recovery unavailable")
	}
	return s.control.ResolveRecoveryStatusForSession(ctx, id, in)
}
func (s *StaticService) RepairResolvedRecoveryPurposeForSession(ctx context.Context, session string, in recoverystatus.RepairPurposeInput) (recoverystatus.PurposeRepairReceipt, error) {
	if s.control == nil {
		return recoverystatus.PurposeRepairReceipt{}, errors.New("Recovery purpose unavailable")
	}
	return s.control.RepairResolvedRecoveryPurposeForSession(ctx, session, in)
}
func (s *StaticService) ContinueInspectionRecoveryForSession(ctx context.Context, id string, in probeexecution.InspectionContinuationInput) (contextprobe.Probe, error) {
	if s.control == nil {
		return contextprobe.Probe{}, errors.New("recovery unavailable")
	}
	return s.control.ContinueInspectionRecoveryForSession(ctx, id, in)
}
func (s *StaticService) RecoveryStatusForSourceForSession(ctx context.Context, id, run string) (recoverystatus.Projection, error) {
	if s.control == nil {
		return recoverystatus.Projection{}, errors.New("recovery unavailable")
	}
	return s.control.RecoveryStatusForSourceForSession(ctx, id, run)
}

func recoveryPrepareSchema() string {
	var schema map[string]any
	if err := json.Unmarshal([]byte(runtimeDTOSchema[recoveryPrepareInput]()), &schema); err != nil {
		panic(err)
	}
	spec := schema["properties"].(map[string]any)["spec"].(map[string]any)
	delete(spec["properties"].(map[string]any), "actorId")
	required := []any{}
	for _, key := range spec["required"].([]any) {
		if key != "actorId" && key != "payloadHash" {
			required = append(required, key)
		}
	}
	spec["required"] = required
	raw, _ := json.Marshal(schema)
	return string(raw)
}
