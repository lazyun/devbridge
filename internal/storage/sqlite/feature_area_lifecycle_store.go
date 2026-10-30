package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/featurearea"
)

// ApplyLifecycle serializes only the durable mutation, never the Agent runtime.
// beginWrite takes the SQLite writer boundary before any lifecycle reads.
func (s *FeatureAreaStore) ApplyLifecycle(ctx context.Context, input featurearea.LifecycleMutation) (featurearea.LifecycleResult, error) {
	if input.Action == featurearea.ActionRetire {
		return s.beginRetirement(ctx, input)
	}
	if result, found, err := s.readLifecycleReplay(ctx, input); found || err != nil {
		return result, err
	}
	empty := featurearea.LifecycleResult{}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	parent, err := scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=? AND project_id=?`, input.FeatureAreaID, input.ProjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, featurearea.ErrNotFound
	}
	if err != nil {
		return empty, err
	}
	var action, hash, encoded string
	err = tx.QueryRowContext(ctx, `SELECT action,payload_sha256,result_json FROM feature_area_lifecycle_requests WHERE feature_area_id=? AND request_id=?`, parent.ID, input.RequestID).Scan(&action, &hash, &encoded)
	if err == nil {
		if action != string(input.Action) || hash != input.PayloadSHA256 {
			return empty, featurearea.ErrConflict
		}
		var result featurearea.LifecycleResult
		err = json.Unmarshal([]byte(encoded), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=?)`, parent.ID).Scan(&busy); err != nil {
		return empty, err
	}
	if busy {
		return empty, agentconversation.ErrBusy
	}
	target, err := featurearea.LifecycleTarget(parent, input.Action)
	if err != nil {
		return empty, err
	}
	if input.Action == featurearea.ActionDecompose {
		if len(input.Children) == 0 || len(input.Children) > 32 {
			return empty, featurearea.ErrInvalidArgument
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE feature_area_id=? AND legacy_read_only=0 AND task_type='DEVELOPMENT'`, parent.ID).Scan(&count); err != nil {
			return empty, err
		}
		if count != 0 {
			return empty, featurearea.ErrConflict
		}
	} else if len(input.Children) != 0 {
		return empty, featurearea.ErrInvalidArgument
	}
	result := featurearea.LifecycleResult{Parent: parent, Children: []featurearea.FeatureArea{}, Relations: []featurearea.Relation{}}
	for _, child := range input.Children {
		if child.ProjectID != parent.ProjectID || child.Status != featurearea.StatusDiscovering || child.LegacyReadOnly {
			return empty, featurearea.ErrInvalidArgument
		}
		if parent.MergedCommit != "" {
			child.MergedCommit, err = s.CurrentDevelopmentHead(ctx, parent.ProjectID)
			if err != nil {
				return empty, err
			}
			child.MergedAt = &child.CreatedAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at,merged_commit,merged_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, child.ID, child.ProjectID, child.CreatedByAISessionID, child.Title, child.ProblemStatement, child.Status, formatTime(child.CreatedAt), formatTime(child.UpdatedAt), nullString(child.MergedCommit), nullableCommitTime(child.MergedAt)); err != nil {
			return empty, conflict(err)
		}
		relation := featurearea.Relation{SourceFeatureAreaID: parent.ID, TargetFeatureAreaID: child.ID, Kind: featurearea.RelationSplitInto, CreatedAt: input.At}
		if _, err := tx.ExecContext(ctx, `INSERT INTO feature_area_relations(source_feature_area_id,target_feature_area_id,kind,project_id,created_at) VALUES(?,?,?,?,?)`, parent.ID, child.ID, relation.Kind, parent.ProjectID, formatTime(input.At)); err != nil {
			return empty, conflict(err)
		}
		result.Children = append(result.Children, child)
		result.Relations = append(result.Relations, relation)
		payload, _ := event.Payload(map[string]any{"status": child.Status, "title": child.Title})
		if err := appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaCreated, ProjectID: child.ProjectID, EntityType: "feature_area", EntityID: child.ID, Payload: payload, CreatedAt: input.At}); err != nil {
			return empty, err
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE feature_areas SET status=?,updated_at=? WHERE id=? AND project_id=? AND status=? AND legacy_read_only=0`, target, formatTime(input.At), parent.ID, parent.ProjectID, parent.Status)
	if err != nil {
		return empty, conflict(err)
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return empty, featurearea.ErrConflict
	}
	invalidateDirectFile(s.db, parent.ID)
	// The lifecycle trigger ends the canonical conversation in this transaction.
	result.Parent.Status, result.Parent.UpdatedAt = target, input.At
	payload, _ := event.Payload(map[string]any{"from": parent.Status, "to": target, "requestId": input.RequestID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaStatusChanged, ProjectID: parent.ProjectID, EntityType: "feature_area", EntityID: parent.ID, Payload: payload, CreatedAt: input.At}); err != nil {
		return empty, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feature_area_lifecycle_requests(feature_area_id,request_id,action,payload_sha256,result_json,created_at) VALUES(?,?,?,?,?,?)`, parent.ID, input.RequestID, input.Action, input.PayloadSHA256, string(data), formatTime(input.At)); err != nil {
		return empty, conflict(err)
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return result, nil
}
