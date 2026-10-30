package mcpserver

import (
	"strings"
	"testing"
)

func TestImplementationScopeConflictInstructionNamesCanonicalRoutes(t *testing.T) {
	for _, text := range []string{"SCOPE_CONFLICT is a known stop", "Preserve all changed files", "RECOVERED_PARTIAL", "same approved revision", "REVISION_REQUIRED", "RECOVERY_PURPOSE", "without an automatic Review", "REQUEST_TASK_REVIEW", "REQUEST_SUPPLEMENTAL_REVIEW", "CONFLICT/INSUFFICIENT", "Review-result SCOPE_CONFLICT"} {
		if !strings.Contains(ChatGPTConfirmationInstructions, text) {
			t.Errorf("missing scope conflict instruction %q", text)
		}
	}
}
