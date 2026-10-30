package sqlite

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/review"
)

// Older re-review producers encoded an unloaded ForbiddenPaths slice as null.
// Resolve only a proven same-scope successor to its immutable predecessor grant;
// null without that lineage stays invalid. This is a read interpretation, never
// a rewrite or an empty-list default that could drop a prior deny boundary.
func reviewForbiddenPathsJSON(ctx context.Context, q definitionQuerier, reviewID, raw string) (string, error) {
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		var paths []string
		if json.Unmarshal([]byte(raw), &paths) != nil {
			return "", review.ErrInvalidArgument
		}
		if paths != nil {
			return raw, nil
		}
		if seen[reviewID] {
			return "", review.ErrInvalidArgument
		}
		seen[reviewID] = true
		var parent, next string
		err := q.QueryRowContext(ctx, `SELECT p.id,pg.forbidden_paths_json FROM (
    SELECT successor_review_id,previous_review_id,task_id FROM review_re_review_requests
    UNION ALL SELECT successor_review_id,previous_review_id,task_id FROM review_invalid_result_resolutions
  ) link JOIN reviews v ON v.id=link.successor_review_id AND v.task_id=link.task_id
  JOIN reviews p ON p.id=link.previous_review_id AND p.task_id=v.task_id AND p.project_id=v.project_id AND p.feature_area_id=v.feature_area_id
  JOIN review_grants g ON g.review_id=v.id JOIN review_grants pg ON pg.review_id=p.id
  WHERE v.id=? AND v.executor_kind=p.executor_kind AND v.repository_id IS p.repository_id AND v.base_commit IS p.base_commit
   AND v.focus_kind=p.focus_kind AND v.focus_paths_json=p.focus_paths_json AND v.repository_targets_json=p.repository_targets_json
   AND g.protocol_version=pg.protocol_version AND g.initial_scope_kind=pg.initial_scope_kind AND g.initial_scope_paths_json=pg.initial_scope_paths_json
   AND json_type(g.forbidden_paths_json)='null'`, reviewID).Scan(&parent, &next)
		if err != nil {
			return "", review.ErrInvalidArgument
		}
		reviewID, raw = parent, next
	}
	return "", review.ErrInvalidArgument
}
