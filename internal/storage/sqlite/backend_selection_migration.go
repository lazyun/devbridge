package sqlite

const backendSelectionMigrationSQL = `
ALTER TABLE review_approval_materializations ADD COLUMN backend_selection_json TEXT;
ALTER TABLE execution_proposals ADD COLUMN backend_selection_json TEXT;
ALTER TABLE local_approve_and_start_commands ADD COLUMN backend_selection_json TEXT;
CREATE TRIGGER review_materialization_backend_selection_immutable BEFORE UPDATE OF backend_selection_json ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'Review backend selection is immutable'); END;
CREATE TRIGGER execution_proposal_backend_selection_immutable BEFORE UPDATE OF backend_selection_json ON execution_proposals BEGIN SELECT RAISE(ABORT,'Execution backend selection is immutable'); END;
CREATE TRIGGER local_approve_and_start_backend_selection_immutable BEFORE UPDATE OF backend_selection_json ON local_approve_and_start_commands BEGIN SELECT RAISE(ABORT,'Local backend selection is immutable'); END;
`
