package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// This harness is the single source of truth for Host/Card mount-parity (§4): it
// loads testdata/confirmation_card_contract_v5.json — the SAME file the Card
// JavaScript test loads — applies each declared mutation to the named fixture, and
// asserts the Go mount-gate validator reaches the SAME accept/reject decision (and,
// on rejection, the SAME machine reason code) as the Card for every MOUNT_ABSOLUTE
// vector. Hand-written Go/JS expectation tables are forbidden; if a decision must
// change, the vector file changes and both runtimes re-derive it here.

type contractOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type contractVector struct {
	ID                  string         `json:"id"`
	Kind                string         `json:"kind"`
	Scope               string         `json:"scope"`
	Fixture             string         `json:"fixture"`
	Mutation            []contractOp   `json:"mutation"`
	BoundEntry          map[string]any `json:"boundEntry"`
	PendingCommit       map[string]any `json:"pendingCommit"`
	ExpectedAccepted    bool           `json:"expectedAccepted"`
	ExpectedDisposition string         `json:"expectedDisposition"`
	ExpectedReasonCode  string         `json:"expectedReasonCode"`
}

type contractFile struct {
	Meta struct {
		RelativeReasonCodes []string `json:"relativeReasonCodes"`
	} `json:"_meta"`
	Fixtures map[string]map[string]any `json:"fixtures"`
	Vectors  []contractVector          `json:"vectors"`
}

func loadContractFile(t *testing.T) contractFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/confirmation_card_contract_v5.json")
	if err != nil {
		t.Fatalf("read shared contract vectors: %v", err)
	}
	var file contractFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode shared contract vectors: %v", err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("contract vector file is empty")
	}
	return file
}

type pathSeg struct {
	key     string
	index   int
	isIndex bool
}

func parseContractPath(path string) ([]pathSeg, error) {
	var segs []pathSeg
	for _, part := range strings.Split(path, ".") {
		name := part
		bracket := strings.IndexByte(part, '[')
		if bracket >= 0 {
			name = part[:bracket]
		}
		if name != "" {
			segs = append(segs, pathSeg{key: name})
		}
		if bracket >= 0 {
			rest := part[bracket:]
			for strings.HasPrefix(rest, "[") {
				end := strings.IndexByte(rest, ']')
				if end < 0 {
					return nil, fmt.Errorf("unbalanced index in %q", path)
				}
				n, err := strconv.Atoi(rest[1:end])
				if err != nil {
					return nil, fmt.Errorf("bad index in %q: %v", path, err)
				}
				segs = append(segs, pathSeg{index: n, isIndex: true})
				rest = rest[end+1:]
			}
		}
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return segs, nil
}

func descend(root any, segs []pathSeg) (any, error) {
	cur := root
	for _, s := range segs {
		if s.isIndex {
			arr, ok := cur.([]any)
			if !ok || s.index < 0 || s.index >= len(arr) {
				return nil, fmt.Errorf("index %d out of range", s.index)
			}
			cur = arr[s.index]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot key %q into non-object", s.key)
		}
		v, ok := m[s.key]
		if !ok {
			return nil, fmt.Errorf("missing key %q", s.key)
		}
		cur = v
	}
	return cur, nil
}

func setLeaf(container any, leaf pathSeg, value any) error {
	if leaf.isIndex {
		arr, ok := container.([]any)
		if !ok || leaf.index < 0 || leaf.index >= len(arr) {
			return fmt.Errorf("cannot set array index")
		}
		arr[leaf.index] = value
		return nil
	}
	m, ok := container.(map[string]any)
	if !ok {
		return fmt.Errorf("cannot set object key on non-object")
	}
	m[leaf.key] = value
	return nil
}

func applyContractMutation(root map[string]any, op contractOp) error {
	segs, err := parseContractPath(op.Path)
	if err != nil {
		return err
	}
	switch op.Op {
	case "set":
		parent, err := descend(root, segs[:len(segs)-1])
		if err != nil {
			return err
		}
		var value any
		if len(op.Value) > 0 {
			if err := json.Unmarshal(op.Value, &value); err != nil {
				return err
			}
		}
		return setLeaf(parent, segs[len(segs)-1], value)
	case "delete":
		parent, err := descend(root, segs[:len(segs)-1])
		if err != nil {
			return err
		}
		leaf := segs[len(segs)-1]
		m, ok := parent.(map[string]any)
		if !ok || leaf.isIndex {
			return fmt.Errorf("delete requires an object key")
		}
		delete(m, leaf.key)
		return nil
	case "push", "pushAt":
		target, err := descend(root, segs)
		if err != nil {
			return err
		}
		arr, ok := target.([]any)
		if !ok {
			return fmt.Errorf("push target is not an array")
		}
		var value any
		if err := json.Unmarshal(op.Value, &value); err != nil {
			return err
		}
		parent, err := descend(root, segs[:len(segs)-1])
		if err != nil {
			return err
		}
		return setLeaf(parent, segs[len(segs)-1], append(arr, value))
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

// cloneFixture returns an independent deep copy so each vector starts from a clean
// fixture and mutations never leak across cases.
func cloneFixture(t *testing.T, fixture map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var copy map[string]any
	if err := json.Unmarshal(raw, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestGoCardEntryContractVectorsMatchSharedFile(t *testing.T) {
	file := loadContractFile(t)
	relative := map[string]bool{}
	for _, code := range file.Meta.RelativeReasonCodes {
		relative[code] = true
	}
	seenIDs := map[string]bool{}
	var absolute, boundCount int
	for _, vector := range file.Vectors {
		if seenIDs[vector.ID] {
			t.Fatalf("duplicate vector id %q", vector.ID)
		}
		seenIDs[vector.ID] = true
		fixture, ok := file.Fixtures[vector.Fixture]
		if !ok {
			t.Fatalf("vector %q references unknown fixture %q", vector.ID, vector.Fixture)
		}
		envelope := cloneFixture(t, fixture)
		for _, op := range vector.Mutation {
			if err := applyContractMutation(envelope, op); err != nil {
				t.Fatalf("vector %q mutation: %v", vector.ID, err)
			}
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("vector %q marshal: %v", vector.ID, err)
		}
		var failure *c.Failure
		switch vector.Kind {
		case "INTENT_ENTRY", "SUBMISSION":
			failure = ValidateConfirmationCardEntryProjectionJSON(raw)
		case "OPTIONS":
			failure = ValidateConfirmationOptionsProjectionJSON(raw)
		default:
			t.Fatalf("vector %q unknown kind %q", vector.ID, vector.Kind)
		}
		accepted := failure == nil

		switch vector.Scope {
		case "MOUNT_ABSOLUTE":
			absolute++
			if accepted != vector.ExpectedAccepted {
				t.Fatalf("vector %q: Go accepted=%v want=%v (failure=%+v)", vector.ID, accepted, vector.ExpectedAccepted, failure)
			}
			if !accepted {
				if failure.ReasonCode != vector.ExpectedReasonCode {
					t.Fatalf("vector %q: Go reasonCode=%q want=%q", vector.ID, failure.ReasonCode, vector.ExpectedReasonCode)
				}
				wantCode := "CONFIRMATION_CARD_ENTRY_PROJECTION_INVALID"
				if vector.Kind == "OPTIONS" {
					wantCode = "CONFIRMATION_OPTIONS_PROJECTION_INVALID"
				}
				if failure.Code != wantCode {
					t.Fatalf("vector %q: Go code=%q want=%q", vector.ID, failure.Code, wantCode)
				}
			}
		case "BOUND_ENTRY_RELATIVE":
			// The single-read mount gate holds no prior entry/pendingCommit, so it
			// MUST stay blind to these decisions: it accepts the same envelope the
			// absolute path accepts. If it ever rejects one, the boundary is broken
			// and coverage is being over-claimed. The Card test asserts the actual
			// rejection against a bound context.
			boundCount++
			if !relative[vector.ExpectedReasonCode] {
				t.Fatalf("vector %q: reason %q is not a declared relative reason code", vector.ID, vector.ExpectedReasonCode)
			}
			if !accepted {
				t.Fatalf("vector %q: Go single-read gate rejected a BOUND_ENTRY_RELATIVE case (%s) — it must not claim coverage of bound staleness", vector.ID, failure.ReasonCode)
			}
		default:
			t.Fatalf("vector %q unknown scope %q", vector.ID, vector.Scope)
		}
	}
	if absolute == 0 || boundCount == 0 {
		t.Fatalf("contract coverage regression: absolute=%d bound=%d", absolute, boundCount)
	}
}

// TestGoAbsoluteValidatorNeverLeaksFeedback proves §5's content-free guarantee for
// every rejected MOUNT_ABSOLUTE vector: the projected message must carry only the
// structural jsonPath and must never echo an intent payload value.
func TestGoAbsoluteValidatorNeverLeaksFeedback(t *testing.T) {
	file := loadContractFile(t)
	for _, vector := range file.Vectors {
		if vector.Scope != "MOUNT_ABSOLUTE" || vector.ExpectedAccepted {
			continue
		}
		fixture := file.Fixtures[vector.Fixture]
		envelope := cloneFixture(t, fixture)
		for _, op := range vector.Mutation {
			if err := applyContractMutation(envelope, op); err != nil {
				t.Fatalf("vector %q: %v", vector.ID, err)
			}
		}
		raw, _ := json.Marshal(envelope)
		var failure *c.Failure
		if vector.Kind == "OPTIONS" {
			failure = ValidateConfirmationOptionsProjectionJSON(raw)
		} else {
			failure = ValidateConfirmationCardEntryProjectionJSON(raw)
		}
		if failure == nil {
			t.Fatalf("vector %q expected rejection but Go accepted", vector.ID)
		}
		if failure.Message == "" {
			t.Fatalf("vector %q empty message", vector.ID)
		}
		for _, secret := range []string{"reconsider", "boundary handling", "Please"} {
			if strings.Contains(failure.Message, secret) {
				t.Fatalf("vector %q message leaked payload text %q: %s", vector.ID, secret, failure.Message)
			}
		}
	}
}
