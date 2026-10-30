package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type decisionMemoryStore struct {
	memoryStore
	decision *UserDecision
}

func (m *decisionMemoryStore) Open(ctx context.Context, id Identity) (Identity, error) {
	id, _ = m.memoryStore.Open(ctx, id)
	id.OperationKind = "EXECUTION"
	id.OperationID = "execution"
	id.ExecutionID = "execution"
	id.TaskDefinitionRevisionID = "revision"
	id.RuntimeEpoch = "epoch"
	id.AgentConversationID = "conversation"
	return id, nil
}
func (m *decisionMemoryStore) SubmitUserDecision(_ context.Context, in UserDecisionInput) (UserDecision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decision != nil {
		if m.decision.PayloadSHA256 == in.PayloadSHA256 && m.decision.SubmissionRequestID == in.SubmissionRequestID {
			return *m.decision, nil
		}
		return UserDecision{}, ErrInvalid
	}
	r := m.state.Requests[0]
	if err := ValidateUserDecision(r, in); err != nil {
		return UserDecision{}, err
	}
	d := UserDecision{UserDecisionInput: in, ID: "decision"}
	m.decision = &d
	m.state.Requests[0].UserDecision = &d
	m.state.Requests[0].State = "USER_DECISION_RECORDED"
	return d, nil
}
func (m *decisionMemoryStore) UserDecision(_ context.Context, run, id string) (UserDecision, error) {
	if m.decision == nil || m.decision.ID != id || m.decision.RunID != run {
		return UserDecision{}, ErrStale
	}
	return *m.decision, nil
}
func TestUserDecisionExactResponseLifecycle(t *testing.T) {
	m := &decisionMemoryStore{}
	svc := NewService(m)
	s := svc.NewTurn(context.Background(), "run", "thread", false)
	defer s.Close()
	if err := s.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	r := addRequest(t, s, "item/tool/requestUserInput")
	in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "user-click", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "  是\nverbatim  ", SourceSurface: "DESKTOP", SubmittedBy: "operator"}
	in.PayloadSHA256 = DecisionPayloadSHA256(in)
	if _, err := svc.Resolve(context.Background(), Resolution{Identity: r.Identity, RequestID: r.RequestID, ItemID: r.ItemID, Response: raw(map[string]any{"answers": map[string]any{"q": map[string]any{"answers": []string{"auto"}}}})}); err == nil {
		t.Fatal("raw response accepted")
	}
	d, err := svc.SubmitUserDecision(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	resolution := Resolution{Identity: r.Identity, RequestID: r.RequestID, ItemID: r.ItemID, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256}
	m.reject = true
	if _, err = svc.Resolve(context.Background(), resolution); err == nil {
		t.Fatal("dispatch proceeded without durable intent")
	}
	m.reject = false
	if _, err = svc.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	response, err := s.Await(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal(response, &decoded) != nil || decoded.Answers["q"].Answers[0] != in.RawText {
		t.Fatalf("rewritten %s", response)
	}
	if _, err = svc.Resolve(context.Background(), resolution); !errors.Is(err, ErrStale) {
		t.Fatal("duplicate waiter delivery", err)
	}
	if err = s.BeforeDispatch(r.RequestID); err != nil {
		t.Fatal(err)
	}
	v, _ := svc.Read(context.Background(), "run")
	if v.Requests[0].State != "RESPONDING" {
		t.Fatal(v.Requests[0].State)
	}
	s.Delivered(r.RequestID, nil)
	v, _ = svc.Read(context.Background(), "run")
	if v.Requests[0].State != "RESPONDING" || ResponseDisposition(v.Requests[0]) != "UNKNOWN" {
		t.Fatal(v.Requests[0].State)
	}
	emit(t, s, "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "server-id"})
	v, _ = svc.Read(context.Background(), "run")
	if v.Requests[0].State != "RESPONDED" {
		t.Fatal(v.Requests[0].State)
	}
}
func TestUserDecisionQuestionCompletenessAndIdentity(t *testing.T) {
	r := Request{ID: "req", Kind: "CODEX_REQUEST", Method: "item/tool/requestUserInput", Payload: raw(map[string]any{"questions": []any{map[string]any{"id": "a", "options": []any{map[string]any{"label": "Option A"}}}, map[string]any{"id": "b", "options": []any{map[string]any{"label": "Option B"}}}}})}
	d := UserDecision{UserDecisionInput: UserDecisionInput{RuntimeRequestID: r.ID, Action: "RESPOND", Answers: []UserAnswer{{QuestionID: "a", SelectedOptionID: "Option A"}, {QuestionID: "b", SelectedOptionID: "Option B"}}}}
	got, err := SerializeUserDecision(r, d)
	if err != nil || string(got) != `{"answers":{"a":{"answers":["Option A"]},"b":{"answers":["Option B"]}}}` {
		t.Fatal(string(got), err)
	}
	d.Answers = d.Answers[:1]
	if _, err = SerializeUserDecision(r, d); err == nil {
		t.Fatal("missing answer accepted")
	}
	d.Answers = append(d.Answers, d.Answers[0])
	if _, err = SerializeUserDecision(r, d); err == nil {
		t.Fatal("duplicate answer accepted")
	}
}

func TestUserDecisionRejectsStaleOwnerAndLegacyRequest(t *testing.T) {
	id := Identity{ProjectID: "p", FeatureAreaID: "f", TaskID: "t", RunID: "r", ThreadID: "th", TurnID: "turn", ConnectionID: "c", OperationKind: "EXECUTION", OperationID: "e", ExecutionID: "e", TaskDefinitionRevisionID: "d", AgentConversationID: "ac", RuntimeEpoch: "epoch"}
	r := Request{Identity: id, ID: "req", Kind: "CODEX_REQUEST", State: "WAITING_FOR_USER", Method: "item/tool/requestUserInput", Payload: requestFixture("item/tool/requestUserInput")}
	base := UserDecisionInput{Identity: id, RuntimeRequestID: "req", SubmissionRequestID: "submit", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "yes", SourceSurface: "DESKTOP", SubmittedBy: "user"}
	base.PayloadSHA256 = DecisionPayloadSHA256(base)
	if err := ValidateUserDecision(r, base); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*UserDecisionInput){"epoch": func(v *UserDecisionInput) { v.RuntimeEpoch = "old" }, "run": func(v *UserDecisionInput) { v.RunID = "other" }, "turn": func(v *UserDecisionInput) { v.TurnID = "other" }, "execution": func(v *UserDecisionInput) { v.ExecutionID = "other" }, "project": func(v *UserDecisionInput) { v.ProjectID = "other" }, "request": func(v *UserDecisionInput) { v.RuntimeRequestID = "other" }, "revision": func(v *UserDecisionInput) { v.TaskDefinitionRevisionID = "other" }}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v := base
			change(&v)
			v.PayloadSHA256 = DecisionPayloadSHA256(v)
			if !errors.Is(ValidateUserDecision(r, v), ErrStale) {
				t.Fatal("stale admitted")
			}
		})
	}
	for _, state := range []string{"UNKNOWN", "RESPONDING", "RESPONDED", "CANCELED", "STALE"} {
		q := r
		q.State = state
		if !errors.Is(ValidateUserDecision(q, base), ErrStale) {
			t.Fatal("terminal admitted", state)
		}
	}
	legacy := r
	legacy.RuntimeEpoch = ""
	if !errors.Is(ValidateUserDecision(legacy, base), ErrStale) {
		t.Fatal("legacy admitted")
	}
	approval := r
	approval.Kind = "RUNTIME_APPROVAL"
	if !errors.Is(ValidateUserDecision(approval, base), ErrStale) {
		t.Fatal("approval accepted as question")
	}
}

func TestUserDecisionDoesNotConsumeOtherRequestOrApproval(t *testing.T) {
	m := &decisionMemoryStore{}
	svc := NewService(m)
	s := svc.NewTurn(context.Background(), "run", "thread", false)
	defer s.Close()
	if err := s.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ id, method string }{{"question-a", "item/tool/requestUserInput"}, {"question-b", "item/tool/requestUserInput"}, {"permission", "item/commandExecution/requestApproval"}} {
		if err := s.Observe(Event{Method: entry.method, RequestID: raw(entry.id), Params: requestFixture(entry.method)}); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := svc.Read(context.Background(), "run")
	if len(v.Requests) != 3 || v.Requests[0].ID == v.Requests[1].ID || v.Requests[2].Kind != "RUNTIME_APPROVAL" {
		t.Fatal("identity/kind collision")
	}
	r := v.Requests[0]
	in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "one-explicit-answer", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "yes", SourceSurface: "WEBUI", SubmittedBy: "operator"}
	in.PayloadSHA256 = DecisionPayloadSHA256(in)
	d, err := svc.SubmitUserDecision(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	other := v.Requests[1]
	if _, err = svc.Resolve(context.Background(), Resolution{Identity: other.Identity, RequestID: other.RequestID, ItemID: other.ItemID, RuntimeRequestID: other.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256}); err == nil {
		t.Fatal("one answer consumed another question")
	}
	if _, err = svc.Resolve(context.Background(), Resolution{Identity: r.Identity, RequestID: r.RequestID, ItemID: r.ItemID, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256}); err != nil {
		t.Fatal(err)
	}
	v, _ = svc.Read(context.Background(), "run")
	if v.Requests[1].State != "WAITING_FOR_USER" || v.Requests[1].UserDecision != nil || v.Requests[2].State != "PENDING" {
		t.Fatal("unrelated request consumed")
	}
}

func TestUserStopCannotBecomeResponded(t *testing.T) {
	for _, action := range []string{"STOP_AND_REVISE", "CANCEL_OPERATION"} {
		t.Run(action, func(t *testing.T) {
			m := &decisionMemoryStore{}
			svc := NewService(m)
			s := svc.NewTurn(context.Background(), "run", "thread", false)
			defer s.Close()
			if err := s.Bind("turn"); err != nil {
				t.Fatal(err)
			}
			r := addRequest(t, s, "item/tool/requestUserInput")
			in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "stop", ExpectedState: "WAITING_FOR_USER", Action: action, SourceSurface: "DESKTOP", SubmittedBy: "user"}
			in.PayloadSHA256 = DecisionPayloadSHA256(in)
			d, err := svc.SubmitUserDecision(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = SerializeUserDecision(r, d); err == nil {
				t.Fatal("stop serialized")
			}
			emit(t, s, "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "server-id"})
			v, _ := svc.Read(context.Background(), "run")
			if v.Requests[0].State != "CANCELED" {
				t.Fatal(v.Requests[0].State)
			}
			s.Delivered(r.RequestID, nil)
			emit(t, s, "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "server-id"})
			v, _ = svc.Read(context.Background(), "run")
			if v.Requests[0].State != "CANCELED" {
				t.Fatal("late callback revived", v.Requests[0].State)
			}
		})
	}
}

func TestUserDecisionElicitationPreservesIntegerContent(t *testing.T) {
	r := Request{ID: "elicitation", Method: "mcpServer/elicitation/request", Payload: json.RawMessage(`{"mode":"form","requestedSchema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}}`)}
	d := UserDecision{UserDecisionInput: UserDecisionInput{RuntimeRequestID: r.ID, Action: "RESPOND", SelectedOptionID: "accept", RawText: `{"value":9007199254740993}`}}
	response, err := SerializeUserDecision(r, d)
	if err != nil || string(response) != `{"action":"accept","content":{"value":9007199254740993}}` {
		t.Fatalf("explicit JSON content changed: %s %v", response, err)
	}
}
