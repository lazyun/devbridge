package recoverystatus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/inspectionresult"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/structuredschema"
)

const ReportVersion = "v1"
const InspectionReportVersion = "v2"
const MaxReportBytes = 1 << 20

type GoalObservation struct {
	GoalID          string `json:"goalId"`
	Status          string `json:"status"`
	EvidenceSummary string `json:"evidenceSummary"`
}
type RepositoryObservation struct {
	RuntimeAlias      string   `json:"runtimeAlias"`
	ChangedPaths      []string `json:"changedPaths"`
	Completion        string   `json:"completion"`
	VerificationState string   `json:"verificationState"`
}
type VerificationObservation struct {
	CheckID         string `json:"checkId"`
	Status          string `json:"status"`
	EvidenceSummary string `json:"evidenceSummary"`
}
type Report struct {
	Version                  string                    `json:"version"`
	SourceOperationKind      string                    `json:"sourceOperationKind"`
	SourceOperationID        string                    `json:"sourceOperationId"`
	SourceRunID              string                    `json:"sourceRunId"`
	SourceTurnID             string                    `json:"sourceTurnId"`
	OverallStatus            string                    `json:"overallStatus"`
	Summary                  string                    `json:"summary"`
	CompletedGoals           []GoalObservation         `json:"completedGoals"`
	RemainingGoals           []GoalObservation         `json:"remainingGoals"`
	RepositoryObservations   []RepositoryObservation   `json:"repositoryObservations"`
	VerificationObservations []VerificationObservation `json:"verificationObservations"`
	UnresolvedIssues         []string                  `json:"unresolvedIssues"`
	ScopeExpansionRequired   bool                      `json:"scopeExpansionRequired"`
	RecommendedNextAction    string                    `json:"recommendedNextAction"`
	ReviewReport             *review.ReviewReport      `json:"reviewReport"`
	VerificationReport       json.RawMessage           `json:"verificationReport"`
	InspectionResult         json.RawMessage           `json:"inspectionResult,omitempty"`
}

// MarshalJSON preserves the strict structured-output shape for an embedded
// ReviewReport without changing ReviewReport's standalone historical wire
// encoding. Its optional proposal fields are explicit nulls in recovery output.
func (r Report) MarshalJSON() ([]byte, error) {
	type reportAlias Report
	raw, err := json.Marshal(reportAlias(r))
	if err != nil || r.ReviewReport == nil {
		return raw, err
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	reportObject, ok := object["reviewReport"].(map[string]any)
	if !ok {
		return nil, errors.New("recovery review report must be an object")
	}
	fillReviewOptionalFields(reportObject, reflect.TypeOf(review.ReviewReport{}))
	return json.Marshal(object)
}

// NormalizeGenerated makes every omitempty property required and nullable,
// including nested targets, scopes, checks and evidence references. Restore
// those keys only when encoding a typed report; ParseReport still validates
// caller bytes before decoding and never repairs malformed external input.
func fillReviewOptionalFields(value any, typ reflect.Type) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			if !field.IsExported() || tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			if child, present := object[name]; present {
				fillReviewOptionalFields(child, field.Type)
			} else {
				for _, option := range tag[1:] {
					if option == "omitempty" {
						object[name] = nil
					}
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if items, ok := value.([]any); ok {
			for _, item := range items {
				fillReviewOptionalFields(item, typ.Elem())
			}
		}
	}
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func textValid(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 16384 && !strings.ContainsRune(s, 0)
}
func memberOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func safePath(s string) bool {
	return s != "" && s != "." && !strings.ContainsRune(s, 0) && !strings.Contains(s, "\\") && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

// ParseReport accepts only the versioned structured result, never prose. The
// supplied source is durable Core authority, not inferred from model output.
func ParseReport(raw []byte, source Source) (Report, error) {
	var r Report
	bad := func(s string) (Report, error) { return Report{}, fmt.Errorf("RECOVERY_REPORT_INVALID: %s", s) }
	if len(raw) == 0 || len(raw) > MaxReportBytes {
		return bad("size")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return bad(err.Error())
	}
	var schema jsonschema.Schema
	reportSchema := ReportSchemaForSource(source)
	if err := json.Unmarshal(reportSchema, &schema); err != nil {
		return bad(err.Error())
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return bad(err.Error())
	}
	var instance any
	if err = json.Unmarshal(raw, &instance); err != nil {
		return bad(err.Error())
	}
	if err = resolved.Validate(instance); err != nil {
		return bad(err.Error())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return bad("object required")
	}
	for _, key := range []string{"version", "sourceOperationKind", "sourceOperationId", "sourceRunId", "sourceTurnId", "overallStatus", "summary", "completedGoals", "remainingGoals", "repositoryObservations", "verificationObservations", "unresolvedIssues", "scopeExpansionRequired", "recommendedNextAction"} {
		if len(fields[key]) == 0 || bytes.Equal(fields[key], []byte("null")) {
			return bad("missing " + key)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return bad(err.Error())
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return bad("trailing value")
	}
	wantVersion := ReportVersion
	if source.OwnerKind == "PROJECT_INSPECTION" {
		wantVersion = InspectionReportVersion
	}
	if r.Version != wantVersion || r.SourceOperationKind != source.OperationKind || r.SourceOperationID != source.OperationID || r.SourceRunID != source.RunID || r.SourceTurnID != source.TurnID {
		return bad("source/version mismatch")
	}
	if source.OwnerKind == "PROJECT_INSPECTION" && len(r.InspectionResult) == 0 {
		return bad("inspection result")
	}
	if source.OwnerKind == "PROJECT_INSPECTION" {
		var schema jsonschema.Schema
		identity := inspectionresult.InspectionSource{ProjectID: source.ProjectID, InspectionOwnerTaskID: source.TaskID, InspectionOperationID: source.OperationID, RunID: source.RunID, RuntimeWorksetHash: source.RuntimeWorksetHash}
		if err := json.Unmarshal(inspectionresult.Schema(identity), &schema); err != nil {
			return bad("inspection schema")
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return bad("inspection schema")
		}
		var value any
		if err = json.Unmarshal(r.InspectionResult, &value); err != nil || resolved.Validate(value) != nil {
			return bad("inspection result")
		}
	}
	if !textValid(r.Summary) || !memberOf(r.OverallStatus, "COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN") || !memberOf(r.RecommendedNextAction, "FINALIZE", "CONTINUE", "REQUEST_USER", "REVISE", "CANCEL", "NONE") {
		return bad("status/summary")
	}
	seen := map[string]bool{}
	for _, list := range [][]GoalObservation{r.CompletedGoals, r.RemainingGoals} {
		for _, g := range list {
			if !textValid(g.GoalID) || !textValid(g.EvidenceSummary) || !memberOf(g.Status, "COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN", "NOT_STARTED") || seen[g.GoalID] {
				return bad("goal")
			}
			seen[g.GoalID] = true
		}
	}
	seen = map[string]bool{}
	for _, o := range r.RepositoryObservations {
		if !textValid(o.RuntimeAlias) || seen[o.RuntimeAlias] || o.ChangedPaths == nil || !memberOf(o.Completion, "COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN", "NOT_STARTED") || !memberOf(o.VerificationState, "PASSED", "FAILED", "NOT_RUN", "UNKNOWN") {
			return bad("repository")
		}
		seen[o.RuntimeAlias] = true
		for _, p := range o.ChangedPaths {
			if !safePath(p) {
				return bad("path")
			}
		}
	}
	seen = map[string]bool{}
	for _, v := range r.VerificationObservations {
		if !textValid(v.CheckID) || seen[v.CheckID] || !textValid(v.EvidenceSummary) || !memberOf(v.Status, "PASSED", "FAILED", "NOT_RUN", "UNKNOWN") {
			return bad("verification")
		}
		seen[v.CheckID] = true
	}
	for _, s := range r.UnresolvedIssues {
		if !textValid(s) {
			return bad("issue")
		}
	}
	if r.ReviewReport != nil && (r.ReviewReport.SchemaVersion != 2 || r.ReviewReport.Validate() != nil) {
		return bad("full v2 review required")
	}
	return r, nil
}

// InspectionReportSchema is a v2 extension. ReportSchema(), and therefore all
// established v1 bytes and hashes, remain unchanged.
func InspectionReportSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(ReportSchema(), &schema)
	p := schema["properties"].(map[string]any)
	p["version"] = map[string]any{"type": "string", "const": InspectionReportVersion}
	p["inspectionResult"] = inspectionresult.Schema(inspectionresult.InspectionSource{})
	req := schema["required"].([]any)
	schema["required"] = append(req, "inspectionResult")
	return structuredschema.Marshal(schema)
}

// ReportSchema is passed as a machine field, separately from exact instruction.
func ReportSchema() json.RawMessage {
	str := func(values ...string) structuredschema.Schema {
		m := structuredschema.String()
		if len(values) > 0 {
			m["enum"] = values
		}
		return m
	}
	obj := structuredschema.Object
	arr := structuredschema.Array
	goal := obj(map[string]any{"goalId": str(), "status": str("COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN", "NOT_STARTED"), "evidenceSummary": str()})
	props := map[string]any{"version": str(ReportVersion), "sourceOperationKind": str(), "sourceOperationId": str(), "sourceRunId": str(), "sourceTurnId": str(), "overallStatus": str("COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN"), "summary": str(), "completedGoals": arr(goal), "remainingGoals": arr(goal), "repositoryObservations": arr(obj(map[string]any{"runtimeAlias": str(), "changedPaths": arr(str()), "completion": str("COMPLETED", "PARTIAL", "BLOCKED", "UNKNOWN", "NOT_STARTED"), "verificationState": str("PASSED", "FAILED", "NOT_RUN", "UNKNOWN")})), "verificationObservations": arr(obj(map[string]any{"checkId": str(), "status": str("PASSED", "FAILED", "NOT_RUN", "UNKNOWN"), "evidenceSummary": str()})), "unresolvedIssues": arr(str()), "scopeExpansionRequired": map[string]any{"type": "boolean"}, "recommendedNextAction": str("FINALIZE", "CONTINUE", "REQUEST_USER", "REVISE", "CANCEL", "NONE")}
	inferred, err := jsonschema.For[review.ReviewReport](nil)
	if err != nil {
		panic(err)
	}
	reviewBytes, _ := json.Marshal(inferred)
	var reviewSchema map[string]any
	_ = json.Unmarshal(reviewBytes, &reviewSchema)
	defs := reviewSchema["$defs"]
	delete(reviewSchema, "$defs")
	props["reviewReport"] = structuredschema.Nullable(structuredschema.NormalizeGenerated(reviewSchema))
	vf := obj(map[string]any{"index": map[string]any{"type": "integer", "minimum": 0}, "status": str("PASSED", "FAILED", "BLOCKED", "NOT_RUN", "UNKNOWN"), "summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}, "evidence": map[string]any{"type": "string", "maxLength": 32768}, "reason": map[string]any{"type": "string", "maxLength": 2048}})
	props["verificationReport"] = structuredschema.Nullable(obj(map[string]any{"version": map[string]any{"type": "integer", "const": 2}, "summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, "checks": arr(vf), "acceptanceCriteria": arr(vf)}))
	root := obj(props)
	if defs != nil {
		root["$defs"] = structuredschema.NormalizeGenerated(map[string]any{"type": "object", "properties": defs})["properties"]
	}
	return structuredschema.Marshal(root)
}
func ReportSchemaHash() string { return digest(ReportSchema()) }

// JSON duplicate keys are rejected rather than allowing later values to replace
// source identities, schema fields or evidence supplied earlier in the bytes.
func rejectDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delimiter, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("duplicate JSON key")
				}
				seen[s] = true
				if e = visit(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := visit(); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := visit(); e != nil {
		return e
	}
	if _, e := d.Token(); !errors.Is(e, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

// ReportSchemaForSource communicates immutable source identity through schema
// constants. No source identifiers or natural language are appended to the
// caller's instruction, and the model does not choose database authority.
func ReportSchemaForSource(source Source) json.RawMessage {
	var schema map[string]any
	base := ReportSchema()
	if source.OwnerKind == "PROJECT_INSPECTION" {
		base = InspectionReportSchema()
	}
	if err := json.Unmarshal(base, &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	if source.OwnerKind == "PROJECT_INSPECTION" {
		properties["inspectionResult"] = inspectionresult.Schema(inspectionresult.InspectionSource{
			ProjectID: source.ProjectID, InspectionOwnerTaskID: source.TaskID,
			InspectionOperationID: source.OperationID, RunID: source.RunID,
			RuntimeWorksetHash: source.RuntimeWorksetHash,
		})
	}
	for key, value := range map[string]string{"sourceOperationKind": source.OperationKind, "sourceOperationId": source.OperationID, "sourceRunId": source.RunID, "sourceTurnId": source.TurnID} {
		properties[key] = map[string]any{"type": "string", "const": value}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	if err = structuredschema.Validate(raw); err != nil {
		panic(err)
	}
	return raw
}
