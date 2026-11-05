package bridgehttp

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type capturedDelegation struct {
	method string
	status int
	body   string
}

// delegationSpy records calls without interpreting status codes. Literal test
// expectations come from the net/http contract, not from this spy's behavior.
type delegationSpy struct {
	header http.Header
	calls  []capturedDelegation
}

func (s *delegationSpy) Header() http.Header { return s.header }
func (s *delegationSpy) WriteHeader(status int) {
	s.calls = append(s.calls, capturedDelegation{method: "header", status: status})
}
func (s *delegationSpy) Write(body []byte) (int, error) {
	s.calls = append(s.calls, capturedDelegation{method: "body", body: string(body)})
	return len(body), nil
}

func TestStatusCaptureInformationalAndSwitchingContract(t *testing.T) {
	type step struct {
		status     int
		body       string
		write      bool
		wantStatus int
	}
	tests := []struct {
		name      string
		steps     []step
		wantCalls []capturedDelegation
	}{
		{
			name: "continue_then_created_then_error",
			steps: []step{
				{status: 100, wantStatus: 0},
				{status: 201, wantStatus: 201},
				{status: 500, wantStatus: 201},
			},
			wantCalls: []capturedDelegation{
				{method: "header", status: 100},
				{method: "header", status: 201},
				{method: "header", status: 500},
			},
		},
		{
			name: "early_hints_then_body_then_error",
			steps: []step{
				{status: 103, wantStatus: 0},
				{write: true, body: "payload", wantStatus: 200},
				{status: 503, wantStatus: 200},
			},
			wantCalls: []capturedDelegation{
				{method: "header", status: 103},
				{method: "body", body: "payload"},
				{method: "header", status: 503},
			},
		},
		{
			name: "multiple_informational_then_final",
			steps: []step{
				{status: 100, wantStatus: 0},
				{status: 103, wantStatus: 0},
				{status: 201, wantStatus: 201},
				{status: 103, wantStatus: 201},
			},
			wantCalls: []capturedDelegation{
				{method: "header", status: 100},
				{method: "header", status: 103},
				{method: "header", status: 201},
				{method: "header", status: 103},
			},
		},
		{
			name: "switching_protocols_is_final",
			steps: []step{
				{status: 101, wantStatus: 101},
				{status: 200, wantStatus: 101},
				{write: true, body: "upgraded", wantStatus: 101},
			},
			wantCalls: []capturedDelegation{
				{method: "header", status: 101},
				{method: "header", status: 200},
				{method: "body", body: "upgraded"},
			},
		},
		{
			name: "final_then_informational_then_error",
			steps: []step{
				{status: 204, wantStatus: 204},
				{status: 103, wantStatus: 204},
				{status: 500, wantStatus: 204},
			},
			wantCalls: []capturedDelegation{
				{method: "header", status: 204},
				{method: "header", status: 103},
				{method: "header", status: 500},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &delegationSpy{header: make(http.Header)}
			capture := &statusCapture{ResponseWriter: spy}
			for index, op := range tt.steps {
				if op.write {
					n, err := capture.Write([]byte(op.body))
					if n != len(op.body) || err != nil {
						t.Errorf("step %d: Write = (%d, %v), want (%d, nil)", index, n, err, len(op.body))
					}
				} else {
					capture.WriteHeader(op.status)
				}
				if capture.status != op.wantStatus {
					t.Errorf("step %d: captured status = %d, want literal contract status %d", index, capture.status, op.wantStatus)
				}
				if !reflect.DeepEqual(spy.calls, tt.wantCalls[:index+1]) {
					t.Errorf("step %d: delegated calls = %#v, want %#v", index, spy.calls, tt.wantCalls[:index+1])
				}
			}
			if capture.Unwrap() != spy {
				t.Error("Unwrap did not preserve the delegation spy")
			}
		})
	}
}

// Recorder is used here only to reject invalid code 99, never as a 1xx oracle.
func TestStatusCaptureRejectedHeaderDoesNotMutateStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	capture := &statusCapture{ResponseWriter: recorder}
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		capture.WriteHeader(99)
	}()
	if !panicked {
		t.Fatal("invalid header did not propagate the underlying writer panic")
	}
	if capture.status != 0 {
		t.Errorf("captured status after rejected header = %d, want 0", capture.status)
	}
	if recorder.Code != 200 {
		t.Errorf("Recorder code after rejection = %d, want unchanged initial 200", recorder.Code)
	}
}
