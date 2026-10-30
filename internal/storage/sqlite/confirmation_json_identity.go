package sqlite

import "strings"

// SQLite resolves the first duplicate JSON key, while encoding/json resolves
// the last and accepts case-insensitive struct field names. Reject ambiguous
// persisted journals before either interpretation can become authority. The
// protected root, commit and step objects use the canonical internal schema;
// nested business maps retain their ordinary case-sensitive keys.
func confirmationAmbiguousJSON(expression string) string {
	return strings.ReplaceAll(`(
 EXISTS(SELECT 1 FROM json_tree(__JSON__) j GROUP BY j.parent,j.key HAVING COUNT(*)>1)
 OR EXISTS(SELECT 1 FROM json_each(__JSON__) j WHERE j.key NOT IN (
  'submission','currentUpperReadAvailable','currentDirectFile','currentUpperReview','currentAccessReadError','canResume',
  'intentId','version','aiSessionId','projectId','purpose','action','state','contentHash','content','commit','steps','createdAt','expiresAt'))
 OR (json_type(__JSON__,'$.commit') IS 'object' AND EXISTS(SELECT 1 FROM json_each(__JSON__,'$.commit') j WHERE j.key NOT IN (
  'aiSessionId','intentId','version','purpose','action','requestId','contentHash','variantId','selection','backendSelection')))
 OR EXISTS(SELECT 1 FROM json_each(__JSON__,'$.steps') s,
  json_each(CASE WHEN s.type='object' THEN s.value ELSE '{}' END) j WHERE j.key NOT IN (
   'repositoryId','developmentRef','failure','effect','kind','requestId','startRequestId','state','projectId','featureAreaId',
   'taskId','reviewId','executionId','runId','revisionId','workflowId','code','message'))
)`, "__JSON__", expression)
}
