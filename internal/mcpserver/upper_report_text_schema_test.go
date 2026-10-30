package mcpserver

import (
	"regexp"
	"testing"
)

func TestUpperReportNonblankTextSupportsFullMatchConnectors(t *testing.T) {
	properties := ReviewReportSchema()["properties"].(map[string]any)
	for _, key := range []string{"summary", "coverage", "limitations", "recommendedNextSteps"} {
		schema := properties[key].(map[string]any)
		if key != "summary" {
			schema = schema["items"].(map[string]any)
		}
		pattern := regexp.MustCompile(schema["pattern"].(string))
		for _, value := range []string{"docs/ARCHITECTURE.md", "中文审查说明", "第一行\n\t第二行", " a ", "x"} {
			match := pattern.FindStringIndex(value)
			if match == nil || match[0] != 0 || match[1] != len(value) {
				t.Errorf("%s rejects full text %q", key, value)
			}
		}
		for _, value := range []string{"", " ", "\n\t\r"} {
			if pattern.MatchString(value) {
				t.Errorf("%s accepts blank %q", key, value)
			}
		}
	}
}
