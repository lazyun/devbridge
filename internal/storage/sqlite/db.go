package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/observability"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("sqlite entity not found")
	ErrConflict = errors.New("sqlite constraint conflict")
)

type DB struct {
	// Original file operations hold a shared guard across I/O; lifecycle
	// revocation takes it exclusively without waiting for an active operation.
	directFileOperations sync.RWMutex
	sql                  *sql.DB
	stateRoot            string
	epoch                string
	writer               *dataroot.WriterLock
	closeOnce            sync.Once
	closeErr             error
	// reviewMaterializationFaultHook is a test-only seam at the durable-claim
	// to finalize boundary of the Review materialization protocol. It is nil in
	// production and never changes an effect itself.
	reviewMaterializationFaultHook func(reviewID, stage string) error
	// runtimeBindingFaultHook is used by package-local transaction rollback tests.
	runtimeBindingFaultHook func(stage string) error
}

// SetReviewMaterializationFaultHookForTest exposes the real claim-to-finalize
// crash seam to isolated integration tests. Production callers do not
// configure it.
func (d *DB) SetReviewMaterializationFaultHookForTest(hook func(reviewID, stage string) error) {
	d.reviewMaterializationFaultHook = hook
}

func Open(ctx context.Context, stateRoot string) (*DB, error) {
	if stateRoot == "" {
		return nil, errors.New("state root is required")
	}
	absolute, err := filepath.Abs(stateRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve state root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create state root: %w", err)
	}
	// Retain the database descriptor lock for direct SQLite callers and epoch
	// fencing. Core already holds the outer DataRoot bootstrap lock here.
	writer, err := dataroot.AcquireWriterLock(filepath.Join(absolute, "core-writer.lock"))
	if err != nil {
		observability.Error(ctx, "bootstrap", "runtime.writer_lock.conflict", "DataRoot already has an active writer", map[string]any{"errorCode": "DATA_ROOT_WRITER_LOCKED"})
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = writer.Close()
		}
	}()
	path := filepath.Join(absolute, "devbridge.db")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}).String()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	raw.SetMaxOpenConns(8)
	db := &DB{sql: raw, stateRoot: absolute, writer: writer}
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}
	if err := ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
		_ = raw.Close()
		return nil, err
	}
	// The current schema has explicit versioned migrations and deliberately has no
	// same-version hot migration. Verify a semantic fragment introduced by the
	// current contract instead of accepting an older trigger with the same name.
	var finalContractSQL string
	if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name='approved_execution_binding' AND type='trigger'`).Scan(&finalContractSQL); err != nil || !strings.Contains(finalContractSQL, "decision_kind='TASK_ACCEPTANCE'") {
		_ = raw.Close()
		return nil, errors.New("unreleased development database has an obsolete schema; recreate the development database")
	}
	for name, fragment := range map[string]string{"tasks": "current_review_id", "reviews": "task_id TEXT NOT NULL", "task_review_source_owner": "NEW.task_id", "task_review_gate_receipt_owner": "TASK_REVIEW_GATE"} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("unreleased development database lacks the Task-owned workflow contract; recreate the development database")
		}
	}
	for name, fragment := range map[string]string{"tasks": "required_review_depth", "reviews": "review_depth TEXT NOT NULL", "review_approval_disposition_guard": "APPROVAL_READY", "task_initial_review_depth_guard": "FAST", "review_minimum_depth_guard": "required_review_depth"} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("unreleased development database lacks the v45 Review depth contract; recreate the development database")
		}
	}
	for name, fragment := range map[string]string{"tasks": "'ACCEPTANCE_PENDING','DELIVERING','RECOVERY_REQUIRED'", "review_result_materialization_claims": "MATERIALIZING", "review_materialization_claim_state_transition": "FINALIZED", "review_materialization_claim_identity_immutable": "claim identity is immutable", "file_result_task_accept_guard": "OLD.phase<>'DELIVERING'"} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("unreleased development database lacks the v88 Task lifecycle contract; recreate the development database")
		}
	}
	for name, fragment := range map[string]string{
		"recovery_purpose_confirmation_links":        "PRIMARY KEY(recovery_id,recovery_generation)",
		"recovery_purpose_link_history":              "UNIQUE(recovery_id,recovery_generation,link_ordinal)",
		"recovery_purpose_current_links":             "selection_version INTEGER NOT NULL",
		"recovery_current_generations":               "PRIMARY KEY(owner_kind,owner_id)",
		"confirmation_replacement_receipts":          "UNIQUE(ai_session_id,source_intent_id)",
		"confirmation_current_decisions":             "PRIMARY KEY(ai_session_id,decision_id)",
		"confirmation_replacement_receipt_no_update": "Confirmation replacement receipt immutable",
		"recovery_purpose_confirmation_receipts":     "UNIQUE(intent_ai_session_id,commit_request_id)",
		"recovery_purpose_rejection_receipts":        "REJECTED_NOT_CONSUMED",
		"recovery_purpose_confirmation_consumptions": "UNIQUE(intent_ai_session_id,consume_request_id)",
		"recovery_purpose_link_immutable":            "Recovery purpose link immutable",
		"recovery_purpose_receipt_immutable":         "Recovery purpose receipt immutable",
		"recovery_purpose_rejection_immutable":       "Recovery purpose rejection immutable",
		"recovery_purpose_consumption_immutable":     "Recovery purpose consumption immutable",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("unreleased development database lacks the v89 Recovery purpose contract; recreate the development database")
		}
	}
	for name, fragment := range map[string]string{
		"runtime_scope_plans":                       "owner_kind IN ('IMPLEMENTATION','EXECUTION','VERIFICATION','REVIEW','INSPECTION','RECOVERY_STATUS')",
		"runtime_preparation_authorities":           "plan_id TEXT PRIMARY KEY",
		"runtime_preparation_attempts":              "run_id TEXT NOT NULL UNIQUE",
		"runtime_workspace_preparation_claims":      "run_id TEXT NOT NULL UNIQUE",
		"runtime_workspace_observations":            "UNIQUE(plan_id,run_id,member_id)",
		"runtime_workset_bindings":                  "start_authority_id TEXT NOT NULL UNIQUE",
		"runtime_start_authorities":                 "plan_id TEXT NOT NULL UNIQUE",
		"runtime_preparation_receipts":              "request_id TEXT PRIMARY KEY",
		"runtime_workset_bindings_immutable_UPDATE": "runtime preparation fact immutable",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("unreleased development database lacks the v90 Runtime preparation contract; recreate the development database")
		}
	}
	for name, fragment := range map[string]string{
		"ai_admitted_execution_start_requests":  "PRIMARY KEY(ai_session_id,request_id)",
		"ai_admitted_execution_start_source":    "Admitted start requires the exact completed current admission",
		"ai_admitted_execution_start_immutable": "Admitted start request is immutable",
		"ai_admitted_execution_start_durable":   "Admitted start request is durable",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v109 admitted Execution start request contract")
		}
	}
	for _, name := range []string{"runs_operation_owner_insert", "runs_agent_execution_insert", "recovery_status_inspection_child_v83"} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, "independent_inspection_child") {
			_ = raw.Close()
			return nil, errors.New("database lacks the v110 independent Inspection recovery child contract")
		}
	}
	for name, fragment := range map[string]string{
		"recovery_status_adoption_exact":                "Recovery adoption must retain exact child epoch and owner",
		"recovery_child_preparation_adoptions":          "PRIMARY KEY(recovery_id,current_epoch)",
		"recovery_child_preparation_adoption_exact":     "Recovery preparation adoption must retain exact unstarted owner",
		"recovery_child_preparation_adoption_immutable": "Recovery preparation adoption immutable",
		"recovery_child_preparation_adoption_no_delete": "Recovery preparation adoption durable",
		"recovery_child_thread_attempt_binding":         "recovery_child_preparation_adoptions",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v111 Recovery child adoption contract")
		}
	}
	for name, fragment := range map[string]string{
		"runtime_preparation_failures":                      "SYNCHRONOUS_PREPARER_RETURNED",
		"runtime_preparation_revocations":                   "REFERENCES runtime_preparation_failures",
		"runtime_preparation_abandonments":                  "REFERENCES runtime_preparation_revocations",
		"runtime_preparation_failure_exact":                 "preparation failure requires exact quiescent Upper attempt",
		"runtime_preparation_revocation_exact":              "preparation revocation requires exact abandonment intent",
		"runtime_preparation_abandonment_exact":             "preparation abandonment requires atomic terminal outcomes",
		"runtime_preparation_failures_immutable_UPDATE":     "preparation failure history immutable",
		"runtime_preparation_failures_immutable_DELETE":     "preparation failure history immutable",
		"runtime_preparation_revocations_immutable_UPDATE":  "preparation failure history immutable",
		"runtime_preparation_revocations_immutable_DELETE":  "preparation failure history immutable",
		"runtime_preparation_abandonments_immutable_UPDATE": "preparation failure history immutable",
		"runtime_preparation_abandonments_immutable_DELETE": "preparation failure history immutable",
		"runtime_workspace_observations_failed_guard":       "failed preparation cannot gain runtime authority",
		"runtime_workset_bindings_failed_guard":             "failed preparation cannot gain runtime authority",
		"runtime_start_authorities_failed_guard":            "failed preparation cannot gain runtime authority",
		"runtime_workspace_preparation_claims_upper_guard":  "workspace preparation requires exact active owner",
		"upper_review_grant_transition":                     "runtime_preparation_revocations",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v115 Upper purpose recovery and abandonment contract")
		}
	}
	for name, fragment := range map[string]string{
		"task_delivery_dispatch_intents":   "PRIMARY KEY(task_id,repository_id)",
		"task_delivery_dispatch_results":   "PRIMARY KEY(task_id,repository_id)",
		"task_delivery_dispatch_exact":     "Task delivery dispatch requires current uncanceled admission",
		"task_delivery_result_exact":       "Task delivery result requires exact dispatch",
		"task_delivery_dispatch_immutable": "Task delivery dispatch immutable",
		"task_delivery_dispatch_no_delete": "Task delivery dispatch durable",
		"task_delivery_result_immutable":   "Task delivery result immutable",
		"task_delivery_result_no_delete":   "Task delivery result durable",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v116 Task delivery dispatch contract")
		}
	}
	for name, fragment := range map[string]string{
		"review_purpose_preparation_adoptions": "PRIMARY KEY(run_id,current_epoch)",
		"review_purpose_adoption_exact":        "Review adoption requires exact undispatched purpose preparation",
		"review_purpose_adoption_immutable":    "Review adoption immutable",
		"review_purpose_adoption_no_delete":    "Review adoption durable",
		"runs_runtime_epoch_immutable":         "review_purpose_preparation_adoptions",
		"claims_runtime_epoch_immutable":       "review_purpose_preparation_adoptions",
		"upper_review_grant_binding":           "review_purpose_preparation_adoptions",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v113 Review purpose preparation adoption contract")
		}
	}
	if err := ensureRecoveryChildPreparationGuards(ctx, raw); err != nil {
		_ = raw.Close()
		return nil, err
	}
	for name, fragment := range map[string]string{
		"task_delivery_operations":          "UNIQUE(predecessor_operation_id)",
		"task_delivery_operation_outcomes":  "REJECTED_NOT_CONSUMED",
		"task_delivery_abandonments":        "members_json",
		"delivery_operation_initial_exact":  "Initial delivery operation requires exact frozen dispatch",
		"delivery_operation_retry_exact":    "Delivery retry requires exact current rejected operation",
		"delivery_operation_dispatch_exact": "Delivery dispatch requires current uncanceled operation",
		"delivery_outcome_exact":            "Delivery outcome requires current exact dispatch",
		"delivery_abandonment_exact":        "Delivery abandonment requires settled exact Task cancellation",
		"delivery_abandoned_parent_exact":   "Delivery abandonment requires immutable receipt",
	} {
		var definition string
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, fragment) {
			_ = raw.Close()
			return nil, errors.New("database lacks the v118 Task delivery recovery contract")
		}
	}
	if err := db.allocateRuntimeEpoch(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	opened = true
	return db, nil
}

func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		// Stop new admissions and join existing transactions before closing pinned
		// roots. An enable already holding a transaction cannot leak a late grant.
		dbErr := d.sql.Close()
		invalidateAllDirectFiles(d)
		d.closeErr = errors.Join(dbErr, d.writer.Close())
	})
	return d.closeErr
}
func (d *DB) StateRoot() string    { return d.stateRoot }
func (d *DB) SQL() *sql.DB         { return d.sql }
func (d *DB) RuntimeEpoch() string { return d.epoch }
