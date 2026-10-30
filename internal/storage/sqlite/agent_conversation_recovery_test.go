package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func beginRecoveryRun(t *testing.T, f agentStorageFixture, kind agentconversation.OperationKind, label string, pending bool) (run.Run, agentconversation.Conversation) {
	t.Helper()
	op := prepareAgentOperation(t, f, kind, label)
	runValue, err := op.make(context.Background(), "run-"+label)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
	store := NewAgentConversationStore(f.db)
	conversation, err := store.BeginRun(context.Background(), runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		threadID, turnID := "thread-"+label, "turn-"+label
		config, err := NewCodexConfigStore(f.db).Get(context.Background(), f.task.CodexConfigID)
		if err != nil {
			t.Fatal(err)
		}
		// This fixture establishes an active, reusable runtime boundary.  Use the
		// same durable bind path as production so subsequent Review/Execution
		// admission sees the frozen backend provenance it requires.
		if err := store.BindThreadWithRuntimeProvenance(context.Background(), runValue.ID, threadID, f.task.Model, "agent-recovery-fixture", config.Selection("agent-recovery-fixture")); err != nil {
			t.Fatal(err)
		}
		setAgentRunState(t, f.db, runValue.ID, run.StatusRunning, threadID, turnID)
		if err := store.MarkTurnPending(context.Background(), runValue.ID); err != nil {
			t.Fatal(err)
		}
	}
	return runValue, conversation
}

func reopenAgentFixtureDB(t *testing.T, f *agentStorageFixture) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	t.Cleanup(func() { _ = db.Close() })
}

func TestAgentRecoveryUnknownRetainsClaimAndUnsafeConversation(t *testing.T) {
	f := newAgentStorageFixture(t, "recovery-unknown")
	runValue, conversation := beginRecoveryRun(t, f, agentconversation.Execution, "recovery-unknown", true)
	reopenAgentFixtureDB(t, &f)
	recovery := NewRecoveryStore(f.db)
	if err := recovery.RecoverAgentOperations(context.Background(), testNow.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	claim, err := store.GetClaim(context.Background(), f.area.ID)
	if err != nil || claim.RunID != runValue.ID {
		t.Fatalf("UNKNOWN claim=%+v err=%v", claim, err)
	}
	boundary, err := store.Boundary(context.Background(), runValue.ID)
	if err != nil || boundary.Advance != agentconversation.Unknown || boundary.Quiesced {
		t.Fatalf("UNKNOWN boundary=%+v err=%v", boundary, err)
	}
	current, err := store.Get(context.Background(), conversation.ID)
	if err != nil || current.ContinuationSafe {
		t.Fatalf("UNKNOWN conversation=%+v err=%v", current, err)
	}
	if _, err := store.Reset(context.Background(), f.area.ID, "after-unknown-restart"); !errors.Is(err, agentconversation.ErrBusy) && !errors.Is(err, agentconversation.ErrUnavailable) {
		t.Fatalf("new agent context after UNKNOWN restart err=%v", err)
	}
}

func TestAgentRecoveryNoAdvanceSafelyReleasesClaim(t *testing.T) {
	f := newAgentStorageFixture(t, "recovery-no-advance")
	runValue, conversation := beginRecoveryRun(t, f, agentconversation.Execution, "recovery-no-advance", false)
	reopenAgentFixtureDB(t, &f)
	recovery := NewRecoveryStore(f.db)
	if err := recovery.RecoverAgentOperations(context.Background(), testNow.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	if _, err := store.GetClaim(context.Background(), f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("NO_ADVANCE claim after recovery err=%v", err)
	}
	boundary, err := store.Boundary(context.Background(), runValue.ID)
	if err != nil || boundary.Advance != agentconversation.NoAdvance || !boundary.Quiesced {
		t.Fatalf("NO_ADVANCE boundary=%+v err=%v", boundary, err)
	}
	current, err := store.Get(context.Background(), conversation.ID)
	if err != nil || !current.ContinuationSafe || current.CurrentBoundaryTurnID != "" {
		t.Fatalf("NO_ADVANCE conversation=%+v err=%v", current, err)
	}
	if _, err := prepareAgentOperation(t, f, agentconversation.Review, "after-no-advance-restart").make(context.Background(), "run-after-no-advance-restart"); err != nil {
		t.Fatalf("new operation after safe recovery: %v", err)
	}
}

func TestFeatureAreaTerminalLifecycleEndsOnlyItsCurrentConversation(t *testing.T) {
	for _, action := range []string{"retire", "decompose"} {
		t.Run(action, func(t *testing.T) {
			f, service := lifecycleFixture(t, "terminal-"+action)
			ctx := context.Background()
			if action == "retire" {
				if _, err := service.Activate(ctx, f.session.ID, f.area.ID, "activate"); err != nil {
					t.Fatal(err)
				}
			}
			store := NewAgentConversationStore(f.db)
			current, err := store.Reset(ctx, f.area.ID, "establish-current")
			if err != nil {
				t.Fatal(err)
			}
			if action == "retire" {
				_, err = service.Retire(ctx, f.session.ID, f.area.ID, "retire")
				if err == nil {
					err = NewFeatureAreaStore(f.db).FinishRetirement(ctx, f.area.ID, "")
				}
			} else {
				_, err = service.Decompose(ctx, decompositionInput(f))
			}
			if err != nil {
				t.Fatal(err)
			}
			ended, err := store.Get(ctx, current.ID)
			if err != nil || ended.EndedAt == nil {
				t.Fatalf("ended=%+v err=%v", ended, err)
			}
			if _, err := store.Current(ctx, f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
				t.Fatalf("terminal current=%v", err)
			}
			history, err := store.ListByFeatureArea(ctx, f.area.ID)
			if err != nil || len(history) != 2 {
				t.Fatalf("history=%+v err=%v", history, err)
			}
		})
	}
}
func TestSplitChildDoesNotCopyParentConversation(t *testing.T) {
	f, service := lifecycleFixture(t, "split-child")
	ctx := context.Background()
	store := NewAgentConversationStore(f.db)
	parent, err := store.Reset(ctx, f.area.ID, "parent-current")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Decompose(ctx, decompositionInput(f))
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Get(ctx, parent.ID)
	if err != nil || old.EndedAt == nil {
		t.Fatalf("parent=%+v err=%v", old, err)
	}
	for _, child := range result.Children {
		if _, err := store.Current(ctx, child.ID); !errors.Is(err, agentconversation.ErrNotFound) {
			t.Fatalf("child inherited context=%v", err)
		}
		if history, err := store.ListByFeatureArea(ctx, child.ID); err != nil || len(history) != 0 {
			t.Fatalf("child history=%+v err=%v", history, err)
		}
	}
}

func TestTechnicalRotationUnsafeAndContractMismatch(t *testing.T) {
	t.Run("unsafe boundary", func(t *testing.T) {
		f := newAgentStorageFixture(t, "rotate-unsafe")
		runValue, original := beginRecoveryRun(t, f, agentconversation.Execution, "rotate-unsafe", true)
		store := NewAgentConversationStore(f.db)
		if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.Unknown, "", true); err != nil {
			t.Fatal(err)
		}
		if err := terminalizeRun(t, f.db, runValue.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RotateTechnical(context.Background(), f.area.ID, original.ID, agentconversation.UnsafeContextBoundary); !errors.Is(err, agentconversation.ErrBusy) {
			t.Fatalf("technical rotation of UNKNOWN owner err=%v", err)
		}
		old, err := store.Get(context.Background(), original.ID)
		if err != nil || old.EndedAt != nil || old.ContinuationSafe {
			t.Fatalf("old=%+v err=%v", old, err)
		}
	})

	t.Run("contract mismatch", func(t *testing.T) {
		f := newAgentStorageFixture(t, "rotate-contract")
		store := NewAgentConversationStore(f.db)
		const oldID = "conversation_old_contract"
		// Seed the same FeatureArea owner identity as a real conversation.  The
		// current schema validates predecessor ownership, so omitting these
		// columns constructs an impossible legacy row rather than a contract
		// mismatch candidate.
		if _, err := f.db.sql.Exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,owner_kind,owner_id,backend,host_tool_contract_version,created_at) VALUES(?,?,?,?,?,'CODEX',?,?)`, oldID, f.project.ID, f.area.ID, agentconversation.OwnerFeatureArea, f.area.ID, agentconversation.HostToolContractVersion+1, formatTime(testNow)); err != nil {
			t.Fatal(err)
		}
		next, err := store.RotateTechnical(context.Background(), f.area.ID, oldID, agentconversation.HostToolContractUpgrade)
		if err != nil || next.PredecessorID != oldID || next.HostToolContractVersion != agentconversation.HostToolContractVersion {
			t.Fatalf("next=%+v err=%v", next, err)
		}
		old, err := store.Get(context.Background(), oldID)
		if err != nil || old.EndedAt == nil || old.RotationReason != agentconversation.HostToolContractUpgrade {
			t.Fatalf("old=%+v err=%v", old, err)
		}
	})
}

func TestBeginRunRejectsUnknownClaimAndRotatesContractMismatch(t *testing.T) {
	t.Run("quiesced unknown", func(t *testing.T) {
		f := newAgentStorageFixture(t, "begin-rotate-unknown")
		firstRun, original := beginRecoveryRun(t, f, agentconversation.Review, "begin-rotate-unknown-first", true)
		store := NewAgentConversationStore(f.db)
		if err := store.FinishRun(context.Background(), firstRun.ID, agentconversation.Unknown, "", true); err != nil {
			t.Fatal(err)
		}
		if err := terminalizeRun(t, f.db, firstRun.ID); err != nil {
			t.Fatal(err)
		}
		second := prepareAgentOperation(t, f, agentconversation.Review, "begin-rotate-unknown-second")
		if _, err := second.make(context.Background(), "run-begin-rotate-unknown-second"); !errors.Is(err, agentconversation.ErrBusy) {
			t.Fatalf("second Run after UNKNOWN err=%v", err)
		}
		old, err := store.Get(context.Background(), original.ID)
		if err != nil || old.EndedAt != nil || old.ContinuationSafe {
			t.Fatalf("old conversation=%+v err=%v", old, err)
		}
		boundary, err := store.Boundary(context.Background(), firstRun.ID)
		if err != nil || boundary.AgentConversationID != original.ID || boundary.Advance != agentconversation.Unknown || boundary.ExitBoundaryTurnID != "" || boundary.Quiesced {
			t.Fatalf("old boundary=%+v err=%v", boundary, err)
		}
	})

	t.Run("contract mismatch", func(t *testing.T) {
		f := newAgentStorageFixture(t, "begin-rotate-contract")
		store := NewAgentConversationStore(f.db)
		const oldID = "conversation_begin_old_contract"
		// A contract upgrade may rotate an otherwise unstarted conversation.  A
		// raw Thread/Turn without its frozen backend provenance is now rejected
		// before BeginRun, so it is not a valid mismatch fixture.
		if _, err := f.db.sql.Exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,owner_kind,owner_id,backend,host_tool_contract_version,created_at) VALUES(?,?,?,?,?,'CODEX',?,?)`, oldID, f.project.ID, f.area.ID, agentconversation.OwnerFeatureArea, f.area.ID, agentconversation.HostToolContractVersion+1, formatTime(testNow)); err != nil {
			t.Fatal(err)
		}
		op := prepareAgentOperation(t, f, agentconversation.Execution, "begin-rotate-contract")
		runValue, err := op.make(context.Background(), "run-begin-rotate-contract")
		if err != nil {
			t.Fatal(err)
		}
		setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
		next, err := store.BeginRun(context.Background(), runValue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.PredecessorID != oldID || next.BackendThreadID != "" || next.CurrentBoundaryTurnID != "" || next.HostToolContractVersion != agentconversation.HostToolContractVersion {
			t.Fatalf("rotated conversation=%+v", next)
		}
		old, err := store.Get(context.Background(), oldID)
		if err != nil || old.EndedAt == nil || old.RotationReason != agentconversation.HostToolContractUpgrade || old.BackendThreadID != "" || old.CurrentBoundaryTurnID != "" {
			t.Fatalf("old conversation=%+v err=%v", old, err)
		}
		boundary, err := store.Boundary(context.Background(), runValue.ID)
		if err != nil || boundary.AgentConversationID != next.ID || boundary.EntryBoundaryTurnID != "" || boundary.ExitBoundaryTurnID != "" {
			t.Fatalf("new boundary=%+v err=%v", boundary, err)
		}
	})
}

func TestLateOwnerCannotAdvanceResetSuccessor(t *testing.T) {
	f := newAgentStorageFixture(t, "late-reset-successor")
	runValue, original := beginRecoveryRun(t, f, agentconversation.Review, "late-reset-successor", false)
	store := NewAgentConversationStore(f.db)
	if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if err := terminalizeRun(t, f.db, runValue.ID); err != nil {
		t.Fatal(err)
	}
	successor, err := store.Reset(context.Background(), f.area.ID, "reset-after-old-owner")
	if err != nil {
		t.Fatal(err)
	}
	if successor.PredecessorID != original.ID || successor.BackendThreadID != "" || successor.CurrentBoundaryTurnID != "" || !successor.ContinuationSafe {
		t.Fatalf("reset successor=%+v", successor)
	}
	if err := store.BindThread(context.Background(), runValue.ID, "late-thread"); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late BindThread err=%v", err)
	}
	if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.KnownAdvance, "late-turn", true); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late FinishRun err=%v", err)
	}
	unchanged, err := store.Get(context.Background(), successor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.BackendThreadID != "" || unchanged.CurrentBoundaryTurnID != "" || !unchanged.ContinuationSafe || unchanged.EndedAt != nil {
		t.Fatalf("late owner mutated successor=%+v", unchanged)
	}
	boundary, err := store.Boundary(context.Background(), runValue.ID)
	if err != nil || boundary.AgentConversationID != original.ID || boundary.Advance != agentconversation.NoAdvance || boundary.ExitBoundaryTurnID != "" || !boundary.Quiesced {
		t.Fatalf("old boundary=%+v err=%v", boundary, err)
	}
}

func TestCompletedBoundaryImmutableAndLateOwnerRejected(t *testing.T) {
	f := newAgentStorageFixture(t, "late-owner")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Execution, "late-owner", true)
	store := NewAgentConversationStore(f.db)
	if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.KnownAdvance, "turn-late-owner", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`UPDATE agent_operation_boundaries SET exit_boundary_turn_id='late-rewrite' WHERE run_id=?`, runValue.ID); err == nil || !errors.Is(conflict(err), ErrConflict) {
		t.Fatalf("rewrite completed boundary err=%v", err)
	}
	if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.KnownAdvance, "turn-late-owner", true); err != nil {
		t.Fatalf("idempotent duplicate finish err=%v", err)
	}
	if err := terminalizeRun(t, f.db, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BindThread(context.Background(), runValue.ID, "thread-late-owner"); !errors.Is(err, agentconversation.ErrConflict) {
		t.Fatalf("late owner bind err=%v", err)
	}
	if _, err := store.AuthorizeRun(context.Background(), runValue.ID); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("late owner authorization err=%v", err)
	}
}
