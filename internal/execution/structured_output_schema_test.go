package execution

import (
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/structuredschema"
)

func TestEveryExecutionStructuredOutputSchemaIsStrict(t *testing.T) {
	for name, schema := range map[string]string{
		"implementation": ImplementationReportJSONSchema,
		"verification":   VerificationReportV2JSONSchema,
		"coding review":  review.ResultJSONSchema,
	} {
		t.Run(name, func(t *testing.T) {
			if err := structuredschema.Validate([]byte(schema)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
