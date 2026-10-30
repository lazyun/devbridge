package confirmation

import (
	"encoding/json"
	"testing"
)

func resultKeys(t *testing.T, r Result) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range object {
		keys[k] = true
	}
	return keys
}

// A read or commit envelope (get/options/show/commit) must not carry the
// prepare-only contract facts, so the Card entry decoder's four-key contract is
// unchanged for every envelope the Card actually binds.
func TestReadAndCommitEnvelopesOmitPrepareContract(t *testing.T) {
	withIntent := Result{Intent: &Intent{IntentID: "confirm_x", State: "PENDING"}}
	for _, name := range []string{"withIntent", "withFailure"} {
		var r Result
		if name == "withIntent" {
			r = withIntent
		} else {
			r = Result{Failure: NewFailure("CODE", "CORE", "RECEIPT_READBACK", "msg", ReadOriginalReceipt, nil)}
		}
		keys := resultKeys(t, r)
		for _, forbidden := range []string{"businessOutcome", "factsComplete", "presentationReady"} {
			if keys[forbidden] {
				t.Fatalf("%s envelope leaked prepare contract key %q", name, forbidden)
			}
		}
	}
}

// A successful prepare envelope reports the Core-computed contract facts.
func TestPrepareEnvelopeCarriesContract(t *testing.T) {
	truth := true
	prepared := Result{
		Intent:            &Intent{IntentID: "confirm_x", State: "PENDING"},
		BusinessOutcome:   Prepared,
		FactsComplete:     &truth,
		PresentationReady: &truth,
	}
	keys := resultKeys(t, prepared)
	for _, want := range []string{"intent", "businessOutcome", "factsComplete", "presentationReady"} {
		if !keys[want] {
			t.Fatalf("prepare envelope missing %q: %v", want, keys)
		}
	}
}
