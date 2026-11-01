package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// The read envelope records independent observations. Generation and sequence
// belong to the named partition; no response claims an atomic cross-read view.
type SurfaceReadPartition struct {
	Name           string    `json:"name"`
	Generation     string    `json:"generation,omitempty"`
	SourceSequence int64     `json:"sourceSequence"`
	ReadAt         time.Time `json:"readAt"`
	Stale          bool      `json:"stale"`
	ReadError      string    `json:"readError,omitempty"`
}
type SurfaceContext struct {
	AISessionID   string             `json:"aiSessionId"`
	ProjectID     string             `json:"projectId"`
	FeatureAreaID string             `json:"featureAreaId,omitempty"`
	TaskID        string             `json:"taskId,omitempty"`
	Target        *p.OperationTarget `json:"target,omitempty"`
}
type SurfaceWorkflow struct {
	VerificationDecisions *p.VerificationDecisions `json:"verificationDecisions,omitempty"`
	NextStep              *p.TaskNextStep          `json:"nextStep,omitempty"`
	State                 string                   `json:"state"`
	Phase                 string                   `json:"phase,omitempty"`
	Generation            string                   `json:"generation,omitempty"`
	SourceSequence        int64                    `json:"sourceSequence"`
	NextAction            wp.Action                `json:"nextAction"`
	Blockers              []string                 `json:"blockers"`
	PendingDecisions      []p.PendingDecision      `json:"pendingDecisions"`
	Capabilities          []p.Capability           `json:"capabilities"`
	RefreshRequired       bool                     `json:"refreshRequired"`
}
type SurfacePresentationHint struct {
	Required       bool                     `json:"required"`
	Reason         string                   `json:"reason"`
	Tool           string                   `json:"tool"`
	Display        string                   `json:"display"`
	Arguments      *SurfacePresentationArgs `json:"arguments,omitempty"`
	HostVisibility string                   `json:"hostVisibility"`
}

// SurfacePresentationArgs is a bounded, read-only handoff to one of the
// existing presentation tools.  It deliberately excludes presentationRequestId:
// each caller owns the idempotency key for its own presentation intent.
type SurfacePresentationArgs struct {
	AISessionID   string             `json:"aiSessionId"`
	ProjectID     string             `json:"projectId"`
	FeatureAreaID string             `json:"featureAreaId,omitempty"`
	TaskID        string             `json:"taskId,omitempty"`
	DecisionID    string             `json:"decisionId,omitempty"`
	Target        *p.OperationTarget `json:"target,omitempty"`
	ActionTarget  *p.ActionTarget    `json:"actionTarget,omitempty"`
}

func surfaceHint() SurfacePresentationHint {
	return SurfacePresentationHint{Required: false, Reason: "NO_PENDING_INTERACTION", Tool: "workspace_show", Display: "COLLAPSED", HostVisibility: "UNCONFIRMED"}
}

func surfaceHintFor(ctx SurfaceContext, workflow SurfaceWorkflow, unavailable bool) SurfacePresentationHint {
	hint := surfaceHint()
	if unavailable || workflow.RefreshRequired {
		hint.Reason = "READ_UNAVAILABLE"
		return hint
	}
	for _, pending := range workflow.PendingDecisions {
		if pending.Decision == nil && pending.RuntimeRequest == nil {
			continue
		}
		if ctx.TaskID != "" && pending.TaskID != "" && pending.TaskID != ctx.TaskID {
			continue
		}
		if ctx.FeatureAreaID != "" && pending.FeatureAreaID != "" && pending.FeatureAreaID != ctx.FeatureAreaID {
			continue
		}
		if ctx.Target != nil && !pendingMatchesOperation(pending, *ctx.Target) {
			continue
		}
		if pending.Kind == "RUNTIME_REQUEST" && (pending.RuntimeRequest == nil || pending.RuntimeRequest.State != "PENDING" || pending.RuntimeRequest.ItemID == "" || pending.RuntimeRequest.ConnectionID == "") {
			continue
		}
		if pending.Kind == "CODEX_REQUEST" {
			r := pending.RuntimeRequest
			if r == nil || r.Kind != "CODEX_REQUEST" || r.ID == "" || !agentruntime.CompleteDecisionIdentity(r.Identity) || r.State != "WAITING_FOR_USER" || r.UserDecision != nil || r.ProjectID != ctx.ProjectID || ctx.TaskID != "" && r.TaskID != ctx.TaskID || ctx.FeatureAreaID != "" && r.FeatureAreaID != ctx.FeatureAreaID {
				continue
			}
		}
		args := &SurfacePresentationArgs{AISessionID: ctx.AISessionID, ProjectID: ctx.ProjectID, FeatureAreaID: pending.FeatureAreaID, TaskID: pending.TaskID}
		if args.FeatureAreaID == "" {
			args.FeatureAreaID = ctx.FeatureAreaID
		}
		if args.TaskID == "" {
			args.TaskID = ctx.TaskID
		}
		if pending.Decision != nil {
			args.DecisionID = pending.Decision.ID
		}
		if pending.Kind == "CODEX_REQUEST" || pending.Kind == "RUNTIME_REQUEST" || pending.Kind == "RUNTIME_APPROVAL" {
			hint.Reason = "PENDING_RUNTIME_REQUEST"
			for _, option := range pending.Options {
				if option.Available && option.Target != nil {
					copy := *option.Target
					args.ActionTarget = &copy
					break
				}
			}
		} else if pending.Kind == "REVIEW_APPROVAL" || pending.Kind == "TASK_ACCEPTANCE" {
			hint.Reason = "PENDING_USER_DECISION"
		} else {
			continue
		}
		// This is an optional display suggestion, not the decision requirement.
		hint.Required, hint.Arguments = false, args
		return hint
	}
	return hint
}

func pendingMatchesOperation(pending p.PendingDecision, target p.OperationTarget) bool {
	if target.OperationID == "" {
		return false
	}
	switch pending.Kind {
	case "REVIEW_APPROVAL":
		return target.Kind == "REVIEW" && pending.Decision != nil && pending.Decision.ID == "REVIEW_APPROVAL:"+target.OperationID
	case "TASK_ACCEPTANCE":
		return target.Kind == "EXECUTION" && pending.Decision != nil && pending.Decision.ID == "TASK_ACCEPTANCE:"+target.OperationID
	case "CODEX_REQUEST":
		r := pending.RuntimeRequest
		return r != nil && target.RunID != "" && r.RunID == target.RunID && r.OperationKind == target.Kind && r.OperationID == target.OperationID
	case "RUNTIME_REQUEST":
		return target.RunID != "" && pending.RuntimeRequest != nil && target.RunID == pending.RuntimeRequest.Identity.RunID
	case "RUNTIME_APPROVAL":
		for _, option := range pending.Options {
			if option.Target != nil && option.Target.RunID != "" && option.Target.RunID == target.RunID {
				return true
			}
		}
	}
	return false
}
func surfaceWorkflow() SurfaceWorkflow {
	return SurfaceWorkflow{State: "PROJECT_READY", NextAction: wp.Action{Code: "WAIT_FOR_USER_TASK", Message: "Select exact work when needed."}, Blockers: []string{}, PendingDecisions: []p.PendingDecision{}, Capabilities: []p.Capability{}}
}
func readPartition(name string, err error) SurfaceReadPartition {
	v := SurfaceReadPartition{Name: name, ReadAt: time.Now().UTC()}
	if err != nil {
		v.Stale = true
		v.ReadError = publicToolError(err).Error()
	}
	return v
}
func validateSurfaceIncludes(values, allowed []string) error {
	seen := map[string]bool{}
	for _, v := range values {
		found := false
		for _, a := range allowed {
			if a == v {
				found = true
			}
		}
		if !found || seen[v] {
			return fmt.Errorf("invalid or duplicate include %q", v)
		}
		seen[v] = true
	}
	return nil
}
func included(values []string, key string) bool {
	for _, v := range values {
		if v == key {
			return true
		}
	}
	return false
}
func surfaceSession(ctx context.Context, s BridgeService, id string) (aicontext.SessionContext, error) {
	if strings.TrimSpace(id) == "" {
		return aicontext.SessionContext{}, errors.New("aiSessionId is required")
	}
	v, err := s.GetSession(ctx, id)
	if err != nil {
		return v, publicToolError(err)
	}
	if v.Session.ID != id || v.Project.ID == "" || v.Session.ProjectID != v.Project.ID {
		return v, errors.New("Session/Project binding mismatch")
	}
	return v, nil
}

const surfaceSessionProperty = `"aiSessionId":{"type":"string","minLength":1,"maxLength":128}`
const surfaceScopeProperties = `"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128}`
const OperationTargetSchema = `{"type":"object","properties":{"kind":{"type":"string","enum":["REVIEW","EXECUTION","INSPECTION"]},"operationId":{"type":"string","minLength":1,"maxLength":128},"ownerTaskId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","minLength":1,"maxLength":128}},"required":["kind","operationId"],"additionalProperties":false}`
const surfaceActionTargetSchema = `{"type":"object","properties":{"taskActionGeneration":{"type":"integer","minimum":1},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"primaryMemberId":{"type":"string","minLength":1},"approvedWorksetHash":{"type":"string","pattern":"^[a-f0-9]{64}$"},"expectedReviewRequest":` + TaskReviewRequestTargetSchema + `,"expectedUpperVersion":` + UpperVersionSchema + `,"taskDefinitionRevisionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"subjectId":{"type":"string","minLength":1,"maxLength":128},"executionId":{"type":"string","minLength":1,"maxLength":128},"operationKind":{"type":"string","enum":["TASK","REVIEW","EXECUTION","INSPECTION"]},"operationId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","minLength":1,"maxLength":128},"agentConversationId":{"type":"string","minLength":1,"maxLength":128}},"additionalProperties":false}`
const surfacePresentationHintSchema = `{"type":"object","properties":{"required":{"type":"boolean"},"reason":{"type":"string","enum":["NO_PENDING_INTERACTION","PENDING_USER_DECISION","PENDING_RUNTIME_REQUEST","READ_UNAVAILABLE","PRESENTATION_RETURNED"]},"tool":{"type":"string","enum":["panel_show","workspace_show","details_show","project_selection_show"]},"display":{"type":"string","enum":["COLLAPSED","EXPANDED"]},"arguments":{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"target":` + OperationTargetSchema + `,"actionTarget":` + surfaceActionTargetSchema + `},"required":["aiSessionId","projectId"],"additionalProperties":false},"hostVisibility":{"type":"string","const":"UNCONFIRMED"}},"required":["required","reason","tool","display","hostVisibility"],"additionalProperties":false}`
const WorkspaceGetInputSchema = `{"type":"object","properties":{"definitionRevisionId":{"type":"string","minLength":1,"maxLength":128},` + surfaceSessionProperty + `,` + surfaceScopeProperties + `,"include":{"type":"array","maxItems":5,"uniqueItems":true,"items":{"type":"string","enum":["MODULES","BRANCHES","TASK","RUNTIME_CONFIG","NAVIGATION"]}}},"required":["aiSessionId"],"additionalProperties":false}`
const WorkListInputSchema = `{"type":"object","properties":{` + surfaceSessionProperty + `,` + surfaceScopeProperties + `,"kind":{"type":"string","enum":["FEATURE_AREA","TASK","OPERATION"]},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":100000}},"required":["aiSessionId","kind"],"additionalProperties":false}`
const OperationGetInputSchema = `{"type":"object","properties":{` + surfaceSessionProperty + `,"target":` + OperationTargetSchema + `,"include":{"type":"array","maxItems":5,"uniqueItems":true,"items":{"type":"string","enum":["RESULT","EVIDENCE","ACTIVITY","RUNTIME_REQUESTS","SOURCE"]}}},"required":["aiSessionId","target"],"additionalProperties":false}`

// Output contracts derive from the concrete DTOs, with official runtime JSON
// fields retaining their upstream contracts instead of becoming byte arrays.
var WorkspaceGetOutputSchema = surfaceOutputSchema[WorkspaceGetOutput]()
var WorkListOutputSchema = surfaceOutputSchema[WorkListOutput]()
var OperationGetOutputSchema = surfaceOutputSchema[OperationGetOutput]()

func surfaceOutputSchema[T any]() string {
	var runtime jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeOutputSchema), &runtime); err != nil {
		panic(err)
	}
	var coverage jsonschema.Schema
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"state":{"type":"string"},"reasons":{"type":["array","null"],"items":{"type":"string"}}},"required":["state","reasons"],"additionalProperties":false}`), &coverage); err != nil {
		panic(err)
	}
	runtime.Properties["captureCoverage"] = &coverage
	runtime.Required = append(runtime.Required, "captureCoverage")
	var target jsonschema.Schema
	if err := json.Unmarshal([]byte(OperationTargetSchema), &target); err != nil {
		panic(err)
	}
	var hint jsonschema.Schema
	if err := json.Unmarshal([]byte(surfacePresentationHintSchema), &hint); err != nil {
		panic(err)
	}
	types := actionTargetSchemaTypes()
	types[reflect.TypeFor[agentruntime.Snapshot]()] = &runtime
	types[reflect.TypeFor[agentruntime.Request]()] = runtime.Properties["requests"].Items
	types[reflect.TypeFor[p.OperationTarget]()] = &target
	types[reflect.TypeFor[SurfacePresentationHint]()] = &hint
	types[reflect.TypeFor[json.RawMessage]()] = &jsonschema.Schema{Types: []string{"object", "array", "string", "number", "boolean", "null"}, Description: "Bounded source JSON from the exact runtime or approval entity; never an arbitrary command payload."}
	options := &jsonschema.ForOptions{TypeSchemas: types}
	schema, err := jsonschema.For[T](options)
	if err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// Both compact reads and Card/detail projections use the same exact target
// contracts as command inputs. Each inference gets fresh schema trees.
func actionTargetSchemaTypes() map[reflect.Type]*jsonschema.Schema {
	var actionTarget, reviewRequestTarget jsonschema.Schema
	if err := json.Unmarshal([]byte(surfaceActionTargetSchema), &actionTarget); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(TaskReviewRequestTargetSchema), &reviewRequestTarget); err != nil {
		panic(err)
	}
	return map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[p.ActionTarget]():             &actionTarget,
		reflect.TypeFor[wp.TaskReviewRequestTarget](): &reviewRequestTarget,
	}
}

type WorkspaceGetInput struct {
	DefinitionRevisionID string   `json:"definitionRevisionId,omitempty"`
	AISessionID          string   `json:"aiSessionId"`
	FeatureAreaID        string   `json:"featureAreaId,omitempty"`
	TaskID               string   `json:"taskId,omitempty"`
	Include              []string `json:"include,omitempty"`
}

func surfaceTask(t task.Task) p.CompactTaskProjection {
	return p.NewCompactTaskProjection(t)
}

type SurfaceProjectContext struct {
	Status        string `json:"status"`
	Usable        bool   `json:"usable"`
	RefreshStatus string `json:"refreshStatus"`
	Guidance      string `json:"guidance"`
}
type SurfaceModuleFacts struct {
	RepositoryID         string                      `json:"repositoryId"`
	RepositoryName       string                      `json:"repositoryName"`
	Responsibility       string                      `json:"responsibility"`
	ResponsibilitySource module.ResponsibilitySource `json:"responsibilitySource"`
	ProductionRoots      []string                    `json:"productionRoots"`
	Entrypoints          []string                    `json:"entrypoints"`
	RelatedModules       []string                    `json:"relatedModules"`
}
type SurfaceModule struct {
	ModuleOutput
	ProductionFacts []SurfaceModuleFacts `json:"productionFacts"`
}

func surfaceModules(values []module.Module) []SurfaceModule {
	summaries := moduleOutputs(values)
	out := make([]SurfaceModule, 0, len(values))
	for i, v := range values {
		item := SurfaceModule{ModuleOutput: summaries[i], ProductionFacts: []SurfaceModuleFacts{}}
		for _, f := range v.ProductionFacts {
			item.ProductionFacts = append(item.ProductionFacts, SurfaceModuleFacts{RepositoryID: f.RepositoryID, RepositoryName: f.RepositoryName, Responsibility: f.Responsibility, ResponsibilitySource: f.ResponsibilitySource, ProductionRoots: append([]string{}, f.ProductionRoots...), Entrypoints: append([]string{}, f.Entrypoints...), RelatedModules: append([]string{}, f.RelatedModules...)})
		}
		out = append(out, item)
	}
	return out
}

type WorkspaceGetResult struct {
	FeatureAreaProduct  *p.FeatureAreaProductProjection `json:"featureAreaProduct,omitempty"`
	TaskSources         *SurfaceTaskSources             `json:"taskSources,omitempty"`
	ProjectContext      SurfaceProjectContext           `json:"projectContext"`
	RuntimeOptions      *p.TaskOptions                  `json:"runtimeOptions,omitempty"`
	Project             p.Identity                      `json:"project"`
	Development         *p.DevelopmentContext           `json:"development,omitempty"`
	SelectedFeatureArea *p.Identity                     `json:"selectedFeatureArea,omitempty"`
	SelectedTask        *p.CompactTaskProjection        `json:"selectedTask,omitempty"`
	SelectedOperation   *p.SelectedOperation            `json:"selectedOperation,omitempty"`
	Modules             []SurfaceModule                 `json:"modules,omitempty"`
	Branches            *developmentcycle.Context       `json:"branches,omitempty"`
	Task                *TaskOutput                     `json:"task,omitempty"`
	TaskView            *p.TaskView                     `json:"taskView,omitempty"`
	RuntimeConfig       []CodexConfigOutput             `json:"runtimeConfig,omitempty"`
	Navigation          *p.WorkspaceProjection          `json:"navigation,omitempty"`
	Partitions          []SurfaceReadPartition          `json:"partitions"`
	Atomic              bool                            `json:"atomic"`
}
type WorkspaceGetOutput struct {
	Result           WorkspaceGetResult      `json:"result"`
	Context          SurfaceContext          `json:"context"`
	Workflow         SurfaceWorkflow         `json:"workflow"`
	PresentationHint SurfacePresentationHint `json:"presentationHint"`
}

type compactWorkspaceReader interface {
	CompactWorkspaceForSession(context.Context, string, string, string) (p.Projection, error)
}

func (s *StaticService) CompactWorkspaceForSession(ctx context.Context, session, area, task string) (p.Projection, error) {
	if s.control == nil {
		return p.Projection{}, errors.New("workspace read unavailable")
	}
	return s.control.CompactWorkspaceForSession(ctx, session, area, task)
}
func (s *StaticService) WorkspaceRuntimeDecisionsForSession(ctx context.Context, session, runID string) ([]p.PendingDecision, error) {
	if s.control == nil {
		return nil, errors.New("runtime decision read unavailable")
	}
	return s.control.WorkspaceRuntimeDecisionsForSession(ctx, session, runID)
}
func projectSurfaceWorkflow(v p.Projection) SurfaceWorkflow {
	out := surfaceWorkflow()
	out.State = v.CurrentState
	out.Phase = v.CurrentPhase
	out.Generation = v.Generation
	out.SourceSequence = v.SourceSequence
	out.NextAction = v.NextAction
	out.NextStep = v.NextStep
	if v.NextStep != nil && v.NextStep.Code == "RESOLVE_RUNTIME_APPROVAL" {
		out.NextAction = wp.Action{Code: v.NextStep.Code, Required: true, Message: v.NextStep.Summary}
	}
	out.VerificationDecisions = v.VerificationDecisions
	if v.TaskView != nil {
		out.VerificationDecisions = v.TaskView.VerificationDecisions
	}
	out.Blockers = append(out.Blockers, v.Blockers...)
	out.Capabilities = append(out.Capabilities, v.Capabilities...)
	if v.TaskStages != nil {
		out.Phase = v.TaskStages.Phase
	}
	if v.PendingDecision != nil && (v.PendingDecision.Kind == "REVIEW_APPROVAL" || v.PendingDecision.Kind == "TASK_ACCEPTANCE" || v.PendingDecision.Kind == "RUNTIME_APPROVAL") {
		options := []p.Capability{}
		for _, c := range v.Capabilities {
			if c.Target != nil && c.Target.DecisionID == v.PendingDecision.ID {
				options = append(options, c)
			}
		}
		d := *v.PendingDecision
		out.PendingDecisions = append(out.PendingDecisions, p.PendingDecision{Kind: d.Kind, Owner: "TASK", TaskID: v.Details.TaskID, FeatureAreaID: v.Details.FeatureAreaID, SemanticState: v.CurrentState, Decision: &d, Options: options, RequiredInput: []string{"exact target", "requestId"}, CreatedAt: d.CreatedAt, UpdatedAt: p.OptionalDecisionTime(v.UpdatedAt)})
	}
	return out
}
func WorkspaceGetCore(ctx context.Context, s BridgeService, in WorkspaceGetInput) (WorkspaceGetOutput, error) {
	out := WorkspaceGetOutput{Workflow: surfaceWorkflow(), PresentationHint: surfaceHint()}
	if in.DefinitionRevisionID != "" && (in.TaskID == "" || !included(in.Include, "TASK")) {
		return out, errors.New("definitionRevisionId requires exact taskId and include TASK")
	}
	if err := validateSurfaceIncludes(in.Include, []string{"MODULES", "BRANCHES", "TASK", "RUNTIME_CONFIG", "NAVIGATION"}); err != nil {
		return out, err
	}
	session, err := surfaceSession(ctx, s, in.AISessionID)
	if err != nil {
		return out, err
	}
	out.Context = SurfaceContext{AISessionID: in.AISessionID, ProjectID: session.Project.ID, FeatureAreaID: in.FeatureAreaID, TaskID: in.TaskID}
	out.Result.Project = p.Identity{ID: session.Project.ID, Name: session.Project.Name}
	contextState := projectContextProductFor(session.ProjectContext)
	out.Result.ProjectContext = SurfaceProjectContext{Status: contextState.Status, Usable: contextState.Usable, RefreshStatus: contextState.RefreshStatus, Guidance: contextState.Guidance}
	out.Result.Partitions = []SurfaceReadPartition{readPartition("SESSION", nil)}
	var selected *task.Task
	if in.TaskID != "" {
		t, e := s.GetTask(ctx, in.AISessionID, in.TaskID)
		if e != nil {
			return out, publicToolError(e)
		}
		if t.ID != in.TaskID || t.ProjectID != session.Project.ID || (in.FeatureAreaID != "" && in.FeatureAreaID != t.FeatureAreaID) {
			return out, errors.New("FeatureArea/Task mismatch")
		}
		selected = &t
		out.Context.FeatureAreaID = t.FeatureAreaID
		summary := surfaceTask(t)
		out.Result.SelectedTask = &summary
	}
	if in.FeatureAreaID != "" {
		a, e := s.GetFeatureArea(ctx, in.AISessionID, in.FeatureAreaID)
		if e != nil {
			return out, publicToolError(e)
		}
		if a.ID != in.FeatureAreaID || a.ProjectID != session.Project.ID {
			return out, errors.New("FeatureArea/Project mismatch")
		}
	}
	addPartition := func(name string, e error) {
		out.Result.Partitions = append(out.Result.Partitions, readPartition(name, e))
		if e != nil {
			out.Workflow.RefreshRequired = true
		}
	}
	if reader, ok := s.(interface {
		DevelopmentContextPresentationForSession(context.Context, string) (p.DevelopmentContext, error)
	}); ok {
		v, e := reader.DevelopmentContextPresentationForSession(ctx, in.AISessionID)
		addPartition("DEVELOPMENT", e)
		if e == nil {
			if v.Project.ID != session.Project.ID {
				return out, errors.New("Development Project mismatch")
			}
			out.Result.Development = &v
		}
	} else {
		addPartition("DEVELOPMENT", errors.New("development read unavailable"))
	}
	reader, ok := s.(compactWorkspaceReader)
	if !ok {
		addPartition("WORKFLOW", errors.New("compact workspace read unavailable"))
	} else {
		v, e := reader.CompactWorkspaceForSession(ctx, in.AISessionID, out.Context.FeatureAreaID, in.TaskID)
		addPartition("WORKFLOW", e)
		if e == nil {
			if v.FeatureArea.ID != "" {
				product := v.Product
				out.Result.FeatureAreaProduct = &product
			}
			if in.FeatureAreaID == "" && in.TaskID == "" && (v.FeatureArea.ID != "" || v.Details.FeatureAreaID != "" || v.Details.TaskID != "" || v.CurrentOperation != nil) {
				return out, errors.New("Project-only workspace unexpectedly selected work")
			}
			if (in.TaskID != "" && v.Details.TaskID != in.TaskID) || (out.Context.FeatureAreaID != "" && v.FeatureArea.ID != out.Context.FeatureAreaID) {
				return out, errors.New("Workspace exact target mismatch")
			}
			refresh := out.Workflow.RefreshRequired
			out.Workflow = projectSurfaceWorkflow(v)
			out.Workflow.RefreshRequired = refresh
			partition := &out.Result.Partitions[len(out.Result.Partitions)-1]
			partition.Generation = v.Generation
			partition.SourceSequence = v.SourceSequence
			out.Context.FeatureAreaID = v.FeatureArea.ID
			out.Context.TaskID = v.Details.TaskID
			if v.FeatureArea.ID != "" {
				a := v.FeatureArea
				out.Result.SelectedFeatureArea = &a
			}
			if selected == nil && v.Details.TaskID != "" {
				t, e := s.GetTask(ctx, in.AISessionID, v.Details.TaskID)
				addPartition("TASK_SUMMARY", e)
				if e == nil {
					if t.ID != v.Details.TaskID || t.ProjectID != session.Project.ID || t.FeatureAreaID != v.FeatureArea.ID {
						return out, errors.New("Selected Task mismatch")
					}
					selected = &t
					summary := surfaceTask(t)
					out.Result.SelectedTask = &summary
				}
			}
			if v.CurrentOperation != nil {
				op := v.CurrentOperation
				target := p.OperationTarget{Kind: op.Kind, OperationID: op.ID, OwnerTaskID: v.Details.TaskID, RunID: op.RunID}
				out.Context.Target = &target
				out.Result.SelectedOperation = &p.SelectedOperation{Kind: op.Kind, OperationID: op.ID, OwnerTaskID: v.Details.TaskID, RunID: op.RunID, Status: op.Status, UpdatedAt: v.UpdatedAt, ReadOnly: op.Kind == "REVIEW", Target: target}
			}
			if r, ok := s.(interface {
				WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error)
			}); ok {
				decisions, e := r.WorkspaceRuntimeDecisionsForSession(ctx, in.AISessionID, v.Details.RunID)
				addPartition("RUNTIME_DECISIONS", e)
				if e == nil {
					out.Workflow.PendingDecisions = append(out.Workflow.PendingDecisions, decisions...)
				}
			}
		}
	}
	if included(in.Include, "MODULES") {
		v, e := s.ListProjectModuleMap(ctx, in.AISessionID)
		addPartition("MODULES", e)
		if e == nil {
			out.Result.Modules = surfaceModules(v)
		}
	}
	if included(in.Include, "BRANCHES") {
		v, e := DevelopmentBranchListCore(ctx, s, DevelopmentBranchListInput{AISessionID: in.AISessionID})
		addPartition("BRANCHES", e)
		if e == nil {
			out.Result.Branches = &v
		}
	}
	if included(in.Include, "TASK") {
		if selected == nil {
			addPartition("TASK", nil)
		} else {
			sources, e := readSurfaceTaskSources(ctx, s, in.AISessionID, *selected, in.DefinitionRevisionID)
			addPartition("TASK_SOURCES", e)
			if e == nil {
				out.Result.TaskSources = &sources
			}
			v := taskOutput(*selected, true)
			out.Result.Task = &v
			addPartition("TASK", nil)
			if r, ok := s.(taskPresentationReader); ok {
				view, e := r.TaskPresentationForSession(ctx, in.AISessionID, selected.FeatureAreaID, selected.ID)
				addPartition("TASK_VIEW", e)
				if e == nil {
					if view.TaskView == nil || view.TaskView.ID != selected.ID || view.Details.FeatureAreaID != selected.FeatureAreaID {
						return out, errors.New("Task view exact identity mismatch")
					}
					out.Result.TaskView = view.TaskView
					part := &out.Result.Partitions[len(out.Result.Partitions)-1]
					part.Generation, part.SourceSequence = view.Generation, view.SourceSequence
					if view.TaskView.Phase != selected.Phase || view.TaskView.Status != string(selected.Status) {
						addPartition("TASK_VIEW_CONSISTENCY", errors.New("Task changed between independent reads; refresh required"))
					}
				}
			}
		}
	}
	if included(in.Include, "RUNTIME_CONFIG") {
		if provider, ok := s.(interface{ ExecutionAllowlist() codexruntime.Allowlist }); ok {
			v := provider.ExecutionAllowlist()
			out.Result.RuntimeOptions = &p.TaskOptions{Models: v.Models, ReasoningEfforts: v.ReasoningEfforts, ModelConfigs: v.Clone().ModelConfigs, RuntimeOptions: v.RuntimeOptions()}
		}
		v, e := s.ListCodexConfigs(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
		addPartition("RUNTIME_CONFIG", e)
		if e == nil {
			out.Result.RuntimeConfig = codexConfigOutputs(v)
		}
		// A later definition-only start needs a caller-selected fingerprint,
		// not merely a backend display name. Reuse the Task's scoped inventory
		// and continuity rules rather than publishing an unfiltered registry.
		if out.Context.TaskID != "" {
			var optionsErr error
			if reader, ok := s.(taskPresentationReader); ok {
				view, readErr := reader.TaskPresentationForSession(ctx, in.AISessionID, out.Context.FeatureAreaID, out.Context.TaskID)
				optionsErr = readErr
				if optionsErr == nil {
					if view.TaskView == nil || view.TaskView.ID != out.Context.TaskID || view.Details.FeatureAreaID != out.Context.FeatureAreaID || view.TaskOptions == nil {
						optionsErr = errors.New("Task runtime options exact identity mismatch")
					} else {
						out.Result.RuntimeOptions = view.TaskOptions
					}
				}
			} else {
				optionsErr = errors.New("Task runtime options unavailable")
			}
			addPartition("TASK_RUNTIME_OPTIONS", optionsErr)
		}
	}
	if included(in.Include, "NAVIGATION") {
		var v p.WorkspaceProjection
		var e error
		if out.Context.FeatureAreaID == "" && out.Context.TaskID == "" {
			if r, ok := s.(interface {
				ExactDevelopmentWorkspaceForSession(context.Context, string, string, string) (p.WorkspaceProjection, error)
			}); ok {
				v, e = r.ExactDevelopmentWorkspaceForSession(ctx, in.AISessionID, "", "")
				if e == nil && (v.SelectedFeatureAreaID != "" || v.SelectedTaskID != "" || v.SelectedWork != nil || v.Presentation != nil) {
					e = errors.New("Project-only navigation unexpectedly selected work")
				}
			} else {
				e = errors.New("exact Project navigation read unavailable")
			}
		} else if r, ok := s.(interface {
			DevelopmentWorkspaceForSession(context.Context, string, string, string) (p.WorkspaceProjection, error)
		}); ok {
			v, e = r.DevelopmentWorkspaceForSession(ctx, in.AISessionID, out.Context.FeatureAreaID, out.Context.TaskID)
		} else {
			e = errors.New("navigation read unavailable")
		}
		addPartition("NAVIGATION", e)
		if e == nil {
			v.Presentation = nil
			out.Result.Navigation = &v
		}
	}
	if !session.ProjectContext.SnapshotUsable() {
		out.Workflow.Blockers = append(out.Workflow.Blockers, "A usable Project Context snapshot is required for engineering actions.")
		for i := range out.Workflow.Capabilities {
			switch out.Workflow.Capabilities[i].Action {
			case "CREATE_TASK", "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION":
				out.Workflow.Capabilities[i].Available = false
				out.Workflow.Capabilities[i].Reason = "Project Context is unavailable."
			}
		}
	}
	if out.Result.Development != nil && !out.Result.Development.Ready {
		out.Workflow.Blockers = append(out.Workflow.Blockers, "A usable selected development branch is required for engineering actions.")
		for i := range out.Workflow.Capabilities {
			switch out.Workflow.Capabilities[i].Action {
			case "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION":
				out.Workflow.Capabilities[i].Available = false
				out.Workflow.Capabilities[i].Reason = "Development branch is not ready."
			}
		}
	}
	if out.Workflow.RefreshRequired {
		out.Workflow.NextStep = nil
		out.Workflow.VerificationDecisions = nil
		if v := out.Result.TaskView; v != nil {
			for i := range v.Actions {
				v.Actions[i].Available, v.Actions[i].Reason = false, "Read partition failed; refresh before acting."
			}
			if v.CurrentExecutionAcceptance != nil {
				v.CurrentExecutionAcceptance.AllowedActions = []string{}
			}
		}
		for i := range out.Workflow.Capabilities {
			out.Workflow.Capabilities[i].Available = false
			out.Workflow.Capabilities[i].Reason = "Read partition failed; refresh before acting."
		}
		for i := range out.Workflow.PendingDecisions {
			for j := range out.Workflow.PendingDecisions[i].Options {
				out.Workflow.PendingDecisions[i].Options[j].Available = false
			}
		}
	}
	out.PresentationHint = surfaceHintFor(out.Context, out.Workflow, out.Workflow.RefreshRequired)
	return out, nil
}

type WorkListInput struct {
	AISessionID   string `json:"aiSessionId"`
	Kind          string `json:"kind"`
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}
type WorkListResult struct {
	FeatureAreas          []SurfaceFeatureAreaSummary   `json:"featureAreas,omitempty"`
	NextOffset            *int                          `json:"nextOffset,omitempty"`
	Kind                  string                        `json:"kind"`
	Tasks                 []p.CompactTaskProjection     `json:"tasks,omitempty"`
	Operations            []p.SelectedOperation         `json:"operations,omitempty"`
	UnavailableOperations []SurfaceUnavailableOperation `json:"unavailableOperations,omitempty"`
	Limit                 int                           `json:"limit"`
	Offset                int                           `json:"offset"`
	HasMore               bool                          `json:"hasMore"`
	Partitions            []SurfaceReadPartition        `json:"partitions"`
	Atomic                bool                          `json:"atomic"`
}

// Historical envelopes without a semantic owner retain their original Core
// facts in the same bounded page. They have no inferred operation target.
type SurfaceUnavailableOperation struct {
	Source        string                    `json:"source"`
	ReadOnly      bool                      `json:"readOnly"`
	Reason        string                    `json:"reason"`
	Position      int                       `json:"position"`
	CoreOperation agentexecution.Projection `json:"coreOperation"`
}
type WorkListOutput struct {
	Result           WorkListResult          `json:"result"`
	Context          SurfaceContext          `json:"context"`
	Workflow         SurfaceWorkflow         `json:"workflow"`
	PresentationHint SurfacePresentationHint `json:"presentationHint"`
}

func (s *StaticService) ListSurfaceTasksForSession(ctx context.Context, session, area, taskID string, options controlapi.ListOptions) ([]task.Task, error) {
	if s.control == nil {
		return nil, errors.New("semantic task read unavailable")
	}
	return s.control.ListSurfaceTasksForSession(ctx, session, area, taskID, options)
}
func (s *StaticService) ListSurfaceOperationsForSession(ctx context.Context, session, area, taskID string, options controlapi.ListOptions) ([]agentexecution.Projection, error) {
	if s.control == nil {
		return nil, errors.New("operation read unavailable")
	}
	return s.control.ListSurfaceOperationsForSession(ctx, session, area, taskID, options)
}
func WorkListCore(ctx context.Context, s BridgeService, in WorkListInput) (WorkListOutput, error) {
	out := WorkListOutput{Workflow: surfaceWorkflow(), PresentationHint: surfaceHint()}
	if in.Kind != "FEATURE_AREA" && in.Kind != "TASK" && in.Kind != "OPERATION" {
		return out, errors.New("kind must be FEATURE_AREA, TASK or OPERATION")
	}
	if in.Limit == 0 {
		in.Limit = 25
	}
	if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 || in.Offset > 100000 {
		return out, errors.New("pagination out of bounds")
	}
	session, err := surfaceSession(ctx, s, in.AISessionID)
	if err != nil {
		return out, err
	}
	out.Context = SurfaceContext{AISessionID: in.AISessionID, ProjectID: session.Project.ID, FeatureAreaID: in.FeatureAreaID, TaskID: in.TaskID}
	if in.TaskID != "" {
		t, e := s.GetTask(ctx, in.AISessionID, in.TaskID)
		if e != nil {
			return out, publicToolError(e)
		}
		if t.ProjectID != session.Project.ID || (in.FeatureAreaID != "" && t.FeatureAreaID != in.FeatureAreaID) {
			return out, errors.New("FeatureArea/Task mismatch")
		}
		in.FeatureAreaID = t.FeatureAreaID
		out.Context.FeatureAreaID = t.FeatureAreaID
	}
	if in.FeatureAreaID != "" && in.Kind != "FEATURE_AREA" {
		a, e := s.GetFeatureArea(ctx, in.AISessionID, in.FeatureAreaID)
		if e != nil {
			return out, publicToolError(e)
		}
		if a.ProjectID != session.Project.ID {
			return out, errors.New("FeatureArea/Project mismatch")
		}
	}
	out.Result = WorkListResult{Kind: in.Kind, Limit: in.Limit, Offset: in.Offset, Partitions: []SurfaceReadPartition{}}
	// One lookahead row proves hasMore; the source adapters filter before paging.
	options := controlapi.ListOptions{Limit: in.Limit, Offset: in.Offset}
	if in.Kind == "FEATURE_AREA" {
		if in.TaskID != "" {
			return out, errors.New("FeatureArea inventory does not accept a Task target")
		}
		reader, ok := s.(interface {
			ListSurfaceFeatureAreasForSession(context.Context, string, string, controlapi.ListOptions) ([]featurearea.FeatureArea, error)
		})
		if !ok {
			return out, errors.New("FeatureArea inventory unavailable")
		}
		values, e := reader.ListSurfaceFeatureAreasForSession(ctx, in.AISessionID, in.FeatureAreaID, options)
		if e != nil {
			return out, publicToolError(e)
		}
		products := map[string]p.FeatureAreaProductProjection{}
		if workspaceReader, ok := s.(developmentWorkspaceReader); ok {
			workspace, readErr := workspaceReader.DevelopmentWorkspaceForSession(ctx, in.AISessionID, "", "")
			if readErr != nil {
				return out, publicToolError(readErr)
			}
			for _, area := range workspace.FeatureAreas {
				products[area.ID] = area.Product
			}
		}
		for _, value := range values {
			if value.ProjectID != session.Project.ID || in.FeatureAreaID != "" && value.ID != in.FeatureAreaID {
				return out, errors.New("FeatureArea scope mismatch")
			}
			product, found := products[value.ID]
			if !found {
				product = p.NewFeatureAreaProductProjection(value.ID, value.Title, string(value.Status), value.LegacyReadOnly)
			}
			out.Result.FeatureAreas = append(out.Result.FeatureAreas, SurfaceFeatureAreaSummary{Product: product, ID: value.ID, ProjectID: value.ProjectID, Title: boundedUTF8(value.Title, 512), ProblemStatement: boundedUTF8(value.ProblemStatement, 2048), Status: string(value.Status), UpdatedAt: value.UpdatedAt})
		}
		if len(values) == in.Limit {
			next, e := reader.ListSurfaceFeatureAreasForSession(ctx, in.AISessionID, in.FeatureAreaID, controlapi.ListOptions{Limit: 1, Offset: in.Offset + in.Limit})
			if e != nil {
				return out, publicToolError(e)
			}
			out.Result.HasMore = len(next) > 0
		}
	} else if in.Kind == "TASK" {
		r, ok := s.(interface {
			ListSurfaceTasksForSession(context.Context, string, string, string, controlapi.ListOptions) ([]task.Task, error)
		})
		if !ok {
			return out, errors.New("semantic task reader unavailable")
		}
		values, e := r.ListSurfaceTasksForSession(ctx, in.AISessionID, in.FeatureAreaID, in.TaskID, options)
		if e != nil {
			return out, publicToolError(e)
		}
		for _, t := range values {
			if t.Type != "" && t.Type != task.TypeDevelopment || t.Work.Type != "" && t.Work.Type != task.TypeDevelopment {
				return out, errors.New("non-semantic Task in TASK inventory")
			}
			if t.ProjectID != session.Project.ID || (in.FeatureAreaID != "" && t.FeatureAreaID != in.FeatureAreaID) || (in.TaskID != "" && t.ID != in.TaskID) {
				return out, errors.New("Task scope mismatch")
			}
			out.Result.Tasks = append(out.Result.Tasks, surfaceTask(t))
		}
		if len(values) == in.Limit {
			next, e := r.ListSurfaceTasksForSession(ctx, in.AISessionID, in.FeatureAreaID, in.TaskID, controlapi.ListOptions{Limit: 1, Offset: in.Offset + in.Limit})
			if e != nil {
				return out, publicToolError(e)
			}
			out.Result.HasMore = len(next) > 0
		}
	} else {
		r, ok := s.(interface {
			ListSurfaceOperationsForSession(context.Context, string, string, string, controlapi.ListOptions) ([]agentexecution.Projection, error)
		})
		if !ok {
			return out, errors.New("scoped operation reader unavailable")
		}
		values, e := r.ListSurfaceOperationsForSession(ctx, in.AISessionID, in.FeatureAreaID, in.TaskID, options)
		if e != nil {
			return out, publicToolError(e)
		}
		for index, v := range values {
			if v.ProjectID != session.Project.ID || (in.FeatureAreaID != "" && v.FeatureAreaID != in.FeatureAreaID) || (in.TaskID != "" && v.TaskID != in.TaskID) {
				return out, errors.New("Operation scope mismatch")
			}
			op := p.SelectedOperationFromAgentExecution(v)
			if op == nil {
				out.Result.UnavailableOperations = append(out.Result.UnavailableOperations, SurfaceUnavailableOperation{Source: "CORE_OPERATION", ReadOnly: true, Reason: "Exact semantic target unavailable; historical Core facts are read-only.", Position: in.Offset + index, CoreOperation: v})
				continue
			}
			out.Result.Operations = append(out.Result.Operations, *op)
		}
		if len(values) == in.Limit {
			next, e := r.ListSurfaceOperationsForSession(ctx, in.AISessionID, in.FeatureAreaID, in.TaskID, controlapi.ListOptions{Limit: 1, Offset: in.Offset + in.Limit})
			if e != nil {
				return out, publicToolError(e)
			}
			out.Result.HasMore = len(next) > 0
		}
	}
	if out.Result.HasMore {
		next := in.Offset + in.Limit
		out.Result.NextOffset = &next
	}
	out.Result.Partitions = append(out.Result.Partitions, readPartition(in.Kind, nil))
	out.PresentationHint = surfaceHintFor(out.Context, out.Workflow, out.Workflow.RefreshRequired)
	return out, nil
}

type OperationGetInput struct {
	AISessionID string            `json:"aiSessionId"`
	Target      p.OperationTarget `json:"target"`
	Include     []string          `json:"include,omitempty"`
}
type SurfaceOperationResult struct {
	Source  string            `json:"source"`
	RunID   string            `json:"runId,omitempty"`
	Status  string            `json:"status"`
	Failure *RunFailureOutput `json:"failure,omitempty"`
}
type SurfaceOperationClaim struct {
	Source string                   `json:"source"`
	RunID  string                   `json:"runId"`
	Claim  externalAgentClaimOutput `json:"claim"`
}
type SurfaceOperationEvidence struct {
	Source       string                    `json:"source"`
	RunID        string                    `json:"runId"`
	Git          *GitEvidenceOutput        `json:"git,omitempty"`
	Verification *VerificationReportOutput `json:"verification,omitempty"`
}
type SurfaceOperationActivity struct {
	Source     string                 `json:"source"`
	RunID      string                 `json:"runId"`
	Telemetry  *agentruntime.Snapshot `json:"telemetry,omitempty"`
	TokenUsage TokenUsageOutput       `json:"tokenUsage"`
}
type OperationGetResult struct {
	ReviewResult          *ReviewResultGetOutput            `json:"reviewResult,omitempty"`
	FeatureAreaProduct    *p.FeatureAreaProductProjection   `json:"featureAreaProduct,omitempty"`
	RecoveryStatuses      []recoverystatus.Projection       `json:"recoveryStatuses,omitempty"`
	ReviewSource          *SurfaceReviewSource              `json:"reviewSource,omitempty"`
	TaskSources           *SurfaceTaskSources               `json:"taskSources,omitempty"`
	ExecutionDefinition   *SurfaceDefinitionSource          `json:"executionDefinition,omitempty"`
	CoreOperation         *agentexecution.Projection        `json:"coreOperation,omitempty"`
	CoreOperationSource   string                            `json:"coreOperationSource,omitempty"`
	Cancellation          *executionCancellationProjection  `json:"cancellation,omitempty"`
	CancellationSource    string                            `json:"cancellationSource,omitempty"`
	Model                 string                            `json:"model,omitempty"`
	ReasoningEffort       string                            `json:"reasoningEffort,omitempty"`
	Inspection            *ContextProbeOutput               `json:"inspection,omitempty"`
	Operation             p.SelectedOperation               `json:"operation"`
	ReviewProposal        *review.ImplementationResult      `json:"reviewProposal,omitempty"`
	ReviewProposalSource  string                            `json:"reviewProposalSource,omitempty"`
	ReviewEvidence        *review.EvidenceSummary           `json:"reviewEvidence,omitempty"`
	RepositoryDrift       *RepositoryDriftOutput            `json:"repositoryDrift,omitempty"`
	RepositoryDriftSource string                            `json:"repositoryDriftSource,omitempty"`
	RunResults            []SurfaceOperationResult          `json:"runResults,omitempty"`
	Evidence              []SurfaceOperationEvidence        `json:"evidence,omitempty"`
	AgentClaims           []SurfaceOperationClaim           `json:"agentClaims,omitempty"`
	Activity              []SurfaceOperationActivity        `json:"activity,omitempty"`
	RuntimeWorksets       []workset.RuntimeWorkset          `json:"runtimeWorksets,omitempty"`
	RuntimeRequests       []agentruntime.Request            `json:"runtimeRequests,omitempty"`
	RuntimeObservations   []agentruntime.RuntimeObservation `json:"runtimeObservations,omitempty"`
	RuntimeApprovals      []p.PendingDecision               `json:"runtimeApprovals,omitempty"`
	Partitions            []SurfaceReadPartition            `json:"partitions"`
	Atomic                bool                              `json:"atomic"`
}
type OperationGetOutput struct {
	Result           OperationGetResult      `json:"result"`
	Context          SurfaceContext          `json:"context"`
	Workflow         SurfaceWorkflow         `json:"workflow"`
	PresentationHint SurfacePresentationHint `json:"presentationHint"`
}

func OperationGetCore(ctx context.Context, s BridgeService, in OperationGetInput) (OperationGetOutput, error) {
	out := OperationGetOutput{Workflow: surfaceWorkflow(), PresentationHint: surfaceHint()}
	if err := validateSurfaceIncludes(in.Include, []string{"RESULT", "EVIDENCE", "ACTIVITY", "RUNTIME_REQUESTS", "SOURCE"}); err != nil {
		return out, err
	}
	if strings.TrimSpace(in.Target.OperationID) == "" {
		return out, errors.New("exact operation target required")
	}
	session, err := surfaceSession(ctx, s, in.AISessionID)
	if err != nil {
		return out, err
	}
	target := in.Target
	var taskID, areaID, status string
	var updated time.Time
	var selectedReview *review.Review
	runIDs := []string{}
	executionRevisionID := ""
	inspectionSuccessorRunID := ""
	switch target.Kind {
	case "REVIEW":
		v, e := s.GetReview(ctx, in.AISessionID, target.OperationID)
		if e != nil {
			return out, publicToolError(e)
		}
		if v.ID != target.OperationID || v.ProjectID != session.Project.ID {
			return out, errors.New("Review Project mismatch")
		}
		taskID, areaID, status, updated = v.TaskID, v.FeatureAreaID, v.TerminalStatus, v.CreatedAt
		selectedReview = &v
		source := surfaceReviewSource(v)
		out.Result.ReviewSource = &source
		if included(in.Include, "RESULT") || included(in.Include, "ACTIVITY") {
			out.Result.Model = v.Model
			out.Result.ReasoningEffort = v.ReasoningEffort
		}
		if v.RunID != "" {
			runIDs = append(runIDs, v.RunID)
		}
	case "EXECUTION":
		v, e := s.GetExecutionProposal(ctx, in.AISessionID, target.OperationID)
		if e != nil {
			return out, publicToolError(e)
		}
		if v.ID != target.OperationID || v.ProjectID != session.Project.ID {
			return out, errors.New("Execution Project mismatch")
		}
		taskID, status, updated = v.TaskID, string(v.Status), v.UpdatedAt
		executionRevisionID = v.TaskDefinitionRevisionID
		if included(in.Include, "RESULT") || included(in.Include, "ACTIVITY") {
			out.Result.Model = v.RuntimeConfig.Model
			out.Result.ReasoningEffort = v.RuntimeConfig.ReasoningEffort
		}
		if v.Outcome != "" && v.Outcome != "PENDING" {
			status = string(v.Outcome)
		}
		runIDs = append(runIDs, v.RunID, v.ImplementationRunID, v.VerificationRunID)
	case "INSPECTION":
		r, ok := s.(interface {
			GetContextProbeForSession(context.Context, string, string) (contextprobe.Probe, error)
		})
		if !ok {
			return out, errors.New("Inspection read unavailable")
		}
		v, e := r.GetContextProbeForSession(ctx, in.AISessionID, target.OperationID)
		if e != nil {
			return out, publicToolError(e)
		}
		if v.ID != target.OperationID || v.ProjectID != session.Project.ID {
			return out, errors.New("Inspection Project mismatch")
		}
		taskID, status, updated = v.TaskID, string(v.Status), v.UpdatedAt
		inspection := contextProbeOutput(v)
		out.Result.Inspection = &inspection
		if v.Canonical != nil {
			out.Result.Model = v.Canonical.Model
			out.Result.ReasoningEffort = v.Canonical.ReasoningEffort
		}
		// The session-scoped Probe carries the durable continuation receipt's
		// exact successor. Keep the immutable source readable, but select the
		// current continuation by default without scanning Task history.
		inspectionSuccessorRunID = v.RecoverySuccessorRunID
		runIDs = append(runIDs, v.RunID, inspectionSuccessorRunID)
	default:
		return out, errors.New("target.kind must be REVIEW, EXECUTION or INSPECTION")
	}
	if target.OwnerTaskID != "" && target.OwnerTaskID != taskID {
		return out, errors.New("Operation owner Task mismatch")
	}
	if taskID != "" {
		t, e := s.GetTask(ctx, in.AISessionID, taskID)
		if e != nil {
			return out, publicToolError(e)
		}
		if t.ID != taskID || t.ProjectID != session.Project.ID || (areaID != "" && t.FeatureAreaID != areaID) {
			return out, errors.New("Operation Task association mismatch")
		}
		areaID = t.FeatureAreaID
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range runIDs {
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if target.RunID != "" && !seen[target.RunID] {
		return out, errors.New("Operation Run mismatch")
	}
	if target.RunID == "" && len(ids) > 0 {
		target.RunID = ids[len(ids)-1]
	}
	if target.Kind != "INSPECTION" {
		target.OwnerTaskID = taskID
	}
	out.Context = SurfaceContext{AISessionID: in.AISessionID, ProjectID: session.Project.ID, FeatureAreaID: areaID, TaskID: taskID, Target: &target}
	out.Result.Operation = p.SelectedOperation{Kind: target.Kind, OperationID: target.OperationID, OwnerTaskID: target.OwnerTaskID, RunID: target.RunID, Status: status, UpdatedAt: updated, ReadOnly: target.Kind == "REVIEW", Target: target}
	out.Result.Partitions = []SurfaceReadPartition{readPartition("OPERATION", nil)}
	addPartition := func(name string, e error) {
		out.Result.Partitions = append(out.Result.Partitions, readPartition(name, e))
		if e != nil {
			out.Workflow.RefreshRequired = true
		}
	}
	if included(in.Include, "SOURCE") && taskID != "" {
		owner, e := s.GetTask(ctx, in.AISessionID, taskID)
		if e != nil {
			addPartition("TASK_SOURCES", e)
		} else if owner.ProjectID != session.Project.ID || owner.FeatureAreaID != areaID {
			return out, errors.New("Source Task association mismatch")
		} else {
			source, e := readSurfaceTaskSources(ctx, s, in.AISessionID, owner, "")
			addPartition("TASK_SOURCES", e)
			if e == nil {
				out.Result.TaskSources = &source
			}
			if executionRevisionID != "" {
				definition, e := readSurfaceDefinition(ctx, s, in.AISessionID, owner, executionRevisionID)
				addPartition("EXECUTION_DEFINITION", e)
				if e == nil {
					out.Result.ExecutionDefinition = &definition
				}
			}
			if out.Result.ReviewSource != nil {
				receipt, e := readSurfaceReviewReceipt(ctx, s, in.AISessionID, owner, out.Result.ReviewSource.ID, false)
				addPartition("REVIEW_DECISION", e)
				if e == nil {
					out.Result.ReviewSource.DecisionReceipt = receipt
					out.Result.ReviewSource.DecisionStatus = "UNRESOLVED_OR_UNRECORDED"
					if receipt != nil {
						out.Result.ReviewSource.DecisionStatus = receipt.Option
					}
				}
			}
		}
	}
	if reader, ok := s.(compactWorkspaceReader); ok && taskID != "" {
		v, e := reader.CompactWorkspaceForSession(ctx, in.AISessionID, areaID, taskID)
		addPartition("WORKFLOW", e)
		if e == nil {
			product := v.Product
			out.Result.FeatureAreaProduct = &product
			refresh := out.Workflow.RefreshRequired
			out.Workflow = projectSurfaceWorkflow(v)
			out.Workflow.RefreshRequired = refresh
			if v.CurrentOperation == nil || v.CurrentOperation.ID != target.OperationID || v.CurrentOperation.Kind != target.Kind {
				out.Workflow.PendingDecisions = []p.PendingDecision{}
				out.Workflow.Capabilities = []p.Capability{}
			}
			part := &out.Result.Partitions[len(out.Result.Partitions)-1]
			part.Generation = v.Generation
			part.SourceSequence = v.SourceSequence
		}
	} else if r, ok := s.(interface {
		WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error)
	}); ok && taskID != "" {
		g, e := r.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: in.AISessionID, TaskID: taskID})
		addPartition("WORKFLOW", e)
		if e == nil {
			v := p.Projection{CurrentState: g.CurrentState, CurrentPhase: string(g.TaskPhase), Generation: g.Generation, SourceSequence: g.SourceSequence, NextAction: g.NextAction, Capabilities: []p.Capability{}, Blockers: []string{}}
			refresh := out.Workflow.RefreshRequired
			out.Workflow = projectSurfaceWorkflow(v)
			out.Workflow.RefreshRequired = refresh
			partition := &out.Result.Partitions[len(out.Result.Partitions)-1]
			partition.Generation = g.Generation
			partition.SourceSequence = g.SourceSequence
		}
	}
	// Read only exact Runs associated with the semantic operation. Runtime and
	// evidence are opt-in; no Task/Project history scan or latest-owner fallback.
	var coreOperation *agentexecution.Projection
	runs := []run.Run{}
	if target.Kind == "EXECUTION" && len(ids) == 0 {
		if _, ok := s.(agentExecutionReader); ok {
			projection, e := readAgentExecution(ctx, s, in.AISessionID, target.OperationID)
			addPartition("CORE_OPERATION", e)
			if e == nil {
				if projection.ProjectID != session.Project.ID || projection.TaskID != taskID || projection.SemanticOwnerKind != target.Kind || projection.SemanticOwnerID != target.OperationID || projection.RunID != "" {
					return out, errors.New("Operation envelope identity mismatch")
				}
				coreOperation = &projection
				if op := p.SelectedOperationFromAgentExecution(projection); op != nil {
					out.Result.Operation = *op
					out.Result.Operation.Target = target
				}
			}
		}
	}
	for _, id := range ids {
		if len(in.Include) == 0 && id != target.RunID {
			continue
		}
		r, e := s.GetRunForSession(ctx, in.AISessionID, id)
		if e != nil {
			addPartition("RUN:"+id, e)
			continue
		}
		association := (target.Kind == "REVIEW" && r.ReviewID == target.OperationID) || (target.Kind == "EXECUTION" && r.ExecutionProposalID == target.OperationID) || (target.Kind == "INSPECTION" && r.ContextProbeID == target.OperationID)
		if target.Kind == "INSPECTION" && id == inspectionSuccessorRunID {
			// Continuations intentionally have no ContextProbeID. Only the exact
			// receipt-linked Inspection Run may use that shape; same-owner
			// Recovery diagnostics and unrelated Runs remain ineligible.
			association = r.ContextProbeID == "" && r.FeatureAreaID == "" &&
				r.OperationKind == run.OperationInspection && r.OperationID == target.OperationID && r.Purpose == run.PurposeInspection
		}
		if r.ID != id || r.ProjectID != session.Project.ID || r.TaskID != taskID || !association || r.OperationKind != "" && string(r.OperationKind) != target.Kind || r.OperationID != "" && r.OperationID != target.OperationID {
			return out, errors.New("Exact operation Run association changed")
		}
		runs = append(runs, r)
		if out.Result.ReviewSource != nil && id == target.RunID {
			out.Result.ReviewSource.RunStatus = string(r.Status)
			if r.Status == run.StatusSucceeded && selectedReview != nil && (r.Result != nil || r.ResultProtocol == run.NaturalResultProtocol || selectedReview.ExecutorKind == review.ExecutorUpperLLM) {
				result, e := readExactReviewResult(ctx, s, ReviewResultGetInput{AISessionID: in.AISessionID, ReviewID: target.OperationID}, *selectedReview, r)
				addPartition("REVIEW_SUMMARY", e)
				if e == nil && result.Proposal != nil {
					proposal := result.Proposal
					out.Result.ReviewSource.Summary = boundedUTF8(proposal.Summary, 2048)
					out.Result.ReviewSource.SummaryStatus = "AVAILABLE"
					out.Result.ReviewSource.SummaryTruncated = len(proposal.Summary) > len(out.Result.ReviewSource.Summary)
				} else if e == nil && result.Source == "GPT_RUN_ANALYSIS" {
					out.Result.ReviewSource.SummaryStatus = "WAITING_ANALYSIS"
				} else {
					out.Result.ReviewSource.SummaryStatus = "UNAVAILABLE"
				}
				if included(in.Include, "RESULT") {
					addPartition("REVIEW_PROPOSAL", e)
					if e == nil {
						out.Result.ReviewResult = &result
						out.Result.ReviewProposal = result.Proposal
						if result.Proposal != nil {
							out.Result.ReviewProposalSource = result.Source + ":" + id
						}
					}
				}
			}
		}
		if id == target.RunID && r.AgentExecutionID != "" {
			projection, e := readAgentExecution(ctx, s, in.AISessionID, r.AgentExecutionID)
			addPartition("CORE_OPERATION", e)
			if e == nil {
				if projection.ProjectID != session.Project.ID || projection.TaskID != taskID || projection.SemanticOwnerID != target.OperationID || projection.SemanticOwnerKind != target.Kind {
					return out, errors.New("Operation envelope identity mismatch")
				}
				if projection.RunID == target.RunID {
					coreOperation = &projection
					op := p.SelectedOperationFromAgentExecution(projection)
					if op != nil {
						out.Result.Operation = *op
						out.Result.Operation.Target = target
					}
				}
			}
		}
		addPartition("RUN:"+id, nil)
		if id == target.RunID && target.Kind == "REVIEW" && out.Result.Operation.Status == "" {
			out.Result.Operation.Status = string(r.Status)
		}
		if r.UpdatedAt.After(out.Result.Operation.UpdatedAt) {
			out.Result.Operation.UpdatedAt = r.UpdatedAt
		}
		v := runOutput(r, true)
		if included(in.Include, "RESULT") {
			out.Result.RunResults = append(out.Result.RunResults, SurfaceOperationResult{Source: "CORE_RUN", RunID: id, Status: string(r.Status), Failure: v.Failure})
			if r.Result != nil {
				out.Result.AgentClaims = append(out.Result.AgentClaims, SurfaceOperationClaim{Source: "AGENT_CLAIM", RunID: id, Claim: externalAgentClaimOutput{Available: v.AgentClaim != "", Message: v.AgentClaim}})
			}
		}
		if included(in.Include, "EVIDENCE") {
			out.Result.Evidence = append(out.Result.Evidence, SurfaceOperationEvidence{Source: "CORE_EVIDENCE", RunID: id, Git: v.GitEvidence, Verification: v.VerificationReport})
		}
		if included(in.Include, "ACTIVITY") {
			var telemetry *agentruntime.Snapshot
			if r.AgentRuntime != nil {
				copy := *r.AgentRuntime
				copy.Requests = nil
				telemetry = &copy
			}
			out.Result.Activity = append(out.Result.Activity, SurfaceOperationActivity{Source: "RUNTIME_TELEMETRY", RunID: id, Telemetry: telemetry, TokenUsage: v.TokenUsage})
			if r.AgentRuntime != nil {
				out.Result.Partitions[len(out.Result.Partitions)-1].SourceSequence = int64(r.AgentRuntime.Sequence)
			}
		}
		if included(in.Include, "RUNTIME_REQUESTS") {
			if reader, ok := s.(interface {
				RecoveryStatusForSourceForSession(context.Context, string, string) (recoverystatus.Projection, error)
			}); ok {
				projection, e := reader.RecoveryStatusForSourceForSession(ctx, in.AISessionID, id)
				addPartition("RECOVERY_STATUS:"+id, e)
				if e == nil {
					out.Result.RecoveryStatuses = append(out.Result.RecoveryStatuses, projection)
				}
			}
			if r.AgentRuntime != nil && r.AgentRuntime.Observation != nil {
				out.Result.RuntimeObservations = append(out.Result.RuntimeObservations, *r.AgentRuntime.Observation)
			}
			if r.RuntimeWorkset != nil {
				out.Result.RuntimeWorksets = append(out.Result.RuntimeWorksets, *r.RuntimeWorkset)
			}
			if r.AgentRuntime != nil {
				for _, request := range r.AgentRuntime.Requests {
					projection := agentruntime.ProjectRequest(request, r.AgentRuntime.Observation, r.AgentRuntime.TurnStatus)
					request.Projection = &projection
					out.Result.RuntimeRequests = append(out.Result.RuntimeRequests, request)
				}
			}
			if reader, ok := s.(interface {
				WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error)
			}); ok {
				decisions, e := reader.WorkspaceRuntimeDecisionsForSession(ctx, in.AISessionID, id)
				addPartition("RUNTIME_APPROVALS:"+id, e)
				if e == nil {
					for _, d := range decisions {
						if d.Kind == "RUNTIME_APPROVAL" {
							out.Result.RuntimeApprovals = append(out.Result.RuntimeApprovals, d)
						}
					}
				}
			}
		}
	}
	if included(in.Include, "RESULT") && coreOperation != nil {
		out.Result.CoreOperation, out.Result.CoreOperationSource = coreOperation, "CORE_OPERATION"
		cancellation, e := projectExecutionCancellation(ctx, s, in.AISessionID, coreOperation, runs)
		addPartition("CANCELLATION", e)
		if e == nil {
			out.Result.Cancellation, out.Result.CancellationSource = &cancellation, "CORE_CANCELLATION"
		}
	}
	if target.Kind == "REVIEW" && included(in.Include, "EVIDENCE") {
		v, e := s.ReviewEvidence(ctx, in.AISessionID, target.OperationID)
		addPartition("REVIEW_EVIDENCE", e)
		if e == nil {
			out.Result.ReviewEvidence = &v
		}
		if reader, ok := s.(reviewDriftReader); ok {
			drift, e := reader.ReviewRepositoryDriftForSession(ctx, in.AISessionID, target.OperationID, review.DefaultDriftLimit)
			addPartition("REPOSITORY_DRIFT", e)
			if e == nil {
				out.Result.RepositoryDrift, out.Result.RepositoryDriftSource = RepositoryDriftOutputFor(drift), "CORE_REPOSITORY_DRIFT"
			}
		} else {
			addPartition("REPOSITORY_DRIFT", errors.New("repository drift reader unavailable"))
		}
	}
	if out.Workflow.RefreshRequired {
		if c := out.Result.Cancellation; c != nil {
			c.Available = false
			c.AvailableActions = []string{}
		}
		for i := range out.Workflow.Capabilities {
			out.Workflow.Capabilities[i].Available = false
			out.Workflow.Capabilities[i].Reason = "Read partition failed; refresh before acting."
		}
		for i := range out.Workflow.PendingDecisions {
			for j := range out.Workflow.PendingDecisions[i].Options {
				out.Workflow.PendingDecisions[i].Options[j].Available = false
			}
		}
	}
	if out.Result.Operation.Status == "" {
		out.Result.Operation.Status = "PENDING"
	}
	sort.SliceStable(out.Result.Partitions, func(i, j int) bool { return out.Result.Partitions[i].Name < out.Result.Partitions[j].Name })
	out.PresentationHint = surfaceHintFor(out.Context, out.Workflow, out.Workflow.RefreshRequired)
	return out, nil
}
