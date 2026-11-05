package bridgehttp

import (
	"net/http/httptest"
	"testing"
)

// This finite matrix uses Recorder only for final 2xx-5xx statuses. It does
// not invoke handlers, requests, middleware, logging, sanitizers, or servers.
func TestStatusCaptureFinalResponseStatus(t *testing.T) {
	type step struct {
		status int
		body   string
		write  bool
	}
	tests := []struct {
		name        string
		steps       []step
		wantCode    int
		wantCapture int
		wantBody    string
	}{
		{name: "control_no_op", wantCode: 200, wantCapture: 0},
		{name: "control_single_200", steps: []step{{status: 200}, {write: true, body: "ok"}}, wantCode: 200, wantCapture: 200, wantBody: "ok"},
		{name: "control_single_201", steps: []step{{status: 201}, {write: true, body: "created"}}, wantCode: 201, wantCapture: 201, wantBody: "created"},
		{name: "control_single_404", steps: []step{{status: 404}}, wantCode: 404, wantCapture: 404},
		{name: "control_single_500", steps: []step{{status: 500}}, wantCode: 500, wantCapture: 500},
		{name: "control_plain_write", steps: []step{{write: true, body: "payload"}}, wantCode: 200, wantCapture: 200, wantBody: "payload"},
		{name: "control_zero_length_write", steps: []step{{write: true}}, wantCode: 200, wantCapture: 200},
		{name: "control_same_final_twice", steps: []step{{status: 204}, {status: 204}}, wantCode: 204, wantCapture: 204},
		{name: "late_final_201_then_500", steps: []step{{status: 201}, {status: 500}, {write: true, body: "created"}}, wantCode: 201, wantCapture: 201, wantBody: "created"},
		{name: "late_final_500_then_200", steps: []step{{status: 500}, {status: 200}, {write: true, body: "error"}}, wantCode: 500, wantCapture: 500, wantBody: "error"},
		{name: "late_final_301_then_404", steps: []step{{status: 301}, {status: 404}, {write: true, body: "redirect"}}, wantCode: 301, wantCapture: 301, wantBody: "redirect"},
		{name: "late_final_implicit_200_then_503", steps: []step{{write: true, body: "body"}, {status: 503}}, wantCode: 200, wantCapture: 200, wantBody: "body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			capture := &statusCapture{ResponseWriter: recorder}
			if capture.Unwrap() != recorder {
				t.Fatal("Unwrap did not preserve the underlying ResponseWriter")
			}
			for index, op := range tt.steps {
				if op.write {
					n, err := capture.Write([]byte(op.body))
					if err != nil || n != len(op.body) {
						t.Errorf("step %d: Write = (%d, %v), want (%d, nil)", index, n, err, len(op.body))
					}
				} else {
					capture.WriteHeader(op.status)
				}
			}
			if recorder.Code != tt.wantCode {
				t.Errorf("Recorder status = %d, want first final status %d", recorder.Code, tt.wantCode)
			}
			if capture.status != tt.wantCapture {
				t.Errorf("captured status = %d, want %d; Recorder committed status = %d", capture.status, tt.wantCapture, recorder.Code)
			}
			if got := recorder.Body.String(); got != tt.wantBody {
				t.Errorf("response body = %q, want %q", got, tt.wantBody)
			}
			if capture.Unwrap() != recorder {
				t.Error("Unwrap changed the underlying ResponseWriter after writes")
			}
		})
	}
}
