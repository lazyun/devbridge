package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

type recoveryColdWindowStore struct {
	recoverystatus.Store
	recoveryChildWindowStore
	stage string
	hit   atomic.Bool
}

func (s *recoveryColdWindowStore) ReadRecoveryChildPreparation(ctx context.Context, id string) (recoverystatus.ChildPreparation, error) {
	if s.stage == "admitted" && !s.hit.Swap(true) {
		return recoverystatus.ChildPreparation{}, errRecoveryFault
	}
	return s.recoveryChildWindowStore.ReadRecoveryChildPreparation(ctx, id)
}
func (s *recoveryColdWindowStore) ClaimRecoveryChildThread(ctx context.Context, in recoverystatus.ChildThreadClaim, now time.Time) (bool, error) {
	if s.stage == "bound" && !s.hit.Swap(true) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.recoveryChildWindowStore.ClaimRecoveryChildThread(ctx, in, now)
}
func (s *recoveryColdWindowStore) ClaimRecoveryChildTurn(ctx context.Context, in recoverystatus.ChildTurnClaim, now time.Time) (bool, error) {
	if s.stage == "thread-receipted" && !s.hit.Swap(true) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.recoveryChildWindowStore.ClaimRecoveryChildTurn(ctx, in, now)
}
func (s *recoveryColdWindowStore) BindTurn(ctx context.Context, id, epoch, turn string) (recoverystatus.Record, error) {
	if s.stage == "turn-receipted" && !s.hit.Swap(true) {
		<-ctx.Done()
		return recoverystatus.Record{}, ctx.Err()
	}
	return s.Store.BindTurn(ctx, id, epoch, turn)
}

func (s *recoveryColdWindowStore) RecordRecoveryChildThread(ctx context.Context, in recoverystatus.ChildThreadReceipt, now time.Time) error {
	if s.stage == "thread-uncertain" && !s.hit.Swap(true) {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.recoveryChildWindowStore.RecordRecoveryChildThread(ctx, in, now)
}
func (s *recoveryColdWindowStore) RecordRecoveryChildTurn(ctx context.Context, in recoverystatus.ChildTurnReceipt, now time.Time) error {
	if s.stage == "turn-uncertain" && !s.hit.Swap(true) {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.recoveryChildWindowStore.RecordRecoveryChildTurn(ctx, in, now)
}

type recoveryColdObservedRuns struct {
	*run.Service
	hit atomic.Bool
}

func (s *recoveryColdObservedRuns) BindPreparedRuntimeWorkset(ctx context.Context, plan, request, payload, authority string, w workset.RuntimeWorkset, now time.Time) (run.Run, error) {
	if !s.hit.Swap(true) {
		return run.Run{}, errRecoveryFault
	}
	return s.Service.BindPreparedRuntimeWorkset(ctx, plan, request, payload, authority, w, now)
}

func TestRecoveryChildNativeColdRestartUndispatchedStages(t *testing.T) {
	for _, stage := range []string{"admitted", "observed", "bound", "thread-receipted", "turn-receipted", "thread-uncertain", "turn-uncertain"} {
		t.Run(stage, func(t *testing.T) {
			runRecoveryStatusOfflineNativeSetup(t, "complete-old-unknown", nil, func(f *upperLifecycleFixture) {
				native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
				f.plane.RuntimeQualification.BaseCodexPath = native
				f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(t.TempDir(), "native-qualification")
				configs, err := f.plane.CodexConfigs.List(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, backend := range configs {
					if backend.Enabled {
						if _, err = f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "cold-native-qualification-"+backend.ID); err != nil {
							t.Fatal(err)
						}
					}
				}
				f.plane.Coordinator.QualifiedRuntime = f.plane.RuntimeQualification
				f.facade, err = controlapi.NewFromControlPlane(f.plane)
				if err != nil {
					t.Fatal(err)
				}
			}, func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
				original := f.plane.RecoveryStatus.Store
				injected := &recoveryColdWindowStore{Store: original, recoveryChildWindowStore: original.(recoveryChildWindowStore), stage: stage}
				f.plane.RecoveryStatus.Store = injected
				observed := &recoveryColdObservedRuns{Service: f.plane.Runs}
				if stage == "observed" {
					f.plane.RecoveryStatus.Coordinator.Runs = observed
				}
				_, startErr := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
				if stage == "admitted" || stage == "observed" {
					if startErr == nil {
						t.Fatal("missing synchronous crash seam")
					}
				} else if startErr != nil {
					t.Fatal(startErr)
				}
				deadline := time.Now().Add(15 * time.Second)
				for !injected.hit.Load() && !observed.hit.Load() && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !injected.hit.Load() && !observed.hit.Load() {
					t.Fatal("crash seam not reached")
				}
				before, err := original.Read(f.ctx, cmd.ID)
				if err != nil {
					t.Fatal(err)
				}
				threadBefore, threadFound, err := injected.ReadRecoveryChildThread(f.ctx, "recovery_child_thread_"+cmd.ID)
				if err != nil {
					t.Fatal(err)
				}
				turnBefore, turnFound, err := injected.ReadRecoveryChildTurn(f.ctx, "recovery_child_turn_"+cmd.ID)
				if err != nil {
					t.Fatal(err)
				}
				reopenRecoveryFixture(t, f, cfg)
				current, err := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.RuntimeEpoch == before.RuntimeEpoch || current.RecoveryRunID != before.RecoveryRunID {
					t.Fatalf("restart changed owner: before=%+v after=%+v", before, current)
				}
				cmd.ExpectedRecoveryEpoch = current.RuntimeEpoch
				if _, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
					t.Fatal(err)
				}
				if stage == "thread-uncertain" || stage == "turn-uncertain" {
					for i := 0; i < 3; i++ {
						if _, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
							t.Fatal(err)
						}
					}
					time.Sleep(200 * time.Millisecond)
					final, err := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
					if err != nil || final.RecoveryRunID != before.RecoveryRunID || final.RecoveryTurnID != "" || calls.Load() > 4 {
						t.Fatalf("uncertain child redispatched: %+v calls=%d err=%v", final, calls.Load(), err)
					}
					var threadClaims, turnClaims int
					if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM recovery_child_thread_attempts WHERE recovery_id=?),(SELECT count(*) FROM recovery_child_turn_attempts WHERE recovery_id=?)`, cmd.ID, cmd.ID).Scan(&threadClaims, &turnClaims); err != nil {
						t.Fatal(err)
					}
					wantTurn := 1
					if stage == "thread-uncertain" {
						wantTurn = 0
						if calls.Load() != 3 {
							t.Fatalf("unconfirmed thread dispatched turn: calls=%d", calls.Load())
						}
					}
					if threadClaims != 1 || turnClaims != wantTurn {
						t.Fatalf("duplicate claim: thread=%d turn=%d", threadClaims, turnClaims)
					}
					t.Logf("stage=%s remains without confirmed turn; threadClaims=%d turnClaims=%d providerCalls=%d", stage, threadClaims, turnClaims, calls.Load())
					return false
				}
				final := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool {
					return r.State == "ASSESSED" || r.State == "FAILED" || r.State == "UNKNOWN" && r.FailureReason != ""
				})
				if final.State != "ASSESSED" {
					t.Fatalf("cold child did not finish: state=%s turn=%s failure=%s", final.State, final.RecoveryTurnID, final.FailureReason)
				}
				child, err := f.plane.Runs.Get(f.ctx, final.RecoveryRunID)
				if err != nil {
					t.Fatal(err)
				}
				if threadFound && child.ThreadID != threadBefore.ChildThreadID {
					t.Fatal("restart replaced existing thread")
				}
				if turnFound && final.RecoveryTurnID != turnBefore.ChildTurnID {
					t.Fatal("restart replaced accepted turn")
				}
				var threadClaims, turnClaims int
				if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM recovery_child_thread_attempts WHERE recovery_id=?),(SELECT count(*) FROM recovery_child_turn_attempts WHERE recovery_id=?)`, cmd.ID, cmd.ID).Scan(&threadClaims, &turnClaims); err != nil || threadClaims != 1 || turnClaims != 1 {
					t.Fatalf("cold dispatch claims changed: thread=%d turn=%d err=%v", threadClaims, turnClaims, err)
				}
				if calls.Load() != 4 {
					t.Fatalf("recovery redispatched model work: calls=%d", calls.Load())
				}
				if _, err = resolveRecoveryFixture(t, f, cmd, final); err != nil {
					t.Fatal(err)
				}
				t.Logf("stage=%s child=%s thread=%s turn=%s providerCalls=%d", stage, child.ID, child.ThreadID, child.TurnID, calls.Load())
				return false
			})
		})
	}
}
