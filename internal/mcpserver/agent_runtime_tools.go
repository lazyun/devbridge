package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"reflect"

	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
)

type agentRuntimeService interface {
	AgentRuntimeStateForSession(context.Context, string, string) (agentruntime.Snapshot, error)
	ResolveAgentRequestForSession(context.Context, string, agentruntime.Resolution) (agentruntime.Snapshot, error)
}

const AgentRuntimeStateDescription = "Read official Codex plan, item, activity, warnings and exact pending requests for a Run. Telemetry does not decide workflow status."

const ExplicitUserDecisionDescription = "CODEX_REQUEST requires an explicit user answer from the current conversation. Do not infer, recommend, choose defaults, author, translate or supplement an answer. Preserve exact user response. If no explicit answer exists, do not call this tool. A drafted answer requires user confirmation of its exact final text. Bind one exact runtimeRequestId; ordering or latest pending request is never authority. "

const CodexRequestResolveDescription = ExplicitUserDecisionDescription + "Dispatch only an existing immutable UserDecision for one exact CODEX_REQUEST. Copy identity and decision reference from operation_get include RUNTIME_REQUESTS or the canonical submission result. Never provide raw semantic response JSON. RESPONDING is not server-confirmed; RESPONSE_DISPOSITION_UNKNOWN forbids ordinary response submission even if the user requests resend. Use runtime_request_recovery for waiting or canonical cancellation. 你的回答已经记录。Codex 可能已经收到该回答，但当前协议无法确认。Dev Bridge 不会自动重发。"

const agentRuntimeItemMetadataOutputSchema = `{"type":["object","array","null"],"additionalProperties":true,"description":"Official App Server JSON; validated against the installed generated contract when resolving."}`

// All supported request Params and responses are objects in agentruntime/protocol.json.
// Method-specific fields remain authoritative in the runtime validator.
const agentRuntimeRequestPayloadOutputSchema = `{"type":"object","additionalProperties":true,"description":"Official App Server JSON; validated against the installed generated contract when resolving."}`

// Infer the closed DTO contract so new durable identity fields cannot silently
// disappear at an adapter boundary. Only official backend JSON is open.
var agentRuntimeOutputSchema = runtimeDTOSchema[agentruntime.Snapshot]()
var agentRequestInputSchema = durableResolutionInputSchema()

func durableResolutionInputSchema() string {
	value := runtimeDTOSchema[struct {
		AISessionID string                  `json:"aiSessionId"`
		Resolution  agentruntime.Resolution `json:"resolution"`
	}]()
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(value), &schema); err != nil {
		panic(err)
	}
	resolution := schema.Properties["resolution"]
	delete(resolution.Properties, "response")
	var required []string
	for _, name := range resolution.Required {
		if name != "response" {
			required = append(required, name)
		}
	}
	resolution.Required = append(required, "runtimeRequestId", "userDecisionId", "expectedState", "payloadSHA256", "runtimeEpoch", "operationKind", "operationId")
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func runtimeDTOSchema[T any]() string {
	rawJSON := &jsonschema.Schema{Types: []string{"object", "array", "string", "number", "boolean", "null"}, Description: "Exact official backend JSON; never model-authored semantic response."}
	types := map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): rawJSON}
	item, itemErr := jsonschema.For[agentruntime.Item](&jsonschema.ForOptions{TypeSchemas: types})
	if itemErr != nil {
		panic(itemErr)
	}
	var metadata jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeItemMetadataOutputSchema), &metadata); err != nil {
		panic(err)
	}
	item.Properties["metadata"] = &metadata
	var itemRequired []string
	for _, name := range item.Required {
		if name != "metadata" {
			itemRequired = append(itemRequired, name)
		}
	}
	item.Required = itemRequired
	types[reflect.TypeFor[agentruntime.Item]()] = item
	request, err := jsonschema.For[agentruntime.Request](&jsonschema.ForOptions{TypeSchemas: types})
	if err != nil {
		panic(err)
	}
	request.Properties["requestId"] = &jsonschema.Schema{Types: []string{"string", "integer"}}
	request.Properties["payload"] = &jsonschema.Schema{Type: "object"}
	request.Properties["resolution"] = &jsonschema.Schema{Type: "object"}
	request.Properties["availableResponses"] = &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "object"}}
	types[reflect.TypeFor[agentruntime.Request]()] = request
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: types})
	if err != nil {
		panic(err)
	}
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type AgentUserDecisionInput struct {
	AISessionID string                         `json:"aiSessionId"`
	Decision    agentruntime.UserDecisionInput `json:"decision"`
}

var AgentUserDecisionInputSchema = userDecisionInputSchema()

func userDecisionInputSchema() string {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(runtimeDTOSchema[AgentUserDecisionInput]()), &schema); err != nil {
		panic(err)
	}
	decision := schema.Properties["decision"]
	delete(decision.Properties, "submittedBy")
	decision.Properties["action"].Enum = []any{"RESPOND", "STOP_AND_REVISE", "CANCEL_OPERATION"}
	decision.Required = append(decision.Required, "runtimeEpoch", "operationKind", "operationId")
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var AgentUserDecisionOutputSchema = runtimeDTOSchema[controlapi.AgentUserDecisionResult]()

func AgentUserDecisionCore(ctx context.Context, service BridgeService, in AgentUserDecisionInput) (controlapi.AgentUserDecisionResult, error) {
	p, ok := service.(interface {
		SubmitAgentUserDecisionForSession(context.Context, string, agentruntime.UserDecisionInput) (controlapi.AgentUserDecisionResult, error)
	})
	if !ok {
		return controlapi.AgentUserDecisionResult{}, errors.New("user runtime decision unavailable")
	}
	if in.AISessionID == "" || in.Decision.RuntimeRequestID == "" || in.Decision.SubmissionRequestID == "" || in.Decision.ExpectedState == "" || in.Decision.PayloadSHA256 == "" || in.Decision.SubmittedBy != "" {
		return controlapi.AgentUserDecisionResult{}, errors.New("exact request, expected state, submission identity and payload hash required; actor is server-derived")
	}
	return p.SubmitAgentUserDecisionForSession(ctx, in.AISessionID, in.Decision)
}

func (s *StaticService) SubmitAgentUserDecisionForSession(ctx context.Context, sessionID string, in agentruntime.UserDecisionInput) (controlapi.AgentUserDecisionResult, error) {
	if s.control == nil {
		return controlapi.AgentUserDecisionResult{}, errors.New("user runtime decision unavailable")
	}
	return s.control.SubmitAgentUserDecisionForSession(ctx, sessionID, in)
}

// Hash preparation is a pure read. It does not record a UserDecision, queue a
// backend response, or imply that the user has approved a draft.
var AgentUserDecisionPrepareHashInputSchema = func() string {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(AgentUserDecisionInputSchema), &schema); err != nil {
		panic(err)
	}
	decision := schema.Properties["decision"]
	var required []string
	for _, name := range decision.Required {
		if name != "payloadSHA256" {
			required = append(required, name)
		}
	}
	decision.Required = required
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}()

type AgentUserDecisionPreparedHash struct {
	Decision agentruntime.UserDecisionInput `json:"decision"`
	Recorded bool                           `json:"recorded"`
}

func AgentUserDecisionPrepareHashCore(ctx context.Context, service BridgeService, in AgentUserDecisionInput) (AgentUserDecisionPreparedHash, error) {
	out := AgentUserDecisionPreparedHash{}
	if in.AISessionID == "" || in.Decision.SubmittedBy != "" || in.Decision.SubmissionRequestID == "" {
		return out, errors.New("exact scoped user decision identity required")
	}
	reader, ok := service.(agentRuntimeService)
	if !ok {
		return out, errors.New("agent runtime unavailable")
	}
	snapshot, err := reader.AgentRuntimeStateForSession(ctx, in.AISessionID, in.Decision.RunID)
	if err != nil {
		return out, err
	}
	for _, request := range snapshot.Requests {
		if request.ID != in.Decision.RuntimeRequestID {
			continue
		}
		if agentruntime.ResponseDisposition(request) == "UNKNOWN" {
			return out, controlapi.MapError(agentruntime.ErrResponseDispositionUnknown)
		}
		if request.Kind != "CODEX_REQUEST" || request.Identity != in.Decision.Identity || request.State != in.Decision.ExpectedState {
			return out, errors.New("exact current CODEX_REQUEST identity and state required")
		}
		in.Decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in.Decision)
		out.Decision = in.Decision
		return out, nil
	}
	return out, errors.New("exact runtime request not found; latest is not authority")
}

type AgentRequestRecoveryInput struct {
	AISessionID string                               `json:"aiSessionId"`
	Recovery    controlapi.AgentRequestRecoveryInput `json:"recovery"`
}

var AgentRequestRecoveryInputSchema = func() string {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(runtimeDTOSchema[AgentRequestRecoveryInput]()), &schema); err != nil {
		panic(err)
	}
	schema.Properties["recovery"].Properties["action"].Enum = []any{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}()

func AgentRequestRecoveryCore(ctx context.Context, service BridgeService, in AgentRequestRecoveryInput) (controlapi.AgentRequestRecoveryResult, error) {
	p, ok := service.(interface {
		RecoverAgentRequestForSession(context.Context, string, controlapi.AgentRequestRecoveryInput) (controlapi.AgentRequestRecoveryResult, error)
	})
	if !ok {
		return controlapi.AgentRequestRecoveryResult{}, errors.New("runtime recovery unavailable")
	}
	return p.RecoverAgentRequestForSession(ctx, in.AISessionID, in.Recovery)
}
func (s *StaticService) RecoverAgentRequestForSession(ctx context.Context, sessionID string, in controlapi.AgentRequestRecoveryInput) (controlapi.AgentRequestRecoveryResult, error) {
	if s.control == nil {
		return controlapi.AgentRequestRecoveryResult{}, errors.New("runtime recovery unavailable")
	}
	return s.control.RecoverAgentRequestForSession(ctx, sessionID, in)
}
