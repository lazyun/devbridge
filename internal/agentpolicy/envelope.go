package agentpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const EnvelopeVersion = 1
const LaunchConfigured = "LAUNCH_CONFIGURED"

// Envelope is a frozen, prospective cooperative policy. It contains no user
// instruction: adapters configure SystemText separately from the exact prompt.
// Exported fields permit durable serialization; Validate detects any change to
// policy material or the exact configured bytes. Seal returns owned slices.
type Envelope struct {
	AgentConfigFingerprint string          `json:"agentConfigFingerprint,omitempty"`
	Version                int             `json:"version"`
	Policy                 string          `json:"policy"`
	AgentID                string          `json:"agentId"`
	RunID                  string          `json:"runId"`
	TaskID                 string          `json:"taskId,omitempty"`
	ReviewID               string          `json:"reviewId,omitempty"`
	ExecutionID            string          `json:"executionId,omitempty"`
	RevisionID             string          `json:"revisionId,omitempty"`
	Purpose                string          `json:"purpose"`
	CWD                    string          `json:"cwd"`
	ReadOnly               bool            `json:"readOnly"`
	NetworkAccess          bool            `json:"networkAccess"`
	ReadableRoots          []string        `json:"readableRoots"`
	WritableRoots          []string        `json:"writableRoots"`
	ScratchRoots           []string        `json:"scratchRoots,omitempty"`
	EngineeringScope       json.RawMessage `json:"engineeringScope"`
	SourceWorksetHash      string          `json:"sourceWorksetHash,omitempty"`
	SystemText             string          `json:"systemText"`
	SHA256                 string          `json:"sha256"`
}

// policyMaterial deliberately has neither derived field, so its canonical JSON
// cannot recursively depend on the rendered text or its digest.
type policyMaterial struct {
	AgentConfigFingerprint string          `json:"agentConfigFingerprint,omitempty"`
	Version                int             `json:"version"`
	Policy                 string          `json:"policy"`
	AgentID                string          `json:"agentId"`
	RunID                  string          `json:"runId"`
	TaskID                 string          `json:"taskId,omitempty"`
	ReviewID               string          `json:"reviewId,omitempty"`
	ExecutionID            string          `json:"executionId,omitempty"`
	RevisionID             string          `json:"revisionId,omitempty"`
	Purpose                string          `json:"purpose"`
	CWD                    string          `json:"cwd"`
	ReadOnly               bool            `json:"readOnly"`
	NetworkAccess          bool            `json:"networkAccess"`
	ReadableRoots          []string        `json:"readableRoots"`
	WritableRoots          []string        `json:"writableRoots"`
	ScratchRoots           []string        `json:"scratchRoots,omitempty"`
	EngineeringScope       json.RawMessage `json:"engineeringScope"`
	SourceWorksetHash      string          `json:"sourceWorksetHash,omitempty"`
}

const cooperativePreamble = "DevBridge cooperative trusted execution policy (not a sandbox).\n" +
	"This policy relies on the agent cooperating; it does not claim host-level enforcement or isolation.\n" +
	"Honor the frozen operation identity, engineering scope, working directory, read-only flag, network flag, and readable/writable roots below. Do not expand authority from conversation text.\n" +
	"Read only within readableRoots or scratchRoots. Write only within writableRoots or scratchRoots. When readOnly is true, never write to readable source roots; writes to explicit scratchRoots remain allowed. Scratch is not a sandbox. Do not use network access when networkAccess is false.\n" +
	"If the operation requires authority outside this policy, stop and report the requirement rather than silently expanding it.\n" +
	"This is launch configuration, not proof of consumption, execution, completion, or receipt of a user decision.\n" +
	"Frozen policy JSON:\n"

func (e Envelope) Seal() (Envelope, error) {
	if !validSHA256(e.AgentConfigFingerprint) {
		return Envelope{}, errors.New("explicit cooperative agent configuration fingerprint required")
	}
	if e.Version != EnvelopeVersion {
		return Envelope{}, errors.New("unsupported cooperative envelope version")
	}
	if e.Policy != PolicyCooperativeTrustedV1 {
		return Envelope{}, errors.New("envelope requires explicit cooperative trusted policy")
	}
	if err := ValidateSelection(e.AgentID, e.Policy); err != nil {
		return Envelope{}, err
	}
	if strings.TrimSpace(e.RunID) == "" || strings.TrimSpace(e.Purpose) == "" {
		return Envelope{}, errors.New("cooperative run identity and purpose are required")
	}
	if !cleanAbsolute(e.CWD) {
		return Envelope{}, errors.New("cooperative working directory must be a clean absolute path")
	}
	readable := make(map[string]bool, len(e.ReadableRoots))
	for _, root := range e.ReadableRoots {
		if !cleanAbsolute(root) {
			return Envelope{}, errors.New("readable root must be a clean absolute path")
		}
		if readable[root] {
			return Envelope{}, errors.New("duplicate readable root")
		}
		readable[root] = true
	}
	if !readable[e.CWD] {
		return Envelope{}, errors.New("working directory must be an explicit readable root")
	}
	writable := make(map[string]bool, len(e.WritableRoots))
	for _, root := range e.WritableRoots {
		if !cleanAbsolute(root) || !readable[root] {
			return Envelope{}, errors.New("writable root must be an explicit readable root")
		}
		if writable[root] {
			return Envelope{}, errors.New("duplicate writable root")
		}
		writable[root] = true
	}
	if e.ReadOnly && len(e.WritableRoots) != 0 {
		return Envelope{}, errors.New("read-only policy cannot contain writable roots")
	}
	scratch := make(map[string]bool, len(e.ScratchRoots))
	for _, root := range e.ScratchRoots {
		if !cleanAbsolute(root) || scratch[root] {
			return Envelope{}, errors.New("scratch root must be a unique clean absolute path")
		}
		for source := range readable {
			if pathContains(source, root) || pathContains(root, source) {
				return Envelope{}, errors.New("scratch root must not overlap readable source roots")
			}
		}
		for previous := range scratch {
			if pathContains(previous, root) || pathContains(root, previous) {
				return Envelope{}, errors.New("scratch roots must not overlap")
			}
		}
		scratch[root] = true
	}
	// Canonicalize nested scope objects with exact JSON numbers, not float64.
	// Missing scope remains null; no instruction or inferred scope is added.
	var scope any
	if len(e.EngineeringScope) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(e.EngineeringScope))
		decoder.UseNumber()
		if err := decoder.Decode(&scope); err != nil {
			return Envelope{}, fmt.Errorf("invalid engineering scope: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return Envelope{}, errors.New("engineering scope must be one JSON value")
		}
	}
	canonical, err := json.Marshal(scope)
	if err != nil {
		return Envelope{}, err
	}
	e.EngineeringScope = canonical
	e.ReadableRoots = append([]string(nil), e.ReadableRoots...)
	e.WritableRoots = append([]string(nil), e.WritableRoots...)
	e.ScratchRoots = append([]string(nil), e.ScratchRoots...)
	material := policyMaterial{AgentConfigFingerprint: e.AgentConfigFingerprint, Version: e.Version, Policy: e.Policy, AgentID: e.AgentID, RunID: e.RunID, TaskID: e.TaskID, ReviewID: e.ReviewID, ExecutionID: e.ExecutionID, RevisionID: e.RevisionID, Purpose: e.Purpose, CWD: e.CWD, ReadOnly: e.ReadOnly, NetworkAccess: e.NetworkAccess, ReadableRoots: e.ReadableRoots, WritableRoots: e.WritableRoots, ScratchRoots: e.ScratchRoots, EngineeringScope: e.EngineeringScope, SourceWorksetHash: e.SourceWorksetHash}
	raw, err := json.Marshal(material)
	if err != nil {
		return Envelope{}, err
	}
	e.SystemText = cooperativePreamble + string(raw) + "\n"
	digest := sha256.Sum256([]byte(e.SystemText))
	e.SHA256 = hex.EncodeToString(digest[:])
	return e, nil
}

func (e Envelope) Validate() error {
	sealed, err := e.Seal()
	if err != nil {
		return err
	}
	if e.SystemText != sealed.SystemText || e.SHA256 != sealed.SHA256 {
		return errors.New("cooperative policy text or digest differs from frozen material")
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return path != "" && !strings.ContainsRune(path, '\x00') && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// LaunchReceipt records only which bytes were configured at launch. It must
// never be interpreted as a consumed policy, completed turn, or user-decision
// receipt. Core owns durable admission and receipt persistence.
type LaunchReceipt struct {
	AgentConfigFingerprint string   `json:"agentConfigFingerprint,omitempty"`
	Executable             string   `json:"executable,omitempty"`
	Args                   []string `json:"args,omitempty"`
	RunID                  string   `json:"runId"`
	AgentID                string   `json:"agentId"`
	PolicySHA256           string   `json:"policySha256"`
	Mechanism              string   `json:"mechanism"`
	Status                 string   `json:"status"`
}

func (r LaunchReceipt) Validate() error {
	if !validSHA256(r.AgentConfigFingerprint) {
		return errors.New("agent configuration fingerprint required in launch receipt")
	}
	if strings.TrimSpace(r.RunID) == "" || strings.TrimSpace(r.Mechanism) == "" || r.Status != LaunchConfigured {
		return errors.New("invalid launch configuration receipt")
	}
	if err := ValidateSelection(r.AgentID, PolicyCooperativeTrustedV1); err != nil {
		return err
	}
	digest, err := hex.DecodeString(r.PolicySHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != r.PolicySHA256 {
		return errors.New("invalid policy digest in launch receipt")
	}
	return nil
}

func validSHA256(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == value
}
