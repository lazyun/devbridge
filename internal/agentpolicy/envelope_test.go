package agentpolicy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentpolicy"
)

func prospectivePolicy() agentpolicy.Envelope {
	return agentpolicy.Envelope{AgentConfigFingerprint: strings.Repeat("a", 64), Version: agentpolicy.EnvelopeVersion, Policy: agentpolicy.PolicyCooperativeTrustedV1, AgentID: "pi", RunID: "new-run", TaskID: "task", ExecutionID: "execution", RevisionID: "revision", Purpose: "IMPLEMENTATION", CWD: "/work", ReadableRoots: []string{"/work", "/scratch"}, WritableRoots: []string{"/work"}, EngineeringScope: json.RawMessage(` {"paths":["src"], "kind":"PATHS", "number":9007199254740993} `), SourceWorksetHash: "original-workset"}
}

func TestEnvelopeSealDeterministicCanonicalAndSeparateFromInstruction(t *testing.T) {
	input := prospectivePolicy()
	before := append([]byte(nil), input.EngineeringScope...)
	sealed, err := input.Seal()
	if err != nil {
		t.Fatal(err)
	}
	second, err := input.Seal()
	if err != nil || !reflect.DeepEqual(sealed, second) {
		t.Fatalf("nondeterministic seal: %v", err)
	}
	if err = sealed.Validate(); err != nil {
		t.Fatal(err)
	}
	if string(input.EngineeringScope) != string(before) || input.SystemText != "" || input.SHA256 != "" {
		t.Fatal("Seal mutated source envelope")
	}
	// Equivalent object member order/whitespace renders the same canonical bytes.
	input.EngineeringScope = json.RawMessage(`{"number":9007199254740993,"kind":"PATHS","paths":["src"]}`)
	equivalent, err := input.Seal()
	if err != nil || equivalent.SystemText != sealed.SystemText || equivalent.SHA256 != sealed.SHA256 {
		t.Fatalf("canonical scope changed: %v", err)
	}
	if !strings.Contains(sealed.SystemText, "cooperative trusted execution policy (not a sandbox)") || !strings.Contains(sealed.SystemText, "not proof of consumption") || !strings.Contains(sealed.SystemText, "9007199254740993") {
		t.Fatalf("missing trust/receipt/number semantics: %s", sealed.SystemText)
	}
	digest := sha256.Sum256([]byte(sealed.SystemText))
	if sealed.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("digest is not over exact system text")
	}
	typ := reflect.TypeOf(sealed)
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "instruction") || strings.Contains(name, "prompt") {
			t.Fatalf("policy contains user instruction channel: %s", name)
		}
	}
	input.ReadableRoots[0] = "/changed"
	input.WritableRoots[0] = "/changed"
	input.EngineeringScope[0] = '!'
	if sealed.ReadableRoots[0] != "/work" || sealed.WritableRoots[0] != "/work" || err != nil {
		t.Fatal("sealed envelope aliases source slices")
	}
	if err = sealed.Validate(); err != nil {
		t.Fatalf("source mutation changed seal: %v", err)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentpolicy.Envelope
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Validate(); err != nil {
		t.Fatalf("durable roundtrip changed envelope: %v", err)
	}
}

func TestEnvelopeValidationDetectsTextDigestAndAuthorityTampering(t *testing.T) {
	for name, mutate := range map[string]func(*agentpolicy.Envelope){
		"configuration": func(e *agentpolicy.Envelope) { e.AgentConfigFingerprint = strings.Repeat("b", 64) },
		"system bytes":  func(e *agentpolicy.Envelope) { e.SystemText += "\n" },
		"hash":          func(e *agentpolicy.Envelope) { e.SHA256 = strings.Repeat("0", 64) },
		"run":           func(e *agentpolicy.Envelope) { e.RunID = "other-run" },
		"agent":         func(e *agentpolicy.Envelope) { e.AgentID = "codex" },
		"network":       func(e *agentpolicy.Envelope) { e.NetworkAccess = true },
		"writes":        func(e *agentpolicy.Envelope) { e.WritableRoots = []string{"/scratch"} },
		"purpose":       func(e *agentpolicy.Envelope) { e.Purpose = "VERIFICATION" },
		"scope":         func(e *agentpolicy.Envelope) { e.EngineeringScope = json.RawMessage(`{"kind":"ALL"}`) },
		"source":        func(e *agentpolicy.Envelope) { e.SourceWorksetHash = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			sealed, err := prospectivePolicy().Seal()
			if err != nil {
				t.Fatal(err)
			}
			mutate(&sealed)
			if err = sealed.Validate(); err == nil {
				t.Fatal("modified envelope accepted")
			}
		})
	}
}

func TestEnvelopeRejectsInvalidOrImplicitAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*agentpolicy.Envelope){
		"implicit version":         func(e *agentpolicy.Envelope) { e.Version = 0 },
		"unknown version":          func(e *agentpolicy.Envelope) { e.Version = 2 },
		"implicit policy":          func(e *agentpolicy.Envelope) { e.Policy = "" },
		"native historical policy": func(e *agentpolicy.Envelope) { e.Policy = agentpolicy.PolicyTaskSandboxV1 },
		"implicit agent":           func(e *agentpolicy.Envelope) { e.AgentID = "" },
		"unknown agent":            func(e *agentpolicy.Envelope) { e.AgentID = "other" },
		"run required":             func(e *agentpolicy.Envelope) { e.RunID = " " },
		"purpose required":         func(e *agentpolicy.Envelope) { e.Purpose = "" },
		"relative cwd":             func(e *agentpolicy.Envelope) { e.CWD = "work" },
		"unclean cwd":              func(e *agentpolicy.Envelope) { e.CWD = "/work/../work" },
		"unlisted cwd":             func(e *agentpolicy.Envelope) { e.CWD = "/other" },
		"relative readable":        func(e *agentpolicy.Envelope) { e.ReadableRoots = append(e.ReadableRoots, "relative") },
		"unclean readable":         func(e *agentpolicy.Envelope) { e.ReadableRoots = append(e.ReadableRoots, "/a/../b") },
		"duplicate readable":       func(e *agentpolicy.Envelope) { e.ReadableRoots = append(e.ReadableRoots, "/work") },
		"nul path":                 func(e *agentpolicy.Envelope) { e.ReadableRoots = append(e.ReadableRoots, "/bad\x00path") },
		"unreadable writable":      func(e *agentpolicy.Envelope) { e.WritableRoots = []string{"/other"} },
		"unclean writable":         func(e *agentpolicy.Envelope) { e.WritableRoots = []string{"/work/"} },
		"duplicate writable":       func(e *agentpolicy.Envelope) { e.WritableRoots = append(e.WritableRoots, "/work") },
		"readonly writes":          func(e *agentpolicy.Envelope) { e.ReadOnly = true },
		"invalid scope":            func(e *agentpolicy.Envelope) { e.EngineeringScope = json.RawMessage(`{broken`) },
		"multiple scope values":    func(e *agentpolicy.Envelope) { e.EngineeringScope = json.RawMessage(`{} {}`) },
	} {
		t.Run(name, func(t *testing.T) {
			input := prospectivePolicy()
			mutate(&input)
			if _, err := input.Seal(); err == nil {
				t.Fatal("invalid prospective authority sealed")
			}
		})
	}
	for _, id := range []string{"pi", "codex"} {
		input := prospectivePolicy()
		input.AgentID = id
		input.ReadOnly = true
		input.WritableRoots = nil
		input.EngineeringScope = nil
		input.TaskID = ""
		input.ReviewID = "review"
		input.Purpose = "SCOPE_REVIEW"
		sealed, err := input.Seal()
		if err != nil {
			t.Fatal(err)
		}
		if err = sealed.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLaunchReceiptCannotClaimConsumption(t *testing.T) {
	sealed, err := prospectivePolicy().Seal()
	if err != nil {
		t.Fatal(err)
	}
	receipt := agentpolicy.LaunchReceipt{AgentConfigFingerprint: strings.Repeat("a", 64), RunID: sealed.RunID, AgentID: sealed.AgentID, PolicySHA256: sealed.SHA256, Mechanism: "system-prompt-file", Status: agentpolicy.LaunchConfigured}
	if err = receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"", "CONSUMED", "COMPLETED", "ACKNOWLEDGED"} {
		modified := receipt
		modified.Status = status
		if modified.Validate() == nil {
			t.Fatalf("receipt accepted state %q", status)
		}
	}
	for name, mutate := range map[string]func(*agentpolicy.LaunchReceipt){"run": func(r *agentpolicy.LaunchReceipt) { r.RunID = "" }, "agent": func(r *agentpolicy.LaunchReceipt) { r.AgentID = "unknown" }, "mechanism": func(r *agentpolicy.LaunchReceipt) { r.Mechanism = " " }, "digest length": func(r *agentpolicy.LaunchReceipt) { r.PolicySHA256 = "01" }, "digest malformed": func(r *agentpolicy.LaunchReceipt) { r.PolicySHA256 = strings.Repeat("x", 64) }, "digest noncanonical": func(r *agentpolicy.LaunchReceipt) { r.PolicySHA256 = strings.ToUpper(r.PolicySHA256) }} {
		t.Run(name, func(t *testing.T) {
			r := receipt
			mutate(&r)
			if r.Validate() == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestReadOnlyEnvelopeAllowsOnlyExplicitNonSourceScratch(t *testing.T) {
	input := prospectivePolicy()
	input.ReadOnly = true
	input.WritableRoots = nil
	input.ScratchRoots = []string{"/host-scratch/run"}
	sealed, err := input.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err = sealed.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sealed.SystemText, "never write to readable source roots") || !strings.Contains(sealed.SystemText, "writes to explicit scratchRoots remain allowed") {
		t.Fatal("scratch/source semantics missing")
	}
	input.ScratchRoots[0] = "/changed"
	if sealed.ScratchRoots[0] != "/host-scratch/run" {
		t.Fatal("scratch authority aliases caller")
	}
	changed := sealed
	changed.ScratchRoots = []string{"/different-scratch"}
	if changed.Validate() == nil {
		t.Fatal("scratch authority changed without resealing")
	}
	for _, roots := range [][]string{{"relative"}, {"/host-scratch/../other"}, {"/work"}, {"/work/hidden"}, {"/"}, {"/scratch/hidden"}, {"/host-scratch/run", "/host-scratch/run"}, {"/host-scratch", "/host-scratch/run"}} {
		input.ScratchRoots = roots
		if _, err = input.Seal(); err == nil {
			t.Fatalf("invalid or source-disguised scratch accepted: %q", roots)
		}
	}
}
