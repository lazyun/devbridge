package inspectionresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/structuredschema"
	"github.com/lazyun/devbridge/internal/workset"
)

type InspectionSource struct {
	ProjectID             string `json:"projectId"`
	InspectionOwnerTaskID string `json:"inspectionOwnerTaskId"`
	InspectionOperationID string `json:"inspectionOperationId"`
	RunID                 string `json:"runId"`
	RuntimeWorksetHash    string `json:"runtimeWorksetHash"`
}
type InspectionFinding struct {
	RepositoryAlias string `json:"repositoryAlias"`
	Path            string `json:"path"`
	Summary         string `json:"summary"`
}
type InspectionRepositoryObservation struct {
	RepositoryAlias string   `json:"repositoryAlias"`
	Summary         string   `json:"summary"`
	RelevantPaths   []string `json:"relevantPaths"`
}
type InspectionRelationship struct {
	FromRepositoryAlias string `json:"fromRepositoryAlias"`
	ToRepositoryAlias   string `json:"toRepositoryAlias"`
	Summary             string `json:"summary"`
}
type InspectionReport struct {
	SchemaVersion          string                            `json:"schemaVersion"`
	Source                 InspectionSource                  `json:"source"`
	Status                 string                            `json:"status"`
	Summary                string                            `json:"summary"`
	Findings               []InspectionFinding               `json:"findings"`
	RepositoryObservations []InspectionRepositoryObservation `json:"repositoryObservations"`
	BlockedReasons         []string                          `json:"blockedReasons"`
	Relationships          []InspectionRelationship          `json:"relationships"`
	RecommendedNextAction  string                            `json:"recommendedNextAction"`
}

func SourceForWorkset(w workset.RuntimeWorkset) InspectionSource {
	return InspectionSource{w.ProjectID, w.InspectionOwnerTaskID, w.InspectionOperationID, w.RunID, w.ContentHash}
}
func Hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func Schema(source InspectionSource) json.RawMessage {
	b, _ := json.Marshal(source)
	var fields map[string]string
	_ = json.Unmarshal(b, &fields)
	identity := map[string]any{}
	for k, v := range fields {
		identity[k] = structuredschema.Schema{"type": "string", "const": v}
	}
	str := structuredschema.String
	arr := structuredschema.Array
	obj := structuredschema.Object
	return structuredschema.Marshal(obj(map[string]any{"schemaVersion": structuredschema.Schema{"type": "string", "const": "project-inspection-v1"}, "source": obj(identity), "status": structuredschema.Enum("SUCCEEDED", "BLOCKED"), "summary": str(), "findings": arr(obj(map[string]any{"repositoryAlias": str(), "path": str(), "summary": str()})), "repositoryObservations": arr(obj(map[string]any{"repositoryAlias": str(), "summary": str(), "relevantPaths": arr(str())})), "blockedReasons": arr(str()), "relationships": arr(obj(map[string]any{"fromRepositoryAlias": str(), "toRepositoryAlias": str(), "summary": str()})), "recommendedNextAction": structuredschema.Enum("NONE", "REQUEST_USER_DECISION", "CREATE_FEATURE_AREA", "CREATE_TASK", "INSPECT_MORE")}))
}
func Parse(raw []byte, w workset.RuntimeWorkset) (InspectionReport, error) {
	var r InspectionReport
	if len(raw) == 0 || len(raw) > 1<<20 {
		return r, errors.New("invalid Inspection report size")
	}
	if err := unique(raw); err != nil {
		return r, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, err
	}
	var x any
	if d.Decode(&x) != io.EOF {
		return r, errors.New("trailing Inspection report data")
	}
	if r.SchemaVersion != "project-inspection-v1" || r.Source != SourceForWorkset(w) || (r.Status != "SUCCEEDED" && r.Status != "BLOCKED") || strings.TrimSpace(r.Summary) == "" || r.Findings == nil || r.RepositoryObservations == nil || r.BlockedReasons == nil || r.Relationships == nil {
		return r, errors.New("Inspection report identity or required fields invalid")
	}
	if r.Status == "BLOCKED" && len(r.BlockedReasons) == 0 {
		return r, errors.New("blocked Inspection needs reasons")
	}
	switch r.RecommendedNextAction {
	case "NONE", "REQUEST_USER_DECISION", "CREATE_FEATURE_AREA", "CREATE_TASK", "INSPECT_MORE":
	default:
		return r, errors.New("invalid Inspection recommendation")
	}
	aliases := map[string]bool{}
	scopes := map[string]workset.ReadScope{}
	for _, m := range w.Members {
		aliases[m.RepositoryAlias] = true
		scopes[m.RepositoryAlias] = m.IntendedReadScope
	}
	seen := map[string]bool{}
	for _, o := range r.RepositoryObservations {
		if !aliases[o.RepositoryAlias] || seen[o.RepositoryAlias] || o.RelevantPaths == nil {
			return r, errors.New("Inspection repository observation does not match workset")
		}
		seen[o.RepositoryAlias] = true
		for _, p := range o.RelevantPaths {
			if !relative(p) || !inScope(p, scopes[o.RepositoryAlias]) {
				return r, errors.New("invalid repository-qualified path")
			}
		}
	}
	if len(seen) != len(aliases) {
		return r, errors.New("incomplete Inspection repository observations")
	}
	for _, f := range r.Findings {
		if !aliases[f.RepositoryAlias] || !relative(f.Path) || !inScope(f.Path, scopes[f.RepositoryAlias]) || strings.TrimSpace(f.Summary) == "" {
			return r, errors.New("invalid Inspection finding")
		}
	}
	for _, v := range r.Relationships {
		if !aliases[v.FromRepositoryAlias] || !aliases[v.ToRepositoryAlias] || v.FromRepositoryAlias == v.ToRepositoryAlias || strings.TrimSpace(v.Summary) == "" {
			return r, errors.New("invalid Inspection relationship")
		}
	}
	return r, nil
}
func inScope(p string, s workset.ReadScope) bool {
	if s.Kind == "REPOSITORY" {
		return true
	}
	if s.Kind != "PATHS" {
		return false
	}
	for _, a := range s.Paths {
		if p == a || strings.HasPrefix(p, a+"/") {
			return true
		}
	}
	return false
}
func relative(v string) bool {
	return v != "" && !strings.ContainsRune(v, 0) && !strings.HasPrefix(v, "/") && v != ".." && !strings.HasPrefix(v, "../") && path.Clean(v) == v && !strings.Contains(v, "\\")
}
func unique(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		if z, ok := t.(json.Delim); ok {
			if z == '{' {
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					s, ok := k.(string)
					if !ok || seen[s] {
						return errors.New("duplicate Inspection JSON key")
					}
					seen[s] = true
					if e = visit(); e != nil {
						return e
					}
				}
			} else if z == '[' {
				for d.More() {
					if e := visit(); e != nil {
						return e
					}
				}
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := visit(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing Inspection JSON")
	}
	return nil
}

var _ = reflect.DeepEqual
