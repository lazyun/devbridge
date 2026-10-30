package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

type UserAnswer struct {
	QuestionID       string `json:"questionId"`
	SelectedOptionID string `json:"selectedOptionId,omitempty"`
	RawText          string `json:"rawText,omitempty"`
}
type UserDecisionInput struct {
	Identity
	RuntimeRequestID    string       `json:"runtimeRequestId"`
	SubmissionRequestID string       `json:"submissionRequestId"`
	ExpectedState       string       `json:"expectedState"`
	PayloadSHA256       string       `json:"payloadSHA256"`
	Action              string       `json:"action"`
	SelectedOptionID    string       `json:"selectedOptionId,omitempty"`
	RawText             string       `json:"rawText,omitempty"`
	Answers             []UserAnswer `json:"answers,omitempty"`
	SourceSurface       string       `json:"sourceSurface"`
	SourceUserEventID   string       `json:"sourceUserEventId,omitempty"`
	SubmittedBy         string       `json:"submittedBy,omitempty"`
}
type UserDecision struct {
	UserDecisionInput
	ID          string    `json:"id"`
	SubmittedAt time.Time `json:"submittedAt"`
}
type DecisionStore interface {
	SubmitUserDecision(context.Context, UserDecisionInput) (UserDecision, error)
	UserDecision(context.Context, string, string) (UserDecision, error)
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func DecisionPayloadSHA256(in UserDecisionInput) string {
	in.PayloadSHA256 = ""
	in.SubmittedBy = ""
	return hashBytes(raw(in))
}
func CompleteDecisionIdentity(id Identity) bool {
	if (id.AppServerInstanceID == "") != (id.AppServerInstanceEpoch == 0) || id.AppServerInstanceEpoch < 0 {
		return false
	}
	if id.RunID == "" || id.ThreadID == "" || id.TurnID == "" || id.ConnectionID == "" || id.RuntimeEpoch == "" || id.OperationID == "" || id.ProjectID == "" || id.TaskID == "" {
		return false
	}
	if id.RecoveryStatusID != "" {
		// Core binds an independent diagnostic to its own Recovery identity.
		// It retains its business owner, but never borrows the source Conversation.
		if id.AgentConversationID != "" {
			return false
		}
		switch id.OperationKind {
		case "EXECUTION":
			return id.ExecutionID == id.OperationID && id.TaskDefinitionRevisionID != "" && id.ReviewID == ""
		case "REVIEW":
			return id.ReviewID == id.OperationID && id.ExecutionID == ""
		case "INSPECTION":
			return id.ExecutionID == "" && id.ReviewID == "" && id.TaskDefinitionRevisionID == ""
		default:
			return false
		}
	}
	switch id.OperationKind {
	case "EXECUTION":
		return id.AgentConversationID != "" && id.ExecutionID == id.OperationID && id.TaskDefinitionRevisionID != ""
	case "REVIEW":
		return id.AgentConversationID != "" && id.ReviewID == id.OperationID
	case "INSPECTION":
		return true
	default:
		return false
	}
}

func (s *Service) SubmitUserDecision(ctx context.Context, in UserDecisionInput) (UserDecision, error) {
	ds, ok := s.store.(DecisionStore)
	if !ok {
		return UserDecision{}, ErrUnsupported
	}
	s.mu.Lock()
	session := s.sessions[in.ConnectionID]
	s.mu.Unlock()
	if session == nil {
		return ds.SubmitUserDecision(ctx, in)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	d, err := ds.SubmitUserDecision(ctx, in)
	if err != nil {
		return d, err
	}
	for i := range session.snapshot.Requests {
		r := &session.snapshot.Requests[i]
		if r.ID == in.RuntimeRequestID {
			r.UserDecision = &d
			if r.State == "WAITING_FOR_USER" {
				r.State = "USER_DECISION_RECORDED"
				r.Delivery = "NOT_DISPATCHED"
			}
			r.AvailableActions = nil
		}
	}
	return d, nil
}
func (s *Service) UserDecision(ctx context.Context, runID, id string) (UserDecision, error) {
	ds, ok := s.store.(DecisionStore)
	if !ok {
		return UserDecision{}, ErrUnsupported
	}
	return ds.UserDecision(ctx, runID, id)
}
func ValidateUserDecision(r Request, in UserDecisionInput) error {
	if (r.OriginalIdentity != nil && !r.PendingReplayVerified) || r.Kind != "CODEX_REQUEST" || !CompleteDecisionIdentity(r.Identity) || r.Identity != in.Identity || r.ID != in.RuntimeRequestID || r.State != "WAITING_FOR_USER" || in.ExpectedState != r.State {
		return ErrStale
	}
	if len(raw(in)) > MaxPayload || !utf8.ValidString(in.RawText) {
		return ErrInvalid
	}
	for _, a := range in.Answers {
		if !utf8.ValidString(a.RawText) {
			return ErrInvalid
		}
	}
	if in.SubmissionRequestID == "" || in.SourceSurface == "" || in.SubmittedBy == "" || in.PayloadSHA256 != DecisionPayloadSHA256(in) {
		return ErrInvalid
	}
	switch in.Action {
	case "RESPOND":
		_, err := SerializeUserDecision(r, UserDecision{UserDecisionInput: in})
		return err
	case "STOP_AND_REVISE", "CANCEL_OPERATION":

		if in.SelectedOptionID != "" || in.RawText != "" || len(in.Answers) > 0 {
			return ErrInvalid
		}
		return nil
	default:
		return ErrInvalid
	}
}
func SerializeUserDecision(r Request, d UserDecision) (json.RawMessage, error) {
	if d.Action != "RESPOND" || d.RuntimeRequestID != r.ID || (d.Identity != r.Identity && (r.OriginalIdentity == nil || d.Identity != *r.OriginalIdentity)) {
		return nil, ErrInvalid
	}
	var result json.RawMessage
	switch r.Method {
	case "item/tool/requestUserInput":
		var p struct {
			Questions []struct {
				ID      string `json:"id"`
				Options []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil, ErrInvalid
		}
		answers := d.Answers
		if len(answers) == 0 && len(p.Questions) == 1 {
			answers = []UserAnswer{{QuestionID: p.Questions[0].ID, SelectedOptionID: d.SelectedOptionID, RawText: d.RawText}}
		} else if d.SelectedOptionID != "" || d.RawText != "" {
			return nil, ErrInvalid
		}
		if len(answers) != len(p.Questions) {
			return nil, ErrInvalid
		}
		out := map[string]any{}
		for _, q := range p.Questions {
			var a *UserAnswer
			for i := range answers {
				if answers[i].QuestionID == q.ID {
					if a != nil {
						return nil, ErrInvalid
					}
					a = &answers[i]
				}
			}
			if a == nil {
				return nil, ErrInvalid
			}
			value := a.RawText
			if a.SelectedOptionID != "" {
				label := ""
				for _, o := range q.Options {
					id := o.ID
					if id == "" {
						id = o.Label
					}
					if id == a.SelectedOptionID {
						label = o.Label
					}
				}
				if label == "" {
					return nil, ErrInvalid
				}
				if value == "" {
					value = label
				}
			}
			if value == "" {
				return nil, ErrInvalid
			}
			out[q.ID] = map[string]any{"answers": []string{value}}
		}
		result = raw(map[string]any{"answers": out})
	case "mcpServer/elicitation/request":
		if len(d.Answers) > 0 {
			return nil, ErrInvalid
		}
		switch d.SelectedOptionID {
		case "accept", "decline", "cancel":
		default:
			return nil, ErrInvalid
		}
		out := map[string]any{"action": d.SelectedOptionID}
		if d.RawText != "" {
			if !json.Valid([]byte(d.RawText)) {
				return nil, fmt.Errorf("%w: elicitation raw text must be exact JSON content", ErrInvalid)
			}
			out["content"] = json.RawMessage(d.RawText)
		}
		result = raw(out)
	default:
		return nil, ErrUnsupported
	}
	if err := validateResponse(r, result, false); err != nil {
		return nil, err
	}
	return result, nil
}
