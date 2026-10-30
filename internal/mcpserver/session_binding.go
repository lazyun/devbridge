package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Binding references locate existing Sessions; they never authorize Project work.
// The concrete store supplies data-instance isolation. Authenticated deployments
// additionally partition references by the adapter-verified principal.
type sessionBindingService interface {
	SessionBinding(context.Context, aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error)
}

type clientBindingReferenceKey struct{}

// VerifiedHostSessionTokenExtra is an optional authentication-ingress contract.
// A trusted verifier may put the independently verified host conversation value
// in TokenInfo.Extra under this key. Copying an unverified request parameter or
// header into this field does not satisfy the contract. No current loopback
// entrypoint populates it, so host-only association is not enabled there.
const VerifiedHostSessionTokenExtra = "devbridge.verified_host_session"

func bindingDigest(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Only RequestExtra.TokenInfo is supplied by SDK authentication integration.
// UserID alone authenticates the caller, not the caller's host-session metadata.
// Caller-supplied openai/subject, headers, IPs, MCP connections and UserAgent do
// not authenticate a principal. The current unauthenticated loopback deployment
// therefore uses the opaque selection handle, not self-asserted host metadata.
func bindingReferenceForRequest(request *sdkmcp.CallToolRequest, handle string) (aiprojectselection.ClientBindingReference, error) {
	ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local", SelectionHandle: handle}
	if !ref.Valid() {
		return ref, errors.New("invalid selectionHandle")
	}
	if request == nil || request.Extra == nil || request.Extra.TokenInfo == nil || strings.TrimSpace(request.Extra.TokenInfo.UserID) == "" {
		return ref, nil
	}
	principal := request.Extra.TokenInfo.UserID
	if len(principal) > 4096 {
		return ref, errors.New("binding principal unavailable")
	}
	ref.CallerScope = "authenticated:" + bindingDigest(principal)
	verifiedValue, present := request.Extra.TokenInfo.Extra[VerifiedHostSessionTokenExtra]
	if !present {
		return ref, nil
	}
	trustedHost, trusted := verifiedValue.(string)
	if !trusted || trustedHost == "" || len(trustedHost) > 1024 || strings.TrimSpace(trustedHost) != trustedHost || strings.ContainsRune(trustedHost, 0) {
		return ref, errors.New("binding host reference unavailable")
	}
	if request.Params != nil {
		if claimed, present := request.Params.GetMeta()["openai/session"]; present {
			if host, ok := claimed.(string); !ok || host != trustedHost {
				return ref, errors.New("binding host reference conflict")
			}
		}
	}
	ref.HostReference = bindingDigest(principal, trustedHost)
	return ref, nil
}

func withBindingRequest(ctx context.Context, request *sdkmcp.CallToolRequest, handle string) (context.Context, error) {
	ref, err := bindingReferenceForRequest(request, handle)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, clientBindingReferenceKey{}, ref), nil
}

func bindingReferenceFromContext(ctx context.Context, handle string) aiprojectselection.ClientBindingReference {
	ref, ok := ctx.Value(clientBindingReferenceKey{}).(aiprojectselection.ClientBindingReference)
	if !ok {
		ref.CallerScope = "data-instance-local"
	}
	ref.SelectionHandle = handle
	return ref
}

func (s *StaticService) SessionBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error) {
	if s.context == nil {
		return aiprojectselection.BindingResult{}, errors.New("AI context unavailable")
	}
	return s.context.SessionBinding(ctx, ref)
}

type SessionBindingGetInput struct {
	SelectionHandle string `json:"selectionHandle,omitempty"`
}

type SessionBindingContext struct {
	AISessionID string `json:"aiSessionId,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
}

type SessionBindingGetOutput struct {
	Result   aiprojectselection.BindingResult `json:"result"`
	Context  SessionBindingContext            `json:"context"`
	Workflow struct {
		State string `json:"state"`
	} `json:"workflow"`
	PresentationHint SurfacePresentationHint `json:"presentationHint"`
}

const SessionBindingGetInputSchema = `{"type":"object","properties":{"selectionHandle":{"type":"string","minLength":1,"maxLength":128}},"additionalProperties":false}`
const sessionBindingResultSchema = `{"type":"object","properties":{"state":{"type":"string","enum":["BOUND","UNBOUND","STALE","UNAVAILABLE"]},"selectionHandle":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"version":{"type":"integer"},"source":{"type":"string"},"reason":{"type":"string"}},"required":["state","version"],"additionalProperties":false}`
const SessionBindingGetOutputSchema = `{"type":"object","properties":{"result":` + sessionBindingResultSchema + `,"context":{"type":"object","properties":{"aiSessionId":{"type":"string"},"projectId":{"type":"string"}},"additionalProperties":false},"workflow":{"type":"object","properties":{"state":{"type":"string"}},"required":["state"],"additionalProperties":false},"presentationHint":` + surfacePresentationHintSchema + `},"required":["result","context","workflow","presentationHint"],"additionalProperties":false}`

// SessionBindingGetCore performs only the exact binding read. It deliberately
// does not call workspace, pending-decision, capability or project-open helpers.
func SessionBindingGetCore(ctx context.Context, service BridgeService, in SessionBindingGetInput) SessionBindingGetOutput {
	ref := bindingReferenceFromContext(ctx, in.SelectionHandle)
	value := aiprojectselection.BindingResult{State: "UNAVAILABLE", SelectionHandle: ref.SelectionHandle, Reason: "BINDING_READ_UNAVAILABLE"}
	if !ref.Valid() {
		value.SelectionHandle, value.Reason = "", "BINDING_REFERENCE_UNAVAILABLE"
	} else if ref.SelectionHandle == "" && ref.HostReference == "" {
		value.Reason = "EXACT_BINDING_REFERENCE_REQUIRED"
	} else if reader, ok := service.(sessionBindingService); ok {
		result, err := reader.SessionBinding(ctx, ref)
		switch {
		case errors.Is(err, aiprojectselection.ErrBindingConflict):
			value.Reason = "BINDING_REFERENCE_CONFLICT"
		case errors.Is(err, aiprojectselection.ErrBindingInvalid):
			value.Reason = "BINDING_REFERENCE_INVALID"
		case err != nil:
			// A fact read failure remains unavailable, never an empty selection.
		case ref.SelectionHandle != "" && result.SelectionHandle != ref.SelectionHandle:
			value.Reason = "BINDING_REFERENCE_CONFLICT"
		default:
			switch result.State {
			case "BOUND":
				if result.AISessionID != "" && result.ProjectID != "" && result.SelectionHandle != "" && result.Version > 0 && result.Source != "" {
					value = result
				}
			case "UNBOUND", "STALE", "UNAVAILABLE":
				result.AISessionID, result.ProjectID = "", ""
				if result.State == "UNBOUND" {
					result.Reason = "SESSION_INITIALIZATION_REQUIRED"
				}
				value = result
			}
		}
	}
	out := SessionBindingGetOutput{Result: value, PresentationHint: surfaceHint()}
	out.Workflow.State = value.State
	if value.State == "BOUND" {
		out.Context = SessionBindingContext{AISessionID: value.AISessionID, ProjectID: value.ProjectID}
	}
	return out
}

func registerSessionBindingGet(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "session_binding_get", Description: "SILENT historical compatibility read only. An exact old selectionHandle may return its already-BOUND original Session; continue by reading session_initialize({aiSessionId}) with that same ID. This is not the new initialization flow. Old UNBOUND handles require a fresh model-led session_initialize intent and cannot create, bind or exchange themselves for a Session. STALE/UNAVAILABLE is a real blocker. New tools and Cards use the actual aiSessionId directly, never generate handles and never guess recent bindings. This getter writes nothing and preserves historical association identities", InputSchema: json.RawMessage(SessionBindingGetInputSchema), OutputSchema: json.RawMessage(SessionBindingGetOutputSchema), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"model", "app"}}, "emission": "SILENT"}, Annotations: toolAnnotations("Read exact Project binding", true, true)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in SessionBindingGetInput) (*sdkmcp.CallToolResult, SessionBindingGetOutput, error) {
		boundCtx, err := withBindingRequest(ctx, request, in.SelectionHandle)
		if err != nil {
			out := SessionBindingGetOutput{Result: aiprojectselection.BindingResult{State: "UNAVAILABLE", Reason: "BINDING_REFERENCE_UNAVAILABLE"}, PresentationHint: surfaceHint()}
			if handle := in.SelectionHandle; handle != "" && strings.TrimSpace(handle) == handle && len(handle) <= 128 && !strings.ContainsRune(handle, 0) {
				out.Result.SelectionHandle = handle
			}
			out.Workflow.State = out.Result.State
			return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, out, nil
		}
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, SessionBindingGetCore(boundCtx, service, in), nil
	})
}
