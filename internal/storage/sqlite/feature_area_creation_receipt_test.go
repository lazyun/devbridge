package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/featurearea"
)

func TestFeatureAreaCreationReceiptMigrationPreservesExistingFeatureArea(t *testing.T) {
	db := reviewDepthLegacyDB(t, "REVIEW_REQUIRED", "ACTIVE", "", false)
	if err := ApplyMigrations(context.Background(), db, []Migration{{Version: 44, ForeignKeysOff: true, Apply: migrateFinalV44}, {Version: 45, ForeignKeysOff: true, Apply: migrateReviewDepthV45}, {Version: 46, ForeignKeysOff: true, Apply: migrateExecutionRuntimeConfigV46}}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT json_array(id,project_id,title,problem_statement,status) FROM feature_areas WHERE id='area'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), db, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	var after string
	var version int
	if err := db.QueryRow(`SELECT json_array(id,project_id,title,problem_statement,status) FROM feature_areas WHERE id='area'`).Scan(&after); err != nil || before != after {
		t.Fatalf("existing Feature Area changed: before=%s after=%s err=%v", before, after, err)
	}
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != DefaultMigrations()[len(DefaultMigrations())-1].Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func sqliteCreationReceipt(f agentStorageFixture, requestID string) featurearea.CreationReceipt {
	return featurearea.CreationReceipt{AISessionID: f.session.ID, ProjectID: f.project.ID, Command: "FEATURE_AREA_CREATE", RequestID: requestID, PayloadSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContractVersion: "v1", Prepared: json.RawMessage(`{"repositoryId":"agent-repo","developmentBaseline":"fixed"}`)}
}

func sqliteReceiptedArea(f agentStorageFixture, id string) featurearea.FeatureArea {
	return featurearea.FeatureArea{ID: id, ProjectID: f.project.ID, CreatedByAISessionID: f.session.ID, Title: "Receipted " + id, ProblemStatement: "Persist creation receipt with Feature Area.", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
}

func TestFeatureAreaCreationReceiptSurvivesReopenAndRejectsPayloadConflict(t *testing.T) {
	f := newAgentStorageFixture(t, "creation-reopen")
	store := NewFeatureAreaStore(f.db)
	receipt := sqliteCreationReceipt(f, "root-reopen")
	area := sqliteReceiptedArea(f, "receipted-reopen")
	receipt.FeatureAreaID = area.ID
	created, saved, err := store.CreateReceipted(context.Background(), area, receipt)
	if err != nil || created.ID != area.ID || saved.FeatureAreaID != area.ID {
		t.Fatalf("created=%+v saved=%+v err=%v", created, saved, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestDB(t, f.root)
	f.db = reopened
	store = NewFeatureAreaStore(reopened)
	replay, replayReceipt, err := store.ReplayCreation(context.Background(), f.session.ID, f.project.ID, receipt.Command, receipt.RequestID, receipt.PayloadSHA256)
	if err != nil || replay.ID != area.ID || string(replayReceipt.Prepared) != string(receipt.Prepared) {
		t.Fatalf("replay=%+v receipt=%+v err=%v", replay, replayReceipt, err)
	}
	if _, _, err := store.ReplayCreation(context.Background(), f.session.ID, f.project.ID, receipt.Command, receipt.RequestID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, featurearea.ErrConflict) {
		t.Fatalf("payload conflict=%v", err)
	}
}

func TestFeatureAreaCreationReceiptConcurrentSameRequestReturnsOneArea(t *testing.T) {
	f := newAgentStorageFixture(t, "creation-concurrent")
	store := NewFeatureAreaStore(f.db)
	const callers = 12
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			receipt := sqliteCreationReceipt(f, "root-concurrent")
			area := sqliteReceiptedArea(f, "receipted-concurrent-"+string(rune('a'+index)))
			receipt.FeatureAreaID = area.ID
			created, _, err := store.CreateReceipted(context.Background(), area, receipt)
			if err == nil {
				ids <- created.ID
			} else {
				errs <- err
			}
		}(i)
	}
	start.Done()
	done.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent create: %v", err)
	}
	var winner string
	for id := range ids {
		if winner == "" {
			winner = id
		}
		if id != winner {
			t.Errorf("returned IDs differ: %s / %s", winner, id)
		}
	}
	if winner == "" {
		t.Fatal("no concurrent caller succeeded")
	}
	var count int
	if err := f.db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM feature_area_creation_receipts WHERE request_id='root-concurrent'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
}

func TestFeatureAreaCreationReceiptFaultRollsBackFeatureAreaAndRejectsRewrite(t *testing.T) {
	f := newAgentStorageFixture(t, "creation-fault")
	store := NewFeatureAreaStore(f.db)
	area := sqliteReceiptedArea(f, "receipted-fault")
	bad := sqliteCreationReceipt(f, "root-fault")
	bad.FeatureAreaID, bad.PayloadSHA256 = area.ID, "not-a-sha"
	if _, _, err := store.CreateReceipted(context.Background(), area, bad); err == nil {
		t.Fatal("invalid receipt unexpectedly committed")
	}
	if _, err := store.Get(context.Background(), area.ID); !errors.Is(err, featurearea.ErrNotFound) {
		t.Fatalf("Feature Area survived failed atomic receipt write: %v", err)
	}
	good := sqliteCreationReceipt(f, "root-fault")
	good.FeatureAreaID = area.ID
	_, saved, err := store.CreateReceipted(context.Background(), area, good)
	if err != nil {
		t.Fatal(err)
	}
	saved.TaskID, saved.ReviewID, saved.RunID, saved.ReviewAdmitted = "task-a", "review-a", "run-a", true
	if err := store.UpdateCreationReceipt(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	saved.Prepared = json.RawMessage(`{"changed":true}`)
	if err := store.UpdateCreationReceipt(context.Background(), saved); !errors.Is(err, featurearea.ErrConflict) {
		t.Fatalf("prepared rewrite=%v", err)
	}
}
