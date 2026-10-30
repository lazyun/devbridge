package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type sessionInitializationService interface {
	InitializeSession(context.Context, aiprojectselection.ClientBindingReference, string, string) (aicontext.InitializationResult, error)
	BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error)
}

// The new protocol needs only the established caller/data-instance scope.
// It does not depend on host metadata, a connection identity or a selection handle.
func sessionReferenceForRequest(request *sdkmcp.CallToolRequest) (aiprojectselection.ClientBindingReference, error) {
	ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
	if request != nil && request.Extra != nil && request.Extra.TokenInfo != nil {
		principal := request.Extra.TokenInfo.UserID
		if len(principal) > 4096 {
			return ref, errors.New("session caller scope unavailable")
		}
		if strings.TrimSpace(principal) != "" {
			ref.CallerScope = "authenticated:" + bindingDigest(principal)
		}
	}
	return ref, nil
}

func withSessionRequest(ctx context.Context, request *sdkmcp.CallToolRequest) (context.Context, error) {
	ref, err := sessionReferenceForRequest(request)
	if err != nil {
		return ctx, err
	}
	// Re-entry must agree with the same authenticated request. Never replace an
	// existing caller or binding reference with whichever identity passes later.
	if scope, present := aisession.CallerScopeFromContext(ctx); present && scope != ref.CallerScope {
		return ctx, aisession.ErrScopeMismatch
	}
	if existing, present := ctx.Value(clientBindingReferenceKey{}).(aiprojectselection.ClientBindingReference); present && existing.CallerScope != ref.CallerScope {
		return ctx, aisession.ErrScopeMismatch
	}
	ctx = aisession.WithCallerScope(ctx, ref.CallerScope)
	return context.WithValue(ctx, clientBindingReferenceKey{}, ref), nil
}

// SessionScopeMiddleware validates before dispatch, including project commands
// whose storage query does not pass through the Session Store. tools/list and
// resources are never allowed to initialize or read business state here.
func SessionScopeMiddleware(service BridgeService) sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			call, ok := request.(*sdkmcp.CallToolRequest)
			if !ok || call == nil || call.Params == nil {
				return nil, errors.New("invalid tool request")
			}
			ctx, err := withSessionRequest(ctx, call)
			if err != nil {
				if confirmationTool(call.Params.Name) {
					return confirmationAdmissionFailure(err)
				}
				return nil, publicToolError(err)
			}
			// Confirmation owns exact existing Session/caller admission, including
			// unbound project setup. It never initializes a substitute Session.
			if confirmationTool(call.Params.Name) {
				return next(ctx, method, request)
			}
			switch call.Params.Name {
			case "session_initialize", "project_open", "project_selection_show", "session_binding_get", "panel_show", "dev_bridge_panel_get":
				// These tools own their precise UNBOUND or historical-read contract.
				return next(ctx, method, request)
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(call.Params.Arguments, &args); err != nil {
				return nil, errors.New("invalid tool arguments")
			}
			if call.Params.Name == "dev_bridge_card_get" && len(args) == 2 {
				var mode, sessionID string
				if json.Unmarshal(args["readMode"], &mode) == nil && mode == "SESSION" && json.Unmarshal(args["aiSessionId"], &sessionID) == nil && validSessionInputID(sessionID) {
					// This exact branch can read UNBOUND identity; the handler
					// fixes requestId to empty and reuses this caller context.
					return next(ctx, method, request)
				}
			}
			if raw, present := args["aiSessionId"]; present {
				var sessionID string
				if json.Unmarshal(raw, &sessionID) != nil || !validSessionInputID(sessionID) {
					return nil, errors.New("valid aiSessionId is required")
				}
				provider, ok := service.(sessionInitializationService)
				if !ok {
					return nil, errors.New("Session admission unavailable")
				}
				value, err := provider.InitializeSession(ctx, sessionReferenceFromContext(ctx), "", sessionID)
				if err != nil {
					return nil, publicToolError(err)
				}
				if value.AISessionID != sessionID || value.State != "PROJECT_BOUND" || value.BoundProjectID == nil || *value.BoundProjectID == "" {
					return nil, publicToolError(aisession.ErrProjectUnbound)
				}
			}
			return next(ctx, method, request)
		}
	}
}

func sessionReferenceFromContext(ctx context.Context) aiprojectselection.ClientBindingReference {
	ref := bindingReferenceFromContext(ctx, "")
	ref.HostReference = ""
	return ref
}

func validSessionInputID(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsRune(value, 0) && strings.IndexFunc(value, unicode.IsSpace) < 0
}

type SessionInitializeInput struct {
	RequestID   string `json:"requestId,omitempty"`
	AISessionID string `json:"aiSessionId,omitempty"`
}

type sessionIdentityWorkflow struct {
	State string `json:"state"`
}

type SessionInitializeOutput struct {
	Result           aicontext.InitializationResult `json:"result"`
	Context          SessionBindingContext          `json:"context"`
	Workflow         sessionIdentityWorkflow        `json:"workflow"`
	PresentationHint SurfacePresentationHint        `json:"presentationHint"`
}

const SessionInitializeInputSchema = `{"type":"object","properties":{"requestId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"},"aiSessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"}},"oneOf":[{"required":["requestId"],"not":{"required":["aiSessionId"]}},{"required":["aiSessionId"],"not":{"required":["requestId"]}}],"additionalProperties":false}`
const sessionProjectIdentitySchema = `{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"}},"required":["id","name"],"additionalProperties":false}`
const sessionRequestReceiptSchema = `{"type":"object","properties":{"requestId":{"type":"string"},"operationKind":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":["string","null"]},"replayed":{"type":"boolean"}},"required":["requestId","operationKind","aiSessionId","projectId","replayed"],"additionalProperties":false}`
const sessionInitializationResultProperties = `"availableProjectsComplete":{"type":"boolean"},"aiSessionId":{"type":"string"},"state":{"type":"string","enum":["PROJECT_SELECTION_REQUIRED","PROJECT_BOUND"]},"boundProjectId":{"type":["string","null"]},"project":` + sessionProjectIdentitySchema + `,"availableProjects":{"type":"array","maxItems":100,"items":` + sessionProjectIdentitySchema + `},"availableProjectsStatus":{"type":"string","enum":["AVAILABLE","UNAVAILABLE","NOT_APPLICABLE"]},"availableProjectsReadError":{"type":"string"},"receipt":` + sessionRequestReceiptSchema
const sessionInitializationResultSchema = `{"type":"object","properties":{` + sessionInitializationResultProperties + `},"required":["aiSessionId","state","boundProjectId","availableProjects","availableProjectsStatus"],"additionalProperties":false}`
const sessionIdentityContextSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string"},"projectId":{"type":"string"}},"required":["aiSessionId"],"additionalProperties":false}`
const sessionIdentityWorkflowSchema = `{"type":"object","properties":{"state":{"type":"string"}},"required":["state"],"additionalProperties":false}`
const SessionInitializeOutputSchema = `{"type":"object","properties":{"result":` + sessionInitializationResultSchema + `,"context":` + sessionIdentityContextSchema + `,"workflow":` + sessionIdentityWorkflowSchema + `,"presentationHint":` + surfacePresentationHintSchema + `},"required":["result","context","workflow","presentationHint"],"additionalProperties":false}`

func (s *StaticService) InitializeSession(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID, sessionID string) (aicontext.InitializationResult, error) {
	if s.context == nil {
		return aicontext.InitializationResult{}, errors.New("AI context unavailable")
	}
	return s.context.InitializeSession(ctx, ref, requestID, sessionID)
}

func (s *StaticService) BindProject(ctx context.Context, ref aiprojectselection.ClientBindingReference, sessionID, projectID, requestID string) (aicontext.ProjectBindingResult, error) {
	if s.context == nil {
		return aicontext.ProjectBindingResult{}, errors.New("AI context unavailable")
	}
	return s.context.BindProject(ctx, ref, sessionID, projectID, requestID)
}

// SessionInitializeCore returns the committed identity independently of bounded
// inventory availability. The aiSessionId branch invokes only the Core read mode.
func SessionInitializeCore(ctx context.Context, service BridgeService, in SessionInitializeInput) (SessionInitializeOutput, error) {
	if (in.RequestID == "") == (in.AISessionID == "") || in.RequestID != "" && !validSessionInputID(in.RequestID) || in.AISessionID != "" && !validSessionInputID(in.AISessionID) {
		return SessionInitializeOutput{}, errors.New("session_initialize requires exactly one valid requestId or aiSessionId")
	}
	provider, ok := service.(sessionInitializationService)
	if !ok {
		return SessionInitializeOutput{}, errors.New("Session initialization unavailable")
	}
	value, err := provider.InitializeSession(ctx, sessionReferenceFromContext(ctx), in.RequestID, in.AISessionID)
	if err != nil {
		return SessionInitializeOutput{}, publicToolError(err)
	}
	out := SessionInitializeOutput{Result: value, Context: SessionBindingContext{AISessionID: value.AISessionID}, Workflow: sessionIdentityWorkflow{State: value.State}, PresentationHint: surfaceHint()}
	if value.BoundProjectID != nil {
		out.Context.ProjectID = *value.BoundProjectID
	}
	return out, nil
}

func registerSessionInitialize(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "session_initialize", Description: "SILENT Session-first initialization. Exactly one input mode: {requestId} creates one real durable UNBOUND AISession with an idempotent receipt; {aiSessionId} only reads that same Session's current binding and never creates or prepares anything. For a new initialization intent generate a fresh random requestId, and reuse the exact original requestId after a lost creation response. The model must obtain this actual aiSessionId before showing a Project picker; the Card must not initialize secretly. PROJECT_SELECTION_REQUIRED has boundProjectId=null and bounded availableProjects; candidate inventory is not a selected Project and its read error does not erase the Session. PROJECT_BOUND reports the actual Project: use workspace_get with this same Session before the user's authorized next steps, without another project_open or any handle exchange. Missing/invalid Sessions fail; never choose recent Sessions or create a replacement implicitly. Creation and binding do not approve engineering work. Tools/list does not initialize anything.", InputSchema: json.RawMessage(SessionInitializeInputSchema), OutputSchema: json.RawMessage(SessionInitializeOutputSchema), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}, "emission": "SILENT"}, Annotations: toolAnnotations("Initialize or read Session", false, true)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in SessionInitializeInput) (*sdkmcp.CallToolResult, SessionInitializeOutput, error) {
		ctx, err := withSessionRequest(ctx, request)
		if err != nil {
			return nil, SessionInitializeOutput{}, publicToolError(err)
		}
		out, err := SessionInitializeCore(ctx, service, in)
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, out, err
	})
}
