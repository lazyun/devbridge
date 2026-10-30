package mcpserver

import (
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type SemanticTaskOutput struct {
	RefreshRequired     bool               `json:"refreshRequired,omitempty"`
	RequiredReviewDepth *task.ReviewDepth  `json:"requiredReviewDepth"`
	AllowedReviewDepths []task.ReviewDepth `json:"allowedReviewDepths"`
	ID                  string             `json:"id"`
	FeatureAreaID       string             `json:"featureAreaId"`
	Title               string             `json:"title"`
	Status              string             `json:"status"`
	Phase               string             `json:"phase"`
	NextAction          wp.Action          `json:"nextAction"`
}

const SemanticTaskOutputSchema = `{"type":"object","properties":{"refreshRequired":{"type":"boolean"},"requiredReviewDepth":{"type":["string","null"],"enum":["FAST","DEEP",null]},"allowedReviewDepths":{"type":"array","items":{"type":"string","enum":["FAST","DEEP"]}},"id":{"type":"string"},"featureAreaId":{"type":"string"},"title":{"type":"string"},"status":{"type":"string"},"phase":{"type":"string"},"nextAction":` + wp.ActionJSONSchema + `},"required":["requiredReviewDepth","allowedReviewDepths","id","featureAreaId","title","status","phase","nextAction"],"additionalProperties":false}`
