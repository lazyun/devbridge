package agentruntime

import (
	"reflect"
	"testing"
	"time"
)

func TestHealthIsPureAndWaitingPrecedesStalled(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, status, connection string
		age                      time.Duration
		pending                  bool
		want                     string
	}{
		{"recent", "RUNNING", "CONNECTED", time.Minute, false, "ACTIVE"},
		{"threshold", "RUNNING", "CONNECTED", 30 * time.Minute, false, "ACTIVE"},
		{"stalled", "RUNNING", "CONNECTED", 43 * time.Minute, false, "POSSIBLY_STALLED"},
		{"waiting", "RUNNING", "CONNECTED", 43 * time.Minute, true, "WAITING"},
		{"terminal", "SUCCEEDED", "CONNECTED", 43 * time.Minute, false, "ACTIVE"},
		{"unknown", "RUNNING", "CONNECTION_LOST", 43 * time.Minute, false, "ACTIVE"},
		{"orphan", "UNKNOWN_ORPHANED", "CONNECTED", 43 * time.Minute, false, "ACTIVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Snapshot{ConnectionState: tc.connection, LastActivityAt: now.Add(-tc.age)}
			if tc.pending {
				v.Requests = []Request{{State: "PENDING", WaitingState: "WAITING_FOR_PERMISSION"}}
			}
			before := v
			if got := Health(v, tc.status, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if !reflect.DeepEqual(before, v) {
				t.Fatal("health mutated authority")
			}
		})
	}
}
