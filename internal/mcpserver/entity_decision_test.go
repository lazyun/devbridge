package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/executionproposal"
)

type entityDecisionService struct {
	BridgeService
	mu               sync.Mutex
	proposal         executionproposal.Proposal
	approval         approval.Approval
	authorizations   int
	rejections       int
	deliveries       int
	loseResponse     bool
	failBeforeCommit bool
}

func (s *entityDecisionService) GetApprovalForSession(_ context.Context, _, id string) (approval.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approval.ID != id {
		return approval.Approval{}, approval.ErrNotFound
	}
	return approval.Clone(s.approval), nil
}
func (s *entityDecisionService) DecideApprovalForSession(_ context.Context, _, id string, decision approval.Decision) (approval.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failBeforeCommit {
		return approval.Approval{}, errors.New("before commit")
	}
	if id != s.approval.ID || s.approval.Status != approval.StatusPending {
		return approval.Approval{}, approval.ErrAlreadyResolved
	}
	decision, err := approval.NormalizeDecision(s.approval, decision)
	if err != nil {
		return approval.Approval{}, err
	}
	s.deliveries++
	s.approval.Status, s.approval.Decision = approval.StatusDecided, &decision
	if s.loseResponse {
		return approval.Approval{}, errors.New("delivery response lost")
	}
	s.approval.Status = approval.StatusDelivered
	return approval.Clone(s.approval), nil
}

func TestEntityApprovalDecisionResponseLossNeverRedelivers(t *testing.T) {
	for _, option := range []approval.DecisionType{approval.DecisionApproveOnce, approval.DecisionApproveForSession, approval.DecisionDecline, approval.DecisionCancel, approval.DecisionGrantRequestedPermissions} {
		t.Run(string(option), func(t *testing.T) {
			s := &entityDecisionService{approval: approval.Approval{ID: "approval-a", Status: approval.StatusPending, RequestPayload: json.RawMessage(`{"permissions":{"network":true}}`), AllowedDecisions: []approval.DecisionType{option, approval.DecisionDecline, approval.DecisionApproveOnce}}, loseResponse: true}
			_, out, err := ResolveApprovalDecisionCore(context.Background(), s, "session-a", "approval-a", approval.Decision{Type: option})
			if err != nil || out.SubjectState != "DECIDED" || out.RecordedOption != string(option) || out.TransitionError == "" {
				t.Fatalf("delivery loss was promoted to success or lost durable choice: %+v %v", out, err)
			}
			_, replay, err := ResolveApprovalDecisionCore(context.Background(), s, "fresh-session", "approval-a", approval.Decision{Type: option})
			if err != nil || !replay.AlreadyResolved || replay.SubjectState != "DECIDED" || s.deliveries != 1 {
				t.Fatalf("already-decided approval was redelivered: %+v %v", replay, err)
			}
			other := approval.DecisionDecline
			if option == other {
				other = approval.DecisionApproveOnce
			}
			_, conflict, err := ResolveApprovalDecisionCore(context.Background(), s, "fresh-session", "approval-a", approval.Decision{Type: other})
			if err != nil || !conflict.Conflict || s.deliveries != 1 {
				t.Fatalf("changed option was not a subject conflict: %+v %v", conflict, err)
			}
			if option == approval.DecisionGrantRequestedPermissions {
				if _, _, err := ResolveApprovalDecisionCore(context.Background(), s, "fresh-session", "approval-a", approval.Decision{Type: option, Permissions: json.RawMessage(`{"network":false}`)}); err == nil {
					t.Fatal("changed permission payload matched the original decision")
				}
			}
			assertEntityStateHasNoReceipt(t, replay)
		})
	}
}

func TestEntityDecisionConcurrentSubmissionsUseSubjectCAS(t *testing.T) {
	s := &entityDecisionService{approval: approval.Approval{ID: "approval-a", Status: approval.StatusPending, AllowedDecisions: []approval.DecisionType{approval.DecisionDecline}}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, out, err := ResolveApprovalDecisionCore(context.Background(), s, "session", "approval-a", approval.Decision{Type: approval.DecisionDecline}); err != nil || out.Conflict {
				t.Errorf("approval race: %+v %v", out, err)
			}
		}()
	}
	wg.Wait()
	if s.deliveries != 1 {
		t.Fatalf("duplicate side effects: authorizations=%d deliveries=%d", s.authorizations, s.deliveries)
	}
}

func TestEntityDecisionPrecommitFailureDoesNotClaimResolution(t *testing.T) {
	s := &entityDecisionService{approval: approval.Approval{ID: "approval-a", Status: approval.StatusPending, AllowedDecisions: []approval.DecisionType{approval.DecisionDecline}}, failBeforeCommit: true}
	if _, _, err := ResolveApprovalDecisionCore(context.Background(), s, "session", "approval-a", approval.Decision{Type: approval.DecisionDecline}); err == nil {
		t.Fatal("uncommitted approval decision reported success")
	}
}

func TestEntityApprovalSupersessionCannotBeAttributedToCaller(t *testing.T) {
	for _, status := range []approval.Status{approval.StatusCanceled, approval.StatusExpired} {
		s := &entityDecisionService{approval: approval.Approval{ID: "approval-a", Status: status, AllowedDecisions: []approval.DecisionType{approval.DecisionCancel}}}
		_, out, err := ResolveApprovalDecisionCore(context.Background(), s, "session", "approval-a", approval.Decision{Type: approval.DecisionCancel})
		if err != nil || !out.AlreadyResolved || !out.Conflict || out.RecordedOption != "" || out.SubjectState != string(status) || s.deliveries != 0 {
			t.Fatalf("supersession was attributed to a caller choice: %+v %v", out, err)
		}
	}
}

func assertEntityStateHasNoReceipt(t *testing.T, value EntityDecisionOutput) {
	t.Helper()
	encoded, _ := json.Marshal(value)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	for _, key := range []string{"requestId", "resolutionReceipt", "resolvedAt", "accepted", "resolved", "replay"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("entity observation masquerades as request receipt: %s", encoded)
		}
	}
}
