package execution

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/lazyun/devbridge/internal/structuredschema"
)

// ImplementationReport is a machine result envelope; it does not prescribe
// engineering behavior or change the immutable Task definition.
type ImplementationReport struct {
	ResultDisposition string `json:"resultDisposition"`
	Summary           string `json:"summary"`
}

var ImplementationReportJSONSchema = string(structuredschema.Marshal(structuredschema.Object(map[string]any{
	"resultDisposition": structuredschema.Enum("COMPLETED", "SCOPE_CONFLICT"),
	"summary":           structuredschema.String(),
})))

func parseImplementationReport(raw string) (ImplementationReport, error) {
	var result ImplementationReport
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if decoder.Decode(new(any)) != io.EOF || (result.ResultDisposition != "COMPLETED" && result.ResultDisposition != "SCOPE_CONFLICT") || strings.TrimSpace(result.Summary) == "" {
		return result, errors.New("invalid structured implementation result")
	}
	return result, nil
}
