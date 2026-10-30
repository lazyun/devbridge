package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/runanalysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type upperReviewService interface {
	SubmitReviewResult(context.Context, review.SubmitInput) (review.ReportReceipt, error)
	GetReviewReportForSession(context.Context, string, string) (review.StoredReport, error)
	GetReviewResultReceiptForSession(context.Context, string, string, string) (review.ReportReceipt, error)
	UpperReviewAccessForSession(context.Context, string, string) (review.UpperAccess, error)
	ListUpperReviewEvidenceForSession(context.Context, string, string) ([]review.FileEvidence, error)
}
type upperReviewPreparationService interface {
	PrepareUpperReview(context.Context, review.UpperTarget) (review.UpperAccess, error)
}
type ReviewResultGetInput struct {
	AISessionID string `json:"aiSessionId"`
	ReviewID    string `json:"reviewId"`
	RequestID   string `json:"requestId,omitempty"`
}
type ReviewResultGetOutput struct {
	ReviewID           string                       `json:"reviewId,omitempty"`
	RunID              string                       `json:"runId,omitempty"`
	State              string                       `json:"state,omitempty"`
	Source             string                       `json:"source,omitempty"`
	ResultProtocol     string                       `json:"resultProtocol,omitempty"`
	Analysis           *runanalysis.RecordedReport  `json:"analysis,omitempty"`
	AnalysisSourceHash string                       `json:"analysisSourceHash,omitempty"`
	Proposal           *review.ImplementationResult `json:"proposal,omitempty"`
	Report             *review.StoredReport         `json:"report,omitempty"`
	Receipt            *review.ReportReceipt        `json:"receipt,omitempty"`
}

const upperIDSchema = `{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"}`
const UpperReviewTargetSchema = `{"type":"object","properties":{"aiSessionId":` + upperIDSchema + `,"featureAreaId":` + upperIDSchema + `,"taskId":` + upperIDSchema + `,"reviewId":` + upperIDSchema + `,"runId":` + upperIDSchema + `,"runtimeEpoch":` + upperIDSchema + `,"grantGeneration":{"type":"integer","minimum":1}},"required":["aiSessionId","featureAreaId","taskId","reviewId","runId","runtimeEpoch","grantGeneration"],"additionalProperties":false}`

func ReviewReportSchema() map[string]any {
	var proposal map[string]any
	_ = json.Unmarshal([]byte(review.ImplementationResultJSONSchema), &proposal)
	p := proposal["properties"].(map[string]any)
	// Some connectors apply a full-string regex match. Keep the nonblank rule
	// equivalent for search and full-match validators, including multiline text.
	const nonblank = `^[\s\S]*\S[\s\S]*$`
	txt := map[string]any{"type": "string", "minLength": 1, "maxLength": 16384, "pattern": nonblank}
	summary := map[string]any{"type": "string", "minLength": 1, "maxLength": 16384, "pattern": nonblank, "description": p["summary"].(map[string]any)["description"]}
	ref := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"evidenceId", "path", "digest"}, "dependentRequired": map[string]any{"startLine": []string{"endLine"}, "endLine": []string{"startLine"}}, "properties": map[string]any{"repositoryId": json.RawMessage(upperIDSchema), "memberId": json.RawMessage(upperIDSchema), "evidenceId": json.RawMessage(upperIDSchema), "path": map[string]any{"type": "string", "maxLength": 4096}, "digest": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "startLine": map[string]any{"type": "integer", "minimum": 1}, "endLine": map[string]any{"type": "integer", "minimum": 1}}}
	refs := map[string]any{"type": "array", "maxItems": 256, "items": ref}
	list := map[string]any{"type": "array", "maxItems": 128, "items": txt}
	nonempty := map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": txt}
	finding := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "severity", "title", "description", "impact", "evidenceRefs", "suggestedChange"}, "properties": map[string]any{"id": json.RawMessage(upperIDSchema), "severity": map[string]any{"type": "string", "enum": []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "INFO"}}, "title": txt, "description": txt, "impact": txt, "suggestedChange": txt, "evidenceRefs": map[string]any{"type": "array", "maxItems": 64, "items": ref}}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schemaVersion", "summary", "disposition", "coverage", "findings", "limitations", "recommendedNextSteps", "evidenceRefs"}, "properties": map[string]any{"schemaVersion": map[string]any{"type": "integer", "enum": []int{1, 2}}, "repositoryTargets": repositoryTargetsSchema(), "primaryMemberId": json.RawMessage(upperIDSchema), "summary": summary, "disposition": map[string]any{"type": "string", "enum": []string{"APPROVAL_READY", "FURTHER_REVIEW_REQUIRED", "BLOCKED"}}, "coverage": nonempty, "findings": map[string]any{"type": "array", "maxItems": 128, "items": finding}, "limitations": nonempty, "recommendedNextSteps": list, "evidenceRefs": refs, "workSpec": confirmationRaw(WorkSpecInputJSONSchema), "verificationSpec": confirmationRaw(VerificationSpecInputJSONSchema)}, "if": map[string]any{"properties": map[string]any{"disposition": map[string]any{"const": "APPROVAL_READY"}}}, "then": map[string]any{"required": []string{"workSpec", "verificationSpec"}}, "else": map[string]any{"not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"workSpec"}}, map[string]any{"required": []string{"verificationSpec"}}}}}}
}
func ReviewResultSubmitSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal([]byte(UpperReviewTargetSchema), &schema)
	props := schema["properties"].(map[string]any)
	props["requestId"] = json.RawMessage(upperIDSchema)
	props["report"] = ReviewReportSchema()
	schema["required"] = append(schema["required"].([]any), "requestId", "report")
	raw, _ := json.Marshal(schema)
	return raw
}

type ReviewAccessGetOutput struct {
	Access   review.UpperAccess    `json:"access"`
	Evidence []review.FileEvidence `json:"evidence"`
}

func RegisterUpperReviewTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "review_result_submit", Description: "Submit the exact UPPER_LLM Review structured report. Core persists it and closes read authorization and claim atomically before COMPLETED. This does not approve implementation or validate engineering correctness. Replay the same requestId and payload after response loss. Do not ask GPT to analyze its own submitted report again.", InputSchema: ReviewResultSubmitSchema(), Annotations: stateChangingAnnotations("Submit Review report", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in review.SubmitInput) (*sdkmcp.CallToolResult, review.ReportReceipt, error) {
		p, ok := service.(upperReviewService)
		if !ok {
			return nil, review.ReportReceipt{}, errors.New("UPPER_LLM Review result service unavailable")
		}
		out, err := p.SubmitReviewResult(ctx, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "review_result_get", OutputSchema: json.RawMessage(runtimeDTOSchema[ReviewResultGetOutput]()), Description: "Read the exact Review result by its recorded executor and result protocol. UPPER_LLM returns report; NATURAL_V1 Coding Review returns the original GPT analysis.report/receipt and persisted canonical proposal, never JSON-parses native prose. A state other than RECORDED means no formal analysis is available yet; use run_analysis_get for that exact Run. With requestId, read only the matching submission receipt. Read-only; never creates, approves or reexecutes a Review.", InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":` + upperIDSchema + `,"reviewId":` + upperIDSchema + `,"requestId":` + upperIDSchema + `},"required":["aiSessionId","reviewId"],"additionalProperties":false}`), Annotations: toolAnnotations("Read Review report or receipt", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in ReviewResultGetInput) (*sdkmcp.CallToolResult, ReviewResultGetOutput, error) {
		out, err := ReviewResultGetCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "review_access_get", Description: "Read exact UPPER_LLM Review file authorization state, frozen scope, baseline and fenced target. This is not authorization creation or Review completion.", InputSchema: json.RawMessage(externalReviewGetSchema), Annotations: toolAnnotations("Read Review access", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalReviewGetInput) (*sdkmcp.CallToolResult, review.UpperAccess, error) {
		p, ok := service.(upperReviewService)
		if !ok {
			return nil, review.UpperAccess{}, errors.New("UPPER_LLM Review access service unavailable")
		}
		out, err := p.UpperReviewAccessForSession(ctx, in.AISessionID, in.ReviewID)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "review_access_prepare", Description: "Explicitly activate an already recorded PREPARING UPPER_LLM Review grant using its exact authorized target, including runtimeEpoch and grantGeneration. Core rechecks current lifecycle, owner, workspace and snapshot before activation; failure leaves access unconfirmed. This continues that exact grant and does not recover a Review, create a grant or replay a recovery receipt. After response loss, retry the same target; never replace its version with the latest target.", InputSchema: json.RawMessage(UpperReviewTargetSchema), OutputSchema: json.RawMessage(upperReviewAccessOutputSchema), Annotations: stateChangingAnnotations("Prepare recorded Review access", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in review.UpperTarget) (*sdkmcp.CallToolResult, review.UpperAccess, error) {
		p, ok := service.(upperReviewPreparationService)
		if !ok {
			return nil, review.UpperAccess{}, errors.New("UPPER_LLM Review preparation service unavailable")
		}
		out, err := p.PrepareUpperReview(ctx, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "review_evidence_get", Description: "Read up to 512 most recent durable Core-observed content receipts for the exact upper Review. File lists and path hits are not source-content evidence.", InputSchema: json.RawMessage(externalReviewGetSchema), Annotations: toolAnnotations("Read Review evidence", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalReviewGetInput) (*sdkmcp.CallToolResult, ReviewAccessGetOutput, error) {
		p, ok := service.(upperReviewService)
		if !ok {
			return nil, ReviewAccessGetOutput{}, errors.New("UPPER_LLM Review access unavailable")
		}
		a, e := p.UpperReviewAccessForSession(ctx, in.AISessionID, in.ReviewID)
		if e != nil {
			return nil, ReviewAccessGetOutput{}, publicToolError(e)
		}
		v, e := p.ListUpperReviewEvidenceForSession(ctx, in.AISessionID, in.ReviewID)
		return nil, ReviewAccessGetOutput{Access: a, Evidence: v}, publicToolError(e)
	})

}

func (s *StaticService) SubmitReviewResult(ctx context.Context, in review.SubmitInput) (review.ReportReceipt, error) {
	return s.control.SubmitReviewResult(ctx, in)
}
func (s *StaticService) GetReviewReportForSession(ctx context.Context, session, id string) (review.StoredReport, error) {
	return s.control.GetReviewReportForSession(ctx, session, id)
}
func (s *StaticService) GetReviewResultReceiptForSession(ctx context.Context, session, id, request string) (review.ReportReceipt, error) {
	return s.control.GetReviewResultReceiptForSession(ctx, session, id, request)
}
func (s *StaticService) UpperReviewAccessForSession(ctx context.Context, session, id string) (review.UpperAccess, error) {
	return s.control.UpperReviewAccessForSession(ctx, session, id)
}
func (s *StaticService) PrepareUpperReview(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	return s.control.PrepareUpperReview(ctx, target)
}

const upperReviewAccessOutputSchema = `{"type":"object","properties":{"reviewCreatedAt":{"type":"string"},"protocolVersion":{"type":"integer","enum":[1,2]},"repositoryTargets":` + repositoryTargetsJSONSchema + `,"startSnapshot":{"type":"string"},"blocker":{"type":"string"},"target":` + UpperReviewTargetSchema + `,"state":{"type":"string"},"workspaceId":{"type":"string"},"worktreeId":{"type":"string"},"baseline":{"type":"string"},"allowedPaths":{"type":"array","items":{"type":"string"}},"forbiddenPaths":{"type":"array","items":{"type":"string"}},"lastObservedAt":{"type":"string"}},"required":["target","state","baseline","allowedPaths","forbiddenPaths"],"additionalProperties":false}`

func (s *StaticService) ListUpperReviewEvidenceForSession(ctx context.Context, session, id string) ([]review.FileEvidence, error) {
	return s.control.ListUpperReviewEvidenceForSession(ctx, session, id)
}

// UnifiedReviewContractIdentity fingerprints executable contract declarations,
// not a second version registry. The business catalog is common to both MCPs.
func UnifiedReviewContractIdentity() (string, string) {
	catalog, _ := json.Marshal(SurfaceBusinessToolNames)
	schemas, _ := json.Marshal([]any{json.RawMessage(reviewStartInputSchema()), ReviewResultSubmitSchema(), reviewFileReadSchema(), json.RawMessage(UpperReviewTargetSchema), json.RawMessage(ExternalAgentOperationRecoverInputSchema)})
	a, b := sha256.Sum256(catalog), sha256.Sum256(schemas)
	return hex.EncodeToString(a[:]), hex.EncodeToString(b[:])
}
