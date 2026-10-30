package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Migration struct {
	Version        int
	SQL            string
	ForeignKeysOff bool
	Apply          func(context.Context, *sql.Tx) error
}

func DefaultMigrations() []Migration {
	return []Migration{{Version: 44, ForeignKeysOff: true, Apply: migrateFinalV44}, {Version: 45, ForeignKeysOff: true, Apply: migrateReviewDepthV45}, {Version: 46, ForeignKeysOff: true, Apply: migrateExecutionRuntimeConfigV46}, {Version: 47, Apply: migrateFeatureAreaCreationReceiptV47}, {Version: 48, ForeignKeysOff: true, Apply: migrateUnifiedReviewV48}, {Version: 49, Apply: migrateUpperReviewProtocolV49}, {Version: 50, Apply: migrateClientBindingsV50}, {Version: 51, ForeignKeysOff: true, Apply: migrateSessionFirstV51}, {Version: 52, Apply: migrateDirectFileTaskV52}, {Version: 53, Apply: migrateExecutionInstructionsV53}, {Version: 54, Apply: migrateImplementationContinuationV54}, {Version: 55, ForeignKeysOff: true, Apply: migrateSupplementalReviewV55}, {Version: 56, Apply: migrateConfirmationV56}, {Version: 57, ForeignKeysOff: true, Apply: migrateOperatorCancellationV57}, {Version: 58, Apply: migrateConfirmationOutcomeV58}, {Version: 59, Apply: migrateTaskOptionalConfigV59}, {Version: 60, SQL: runtimeHandoffMigrationSQL}, {Version: 61, SQL: agentSessionHistoryMigrationSQL}, {Version: 62, SQL: decisionActorSourceMigrationSQL}, {Version: 63, ForeignKeysOff: true, SQL: reviewDecisionActorSourceMigrationSQL}, {Version: 64, ForeignKeysOff: true, SQL: supplementalReviewActorSourceMigrationSQL}, {Version: 65, SQL: taskAcceptanceContinuationActorSourceMigrationSQL}, {Version: 66, SQL: operatorExecutionStartReceiptMigrationSQL}, {Version: 67, SQL: executionStartEffectMigrationSQL}, {Version: 68, SQL: localApproveAndStartMigrationSQL}, {Version: 69, ForeignKeysOff: true, Apply: migrateReviewAuthorSourceV69}, {Version: 70, SQL: backendSelectionMigrationSQL}, {Version: 71, SQL: conversationModelMigrationSQL}, {Version: 72, SQL: conversationBackendMigrationSQL}, {Version: 73, SQL: projectCentricMigrationSQL}, {Version: 74, Apply: migrateTaskProjectIntent}, {Version: 75, ForeignKeysOff: true, Apply: migrateMultiRepositoryUpperV75}, {Version: 76, ForeignKeysOff: true, Apply: migrateMultiRepositoryDirectV76}, {Version: 77, ForeignKeysOff: true, SQL: codingRuntimeWorksetV77SQL}, {Version: 78, ForeignKeysOff: true, Apply: migrateRuntimeUserDecisionV78}, {Version: 79, Apply: migrateAppServerContinuityV79}, {Version: 80, SQL: responseDispatchV80SQL}, {Version: 81, ForeignKeysOff: true, Apply: migrateRecoveryStatusV81}, {Version: 82, ForeignKeysOff: true, Apply: migrateProjectInspectionV82}, {Version: 83, ForeignKeysOff: true, Apply: migrateRecoveryStatusInspectionV83}, {Version: 84, ForeignKeysOff: true, Apply: migrateRepositoryUnlinkV84}, {Version: 85, SQL: reviewResultMaterializationV85SQL}, {Version: 86, SQL: reReviewSuccessorV86SQL}, {Version: 87, SQL: invalidReviewResolutionV87SQL}, {Version: 88, ForeignKeysOff: true, Apply: migrateTaskLifecycleV88}, {Version: 89, SQL: recoveryPurposeMigrationV89SQL, Apply: migrateRecoveryPurposeV89}, {Version: 90, ForeignKeysOff: true, SQL: runtimePreparationMigrationV90SQL, Apply: migrateRuntimePreparationV90}, {Version: 91, ForeignKeysOff: true, Apply: migrateExecutionAdmissionV91}, {Version: 92, SQL: recoveryChildAuthorityV92SQL, Apply: migrateRecoveryChildAuthorityV92}, {Version: 93, Apply: migrateRecoveryWorkspaceV93}, {Version: 94, ForeignKeysOff: true, Apply: migrateInstructionAdmissionV94}, {Version: 95, ForeignKeysOff: true, Apply: migrateNoRunSuccessorV95}, {Version: 96, Apply: migrateNoRunRevisionV96}, {Version: 97, SQL: runtimePlatformV97SQL}, {Version: 98, SQL: confirmationCompletionV98SQL}, {Version: 99, SQL: formalDefinitionApprovalV99SQL}, {Version: 100, SQL: operatorExecutionStartSelectionV100SQL}, {Version: 101, SQL: runtimeSelectionV101SQL}, {Version: 102, ForeignKeysOff: true, Apply: migrateFormalExecutionV102}, {Version: 103, SQL: backendPlatformV103SQL}, {Version: 104, SQL: runtimeSelectionAdoptionV104SQL}, {Version: 105, ForeignKeysOff: true, Apply: migrateBackendPlatformV105}, {Version: 106, ForeignKeysOff: true, Apply: migrateRuntimeSelectionV106}, {Version: 107, Apply: migrateRecoveryPurposeV107}, {Version: 108, ForeignKeysOff: true, Apply: migrateFormalConfirmationV108}, {Version: 109, SQL: adoptionStartV109SQL}, {Version: 110, Apply: migrateRecoveryInspectionChildV110}, {Version: 111, Apply: migrateRecoveryChildColdEpochV111}, {Version: 113, Apply: migrateRecoveryReviewPreparationV113}, {Version: 115, Apply: migrateUpperPurposeRecoveryV115}, {Version: 116, SQL: taskDeliveryDispatchV116SQL}, {Version: 117, Apply: migrateRecoveryChildPreparationV117}, {Version: 118, SQL: deliveryRecoveryV118SQL}, {Version: 119, Apply: migrateFlowDeliveryV119}, {Version: 120, SQL: legacyMergeV120SQL}, {Version: 121, SQL: taskDeliveryContextV121SQL}, {Version: 122, SQL: mixedMergeV122SQL}, {Version: 123, Apply: migrateMergeReleaseV123}, {Version: 124, Apply: migrateCancellationTerminalV124}, {Version: 125, Apply: migrateRecoveryInterruptV125}, {Version: 126, Apply: migrateTaskReviewSkipV126}, {Version: 127, SQL: workflowEventsV127SQL}, {Version: 128, Apply: migrateDirectFileVerificationV128}, {Version: 129, SQL: runtimeSteeringV129SQL}, {Version: 130, SQL: naturalExecutionV130SQL}, {Version: 131, SQL: taskVerificationDecisionsV131SQL, Apply: migrateTaskVerificationDecisionsV131}, {Version: 132, SQL: taskCompletionSuccessorV132SQL, Apply: migrateTaskCompletionSuccessorV132}, {Version: 133, Apply: migrateTaskActionNodeV133}, {Version: 134, Apply: migrateTaskActionReconciliationV134}, {Version: 135, SQL: projectAuthorizationV135SQL}}
}
func init() { _ = conversationModelMigrationSQL }

func ApplyMigrations(ctx context.Context, db *sql.DB, migrations []Migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("initialize schema migrations: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latest := 0
	for _, migration := range migrations {
		if migration.Version <= latest {
			return fmt.Errorf("migration versions must be strictly increasing: %d", migration.Version)
		}
		latest = migration.Version
	}
	if current > latest {
		return fmt.Errorf("unsupported SQLite schema version %d (latest supported %d)", current, latest)
	}
	for _, migration := range migrations {
		if migration.Version <= current {
			continue
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("open migration %d connection: %w", migration.Version, err)
		}
		foreignKeysDisabled := false
		if migration.ForeignKeysOff {
			if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
				conn.Close()
				return fmt.Errorf("disable foreign keys for migration %d: %w", migration.Version, err)
			}
			foreignKeysDisabled = true
		}
		restoreForeignKeys := func() error {
			if !foreignKeysDisabled {
				return nil
			}
			_, err := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
			foreignKeysDisabled = false
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			_ = restoreForeignKeys()
			conn.Close()
			return fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		_, applyErr := tx.ExecContext(ctx, migration.SQL)
		if applyErr == nil && migration.Apply != nil {
			applyErr = migration.Apply(ctx, tx)
		}
		if err := applyErr; err != nil {
			_ = tx.Rollback()
			_ = restoreForeignKeys()
			conn.Close()
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, migration.Version); err != nil {
			_ = tx.Rollback()
			_ = restoreForeignKeys()
			conn.Close()
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if migration.ForeignKeysOff {
			rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
			if err != nil {
				_ = tx.Rollback()
				_ = restoreForeignKeys()
				conn.Close()
				return fmt.Errorf("check foreign keys for migration %d: %w", migration.Version, err)
			}
			violated := rows.Next()
			readErr := rows.Err()
			closeErr := rows.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				_ = tx.Rollback()
				_ = restoreForeignKeys()
				conn.Close()
				return fmt.Errorf("read foreign key check for migration %d: %w", migration.Version, err)
			}
			if violated {
				_ = tx.Rollback()
				_ = restoreForeignKeys()
				conn.Close()
				return fmt.Errorf("migration %d left a foreign key violation", migration.Version)
			}
		}
		if err := tx.Commit(); err != nil {
			_ = restoreForeignKeys()
			conn.Close()
			return fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
		if err := restoreForeignKeys(); err != nil {
			conn.Close()
			return fmt.Errorf("restore foreign keys after migration %d: %w", migration.Version, err)
		}
		conn.Close()
		current = migration.Version
	}
	return nil
}
