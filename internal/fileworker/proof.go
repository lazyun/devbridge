package fileworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"unicode"
)

func validID(s string) bool {
	return s != "" && len(s) <= 128 && strings.IndexFunc(s, unicode.IsSpace) < 0 && !strings.ContainsRune(s, 0)
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func digestJSON(v any) string  { raw, _ := json.Marshal(v); return digest(raw) }
func validDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && s == strings.ToLower(s)
}
func copyValue[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

func validateAccess(a directfile.State, t directfile.WorkerTarget, d task.Definition) error {
	actual := directfile.WorkerTarget{AISessionID: a.AISessionID, FeatureAreaID: a.FeatureAreaID, TaskID: a.TaskID, TaskDefinitionRevisionID: a.TaskDefinitionRevisionID, DecisionID: a.DecisionID, WorkflowID: a.WorkflowID, ProtocolVersion: a.ProtocolVersion, RuntimeEpoch: a.RuntimeEpoch, GrantGeneration: a.GrantGeneration}
	if actual != t || !a.Enabled || a.State != "ACTIVE" || a.FailureCode != "" || a.FailureReason != "" || !validID(a.WorkspaceID) || a.ProjectID != d.ProjectID || a.FeatureAreaID != d.FeatureAreaID || digestJSON(a.ExecutionScope) != digestJSON(d.WorkSpec.ExecutionScope) || len(a.RepositoryTargets) != len(d.RepositoryTargets) {
		return ErrResponse
	}
	targets := make([]workset.Target, 0, len(a.RepositoryTargets))
	expected := map[string]workset.Target{}
	for _, m := range d.RepositoryTargets {
		expected[m.MemberID] = m
	}
	for _, m := range a.RepositoryTargets {
		targets = append(targets, m.Target)
		e, ok := expected[m.MemberID]
		if !ok || digestJSON(e) != digestJSON(m.Target) || m.State != "READY" || m.Failure != "" || m.WorkspaceID != a.WorkspaceID || !validID(m.WorktreeID) || !workset.ValidCommit(m.StartHead) || !workset.ValidCommit(m.StartSnapshot) {
			return ErrResponse
		}
	}
	if workset.ValidateTargets(targets) != nil {
		return ErrResponse
	}
	allowed := map[string]bool{}
	for _, op := range a.AllowedOperations {
		allowed[op] = true
	}
	for _, op := range []string{"READ", "LIST", "CREATE", "REPLACE", "DELETE", "MOVE"} {
		if !allowed[op] {
			return ErrResponse
		}
	}
	return nil
}

// The formal result has no public payload hash. Validate actual exported facts,
// not an invented UPPER report digest. Current verification/acceptance projection
// is intentionally mutable; the frozen file result and Execution owner are not.
func (c *Client) validateAndPinResult(r directfile.ImplementationResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.request.Claim.WorkerTarget
	if !validID(r.ID) || r.AISessionID != t.AISessionID || r.TaskID != t.TaskID || r.TaskDefinitionRevisionID != t.TaskDefinitionRevisionID || r.WorkflowID != t.WorkflowID || r.RequestID != c.request.ResultRequestID || r.ProtocolVersion != 2 || r.RuntimeEpoch != t.RuntimeEpoch || r.GrantGeneration != t.GrantGeneration || strings.TrimSpace(r.Summary) == "" || r.Snapshot != "" {
		return ErrResponse
	}
	if c.submitted != nil && (r.Summary != c.submitted.Summary || r.VerificationNotes != c.submitted.VerificationNotes) {
		return ErrResponse
	}
	if workset.ValidateTargets(r.RepositoryTargets) != nil || len(r.Members) != len(r.RepositoryTargets) {
		return ErrResponse
	}
	targets := map[string]workset.Target{}
	for _, m := range r.RepositoryTargets {
		targets[m.MemberID] = m
	}
	original := map[string]directfile.MemberGrant{}
	if c.access != nil {
		if len(c.access.RepositoryTargets) != len(r.Members) {
			return ErrResponse
		}
		for _, m := range c.access.RepositoryTargets {
			original[m.MemberID] = m
		}
	}
	seen := map[string]bool{}
	for _, m := range r.Members {
		target, ok := targets[m.MemberID]
		if !ok || seen[m.MemberID] || digestJSON(target) != digestJSON(m.Target) || !validID(m.WorkspaceID) || !validID(m.WorktreeID) || !workset.ValidCommit(m.StartHead) || !workset.ValidCommit(m.StartSnapshot) || !workset.ValidCommit(m.EndSnapshot) || (m.State != "COMPLETED" && m.State != "NO_CHANGE") {
			return ErrResponse
		}
		seen[m.MemberID] = true
		if c.access != nil {
			o, ok := original[m.MemberID]
			if !ok || digestJSON(o.Target) != digestJSON(m.Target) || o.WorkspaceID != m.WorkspaceID || o.WorktreeID != m.WorktreeID || o.StartHead != m.StartHead || o.StartSnapshot != m.StartSnapshot {
				return ErrResponse
			}
		}
		for _, name := range m.ChangedPaths {
			if !cleanPath(name, false) || !writeAllowed(m.Target, name) {
				return ErrResponse
			}
		}
		if (m.State == "NO_CHANGE") != (len(m.ChangedPaths) == 0) {
			return ErrResponse
		}
	}
	if c.request.VerificationMode == task.VerificationModeNone {
		if r.VerificationStatus != "NOT_RUN" || r.ExecutionID != "" || r.VerificationRunID != "" || r.VerificationStartError != "" {
			return ErrResponse
		}
	} else {
		if !validID(r.ExecutionID) {
			return ErrResponse
		}
		switch r.VerificationStatus {
		case "READY", "RUNNING", "SUCCEEDED", "FAILED", "CANCELED":
		default:
			return ErrResponse
		}
		if r.VerificationRunID != "" && !validID(r.VerificationRunID) {
			return ErrResponse
		}
		if (r.VerificationStatus == "RUNNING" || r.VerificationStatus == "SUCCEEDED") && r.VerificationRunID == "" {
			return ErrResponse
		}
	}
	if c.result != nil && digestJSON(immutableResult(*c.result)) != digestJSON(immutableResult(r)) {
		return ErrResponse
	}
	value := copyValue(r)
	c.result = &value
	return nil
}
func immutableResult(r directfile.ImplementationResult) directfile.ImplementationResult {
	r.Accepted = false
	r.Replay = false
	r.CommitError = ""
	r.VerificationStartError = ""
	r.VerificationStatus = ""
	r.VerificationRunID = ""
	return r
}
func decode(result *sdkmcp.CallToolResult, out any) error {
	if result == nil || result.IsError || result.StructuredContent == nil {
		return ErrResponse
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	if len(raw) > 2<<20 {
		return errors.New("file worker response exceeds bound")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ErrResponse
	}
	if nested, ok := fields["result"]; ok {
		raw = nested
		fields = nil
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return ErrResponse
		}
	}
	// Do not let missing proof/boolean fields silently become Go zero values.
	require := func(v map[string]json.RawMessage, keys ...string) error {
		for _, key := range keys {
			raw, ok := v[key]
			if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrResponse
			}
		}
		return nil
	}
	resultKeys := []string{"id", "aiSessionId", "taskId", "taskDefinitionRevisionId", "workflowId", "requestId", "summary", "verificationNotes", "accepted", "replay", "verificationStatus", "snapshot", "protocolVersion", "runtimeEpoch", "grantGeneration", "members", "repositoryTargets"}
	switch out.(type) {
	case *directfile.WorkerClaimResult:
		if err = require(fields, "receipt", "state", "replay"); err != nil {
			return err
		}
		if raw, ok := fields["resultReceipt"]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) != nil || nested == nil {
				return ErrResponse
			}
			if err = require(nested, resultKeys...); err != nil {
				return err
			}
		}
	case *directfile.ImplementationResult:
		if err = require(fields, resultKeys...); err != nil {
			return err
		}
	case *directfile.Result:
		if err = require(fields, "path", "content", "size", "workflowComplete"); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrResponse, err)
	}
	return nil
}
