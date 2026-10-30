package bridgehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	bridgegit "github.com/lazyun/devbridge/internal/git"
)

func TestTaskActionConflictsKeepPublicCodeAndHTTPStatus(t *testing.T) {
	for _, code := range []string{"TASK_ACTION_PENDING", "TASK_ACTION_STALE", "TASK_ACTION_CONFLICT"} {
		t.Run(code, func(t *testing.T) {
			cause := confirmation.NewFailure(code, "CORE", "UNIQUE_CONSUMPTION", "Read the current task action.", confirmation.ReadCurrentTarget, nil)
			w := httptest.NewRecorder()
			writeError(w, fmt.Errorf("operation: %w", cause))
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusConflict || body["code"] != code || body["message"] != cause.Message {
				t.Fatalf("task action refusal became server error: status=%d body=%v", w.Code, body)
			}
		})
	}
}

func TestDeliveryConflictsKeepPublicCodeAndHTTPStatus(t *testing.T) {
	for _, cause := range []error{delivery.ErrRejected, delivery.ErrRequestConflict, delivery.ErrRetryConflict, delivery.ErrAbandoned, delivery.ErrPlanReleased, developmentcycle.ErrFinalizationPending, bridgegit.ErrRecoveryRequired, bridgegit.ErrMergeConflict} {
		t.Run(cause.Error(), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, fmt.Errorf("operation: %w", cause))
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusConflict || body["code"] != cause.Error() {
				t.Fatalf("domain refusal became server error: status=%d body=%v", w.Code, body)
			}
		})
	}
	w := httptest.NewRecorder()
	writeError(w, errors.New("unexpected internal failure"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unclassified failure masked: %d", w.Code)
	}
}
