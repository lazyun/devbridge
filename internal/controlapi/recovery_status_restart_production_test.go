package controlapi_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

var errRecoveryFault = errors.New("test-owned recovery persistence/reply fault")

type recoveryWindowStore struct {
	recoverystatus.Store
	recoveryChildWindowStore
	window string
	hit    atomic.Bool
}

type recoveryChildWindowStore interface {
	AdmitRecoveryChild(context.Context, string, string, string, recoverystatus.Admission, time.Time) (string, error)
	ReadRecoveryChildPreparation(context.Context, string) (recoverystatus.ChildPreparation, error)
	ClaimRecoveryChildThread(context.Context, recoverystatus.ChildThreadClaim, time.Time) (bool, error)
	ReadRecoveryChildThread(context.Context, string) (recoverystatus.ChildThreadReceipt, bool, error)
	RecordRecoveryChildThread(context.Context, recoverystatus.ChildThreadReceipt, time.Time) error
	ClaimRecoveryChildTurn(context.Context, recoverystatus.ChildTurnClaim, time.Time) (bool, error)
	ReadRecoveryChildTurn(context.Context, string) (recoverystatus.ChildTurnReceipt, bool, error)
	RecordRecoveryChildTurn(context.Context, recoverystatus.ChildTurnReceipt, time.Time) error
}

func (s *recoveryWindowStore) ClaimRecoveryChildThread(ctx context.Context, claim recoverystatus.ChildThreadClaim, now time.Time) (bool, error) {
	claimed, err := s.recoveryChildWindowStore.ClaimRecoveryChildThread(ctx, claim, now)
	if err == nil && s.window == "created-no-turn" && !s.hit.Swap(true) {
		return claimed, errRecoveryFault
	}
	return claimed, err
}

func (s *recoveryWindowStore) BindTurn(ctx context.Context, id, epoch, turn string) (recoverystatus.Record, error) {
	if s.window == "uncertain-start" && !s.hit.Swap(true) {
		return recoverystatus.Record{}, errRecoveryFault
	}
	return s.Store.BindTurn(ctx, id, epoch, turn)
}
func (s *recoveryWindowStore) SaveReport(ctx context.Context, id, epoch string, report, after []byte) (recoverystatus.Record, error) {
	if s.window == "report-not-saved" && !s.hit.Swap(true) {
		return recoverystatus.Record{}, errRecoveryFault
	}
	return s.Store.SaveReport(ctx, id, epoch, report, after)
}
func (s *recoveryWindowStore) SaveAssessment(ctx context.Context, id, epoch string, a, f []byte) (recoverystatus.Record, error) {
	if s.window == "report-saved" && !s.hit.Swap(true) {
		return recoverystatus.Record{}, errRecoveryFault
	}
	r, e := s.Store.SaveAssessment(ctx, id, epoch, a, f)
	if e == nil && s.window == "assessment-saved" && !s.hit.Swap(true) {
		return r, errRecoveryFault
	}
	return r, e
}
func (s *recoveryWindowStore) Resolve(ctx context.Context, in recoverystatus.ResolveInput) (recoverystatus.Record, error) {
	r, e := s.Store.Resolve(ctx, in)
	if e == nil && s.window == "resolution-reply-lost" && !s.hit.Swap(true) {
		return r, errRecoveryFault
	}
	return r, e
}
func reopenRecoveryFixture(t *testing.T, f *upperLifecycleFixture, cfg controlplane.Config) {
	t.Helper()
	if e := f.plane.Close(); e != nil {
		t.Fatal(e)
	}
	f.plane = nil
	var e error
	f.plane, e = controlplane.Open(f.ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.facade, e = controlapi.NewFromControlPlane(f.plane)
	if e != nil {
		t.Fatal(e)
	}
}
func waitRecoveryRecord(t *testing.T, f *upperLifecycleFixture, id string, predicate func(recoverystatus.Record) bool) recoverystatus.Record {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var r recoverystatus.Record
	for time.Now().Before(deadline) {
		v, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, id)
		if e != nil {
			t.Fatal(e)
		}
		r = v.Record
		if predicate(r) {
			return r
		}
		if r.AnalysisRequired {
			submitRecoveryFixtureAnalysis(t, f, r)
			continue
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("recovery timeout state=%s failure=%s", r.State, r.FailureReason)
	return r
}
func resolveRecoveryFixture(t *testing.T, f *upperLifecycleFixture, cmd controlapi.RecoveryStatusCommand, r recoverystatus.Record) (controlapi.RecoveryStatusResult, error) {
	t.Helper()
	cmd.ExpectedRecoveryEpoch = r.RuntimeEpoch
	in := controlapi.RecoveryStatusResolveCommand{RecoveryStatusCommand: cmd, AssessmentHash: r.AssessmentHash, ReportHash: r.ReportHash}
	in.RequestID = "restart-resolution"
	in.PayloadHash = (recoverystatus.ResolveInput{ID: in.ID, RequestID: in.RequestID, RuntimeEpoch: in.ExpectedRecoveryEpoch, AssessmentHash: in.AssessmentHash, ReportHash: in.ReportHash}).ComputePayloadHash()
	return f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, in)
}

// All positive persistence windows use the facade, real native App Server,
// managed dual-repository workset and SQLite stores. Faults wrap a persistence
// boundary; no report, assessment, phase or successful Run is inserted by tests.
func TestRecoveryStatusOfflineRestartPersistenceWindows(t *testing.T) {
	for _, window := range []string{"prepared-no-run", "created-no-turn", "uncertain-start", "report-not-saved", "report-saved", "assessment-saved", "resolution-reply-lost"} {
		t.Run(window, func(t *testing.T) {
			runRecoveryStatusOffline(t, "complete-old-unknown", func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
				originalStore := f.plane.RecoveryStatus.Store
				injected := &recoveryWindowStore{Store: originalStore, recoveryChildWindowStore: originalStore.(recoveryChildWindowStore), window: window}
				f.plane.RecoveryStatus.Store = injected
				originalEpoch := cmd.ExpectedRecoveryEpoch
				if window == "prepared-no-run" {
					reopenRecoveryFixture(t, f, cfg)
					r, e := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
					if e != nil || r.RecoveryRunID != "" || calls.Load() != 3 {
						t.Fatalf("prepared startup dispatched %+v %v calls=%d", r, e, calls.Load())
					}
					cmd.ExpectedRecoveryEpoch = r.RuntimeEpoch
				}
				_, startErr := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
				if window == "created-no-turn" {
					if startErr == nil || !injected.hit.Load() {
						t.Fatal("missing create reply fault")
					}
					before, e := originalStore.Read(f.ctx, cmd.ID)
					if e != nil {
						t.Fatal(e)
					}
					reopenRecoveryFixture(t, f, cfg)
					r, e := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
					if e != nil || r.RecoveryRunID != before.RecoveryRunID || r.RecoveryTurnID != "" || calls.Load() != 3 {
						t.Fatalf("created restart redispatched %+v %v calls=%d", r, e, calls.Load())
					}
					cmd.ExpectedRecoveryEpoch = r.RuntimeEpoch
					_, startErr = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
				}
				if startErr != nil {
					t.Fatal(startErr)
				}
				if window == "uncertain-start" {
					before := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool {
						return injected.hit.Load() && r.State == "UNKNOWN" && r.FailureReason != ""
					})
					if before.RecoveryTurnID != "" {
						t.Fatal("lost persistence invented Turn")
					}
					reopenRecoveryFixture(t, f, cfg)
					r, e := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
					if e != nil {
						t.Fatal(e)
					}
					cmd.ExpectedRecoveryEpoch = r.RuntimeEpoch
					for i := 0; i < 3; i++ {
						if _, e = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); e != nil {
							t.Fatal(e)
						}
					}
					time.Sleep(100 * time.Millisecond)
					r, e = f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
					// The accepted first dispatch may reach the provider after BindTurn
					// persistence fails. Three source calls plus at most that one
					// accepted recovery call is the exact no-redispatch bound.
					if e != nil || r.State != "UNKNOWN" || r.RecoveryRunID != before.RecoveryRunID || r.RecoveryTurnID != "" || calls.Load() > 4 {
						t.Fatalf("uncertain start replayed state=%s run=%s turn=%s err=%v calls=%d", r.State, r.RecoveryRunID, r.RecoveryTurnID, e, calls.Load())
					}
					return false
				}
				var before recoverystatus.Record
				if window == "report-not-saved" || window == "report-saved" || window == "assessment-saved" {
					before = waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool {
						return injected.hit.Load() && (r.State == "REPORT_READY" || r.State == "ASSESSED" || (r.State == "UNKNOWN" && r.FailureReason != ""))
					})
					reopenRecoveryFixture(t, f, cfg)
				}
				assessed := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
				if assessed.State != "ASSESSED" || calls.Load() != 4 {
					t.Fatalf("restart did not converge %+v calls=%d", assessed, calls.Load())
				}
				if before.ReportHash != "" && before.ReportHash != assessed.ReportHash {
					t.Fatal("report bytes changed")
				}
				if before.AssessmentHash != "" && before.AssessmentHash != assessed.AssessmentHash {
					t.Fatal("assessment bytes changed")
				}
				if window != "resolution-reply-lost" {
					resolved, e := resolveRecoveryFixture(t, f, cmd, assessed)
					if e != nil || resolved.Record.State != "RESOLVED" {
						t.Fatalf("resolve %+v %v", resolved, e)
					}
				} else {
					_, e := resolveRecoveryFixture(t, f, cmd, assessed)
					if e == nil || !injected.hit.Load() {
						t.Fatal("missing resolution reply loss")
					}
					replay, e := resolveRecoveryFixture(t, f, cmd, assessed)
					if e != nil || replay.Record.State != "RESOLVED" {
						t.Fatalf("lost resolution replay %+v %v", replay, e)
					}
				}
				if assessed.RuntimeEpoch != originalEpoch {
					if _, e := originalStore.BeforeStart(f.ctx, cmd.ID, originalEpoch); !errors.Is(e, recoverystatus.ErrStale) && !errors.Is(e, runtimeepoch.ErrFenced) {
						t.Fatalf("stale callback accepted: %v", e)
					}
				}
				if calls.Load() != 4 {
					t.Fatal("restart duplicated model dispatch")
				}
				return false
			})
		})
	}
}

func TestRecoveryStatusOfflineActiveTurnCoreRestart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	runRecoveryStatusOfflineProvider(t, "complete-old-unknown", func(n int64) {
		if n == 4 {
			close(entered)
			<-release
		}
	}, func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		if _, e := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); e != nil {
			t.Fatal(e)
		}
		select {
		case <-entered:
		case <-time.After(45 * time.Second):
			t.Fatal("new recovery Turn never reached provider")
		}
		before := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "RUNNING" && r.RecoveryTurnID != "" })
		reopenRecoveryFixture(t, f, cfg)
		if calls.Load() != 4 {
			t.Fatal("restart dispatched replacement Turn")
		}
		if released.CompareAndSwap(false, true) {
			close(release)
		}
		after := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
		if after.State != "ASSESSED" || before.RecoveryRunID != after.RecoveryRunID || before.RecoveryTurnID != after.RecoveryTurnID || before.RuntimeEpoch == after.RuntimeEpoch || calls.Load() != 4 {
			t.Fatalf("active restart changed owner before=%+v after=%+v calls=%d", before, after, calls.Load())
		}
		resolved, e := resolveRecoveryFixture(t, f, cmd, after)
		if e != nil || resolved.Record.State != "RESOLVED" {
			t.Fatalf("resolution %+v %v", resolved, e)
		}
		return false
	})
}

func submitRecoveryFixtureAnalysis(t *testing.T, f *upperLifecycleFixture, r recoverystatus.Record) {
	t.Helper()
	if len(f.recoveryReport) == 0 {
		t.Fatal("missing authored GPT recovery fixture")
	}
	source, err := f.facade.GetRunAnalysis(f.ctx, f.session, r.RecoveryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if source.State == "RECORDED" {
		return
	}
	if _, err = f.facade.SubmitRunAnalysis(f.ctx, runanalysis.Input{AISessionID: f.session, RunID: r.RecoveryRunID, RequestID: "fixture-gpt-recovery-" + r.RecoveryRunID, SourceHash: source.SourceHash, Report: f.recoveryReport}); err != nil {
		t.Fatal(err)
	}
}
func (s *recoveryWindowStore) SaveNaturalExecution(ctx context.Context, id, epoch string, raw, after, captured []byte) (recoverystatus.Record, error) {
	if s.window == "report-not-saved" && !s.hit.Swap(true) {
		return recoverystatus.Record{}, errRecoveryFault
	}
	return s.Store.(interface {
		SaveNaturalExecution(context.Context, string, string, []byte, []byte, []byte) (recoverystatus.Record, error)
	}).SaveNaturalExecution(ctx, id, epoch, raw, after, captured)
}
