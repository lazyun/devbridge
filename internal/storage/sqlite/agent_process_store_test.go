package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"testing"
	"time"
)

func TestProcessTelemetryManyToOneExitAndRestart(t *testing.T) {
	f := newAgentStorageFixture(t, "process")
	store := NewAgentRuntimeStore(f.db)
	// Real launch is exercised by the stdio fixture test. Here two distinct Run
	// records bind one observed launch, and identity is independent of PID reuse.
	launch := agentruntime.ProcessLaunch{LaunchID: "agent_process_actual", PID: 48217, Executable: "fixture", StartedAt: testNow}
	if err := store.RecordProcess(context.Background(), launch); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, label := range []string{"one", "two"} {
		area := f.area
		area.ID += "-" + label
		if err := NewFeatureAreaStore(f.db).Create(context.Background(), area); err != nil {
			t.Fatal(err)
		}
		one := f
		one.area = area
		one.task.ID += "-" + label
		one.task.FeatureAreaID = area.ID
		if err := NewTaskStore(f.db).Create(context.Background(), one.task); err != nil {
			t.Fatal(err)
		}
		op := prepareAgentOperation(t, one, agentconversation.Review, label)
		r, err := op.make(context.Background(), "run-process-"+label)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		ctx := runtimeepoch.WithFence(context.Background(), f.db.epoch, r.ID)
		if err = store.BindProcess(ctx, r.ID, launch.LaunchID); err != nil {
			t.Fatal(err)
		}
		if err = store.BindProcess(ctx, r.ID, launch.LaunchID); err != nil {
			t.Fatal("idempotent binding", err)
		}
	}
	if err := store.RecordProcess(context.Background(), agentruntime.ProcessLaunch{LaunchID: "agent_process_reused_pid", PID: launch.PID, Executable: "fixture", StartedAt: testNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.BindProcess(runtimeepoch.WithFence(context.Background(), f.db.epoch, ids[0]), ids[0], "agent_process_reused_pid"); err == nil {
		t.Fatal("PID reuse replaced launch identity")
	}
	if err := store.EndProcess(context.Background(), launch.LaunchID, testNow.Add(time.Minute), 7); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		v, err := store.Read(context.Background(), id)
		if err != nil || v.Agent.ProcessLaunchID != launch.LaunchID || v.Agent.Process == nil || v.Agent.Process.ExitCode == nil || *v.Agent.Process.ExitCode != 7 {
			t.Fatalf("exit read %+v %v", v, err)
		}
	}
	root := f.root
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, root)
	defer db.Close()
	after := NewAgentRuntimeStore(db)
	for _, id := range ids {
		v, err := after.Read(context.Background(), id)
		if err != nil || v.Agent.PID != launch.PID || v.Agent.Process.RuntimeEpoch == db.epoch {
			t.Fatalf("restart guessed launch %+v %v", v, err)
		}
	}
	op := prepareAgentOperation(t, agentStorageFixture{db: db, root: f.root, project: f.project, area: f.area, session: f.session, task: f.task}, agentconversation.Review, "old-no-process")
	r, err := op.make(context.Background(), "run-no-process")
	if err != nil {
		t.Fatal(err)
	}
	v, err := after.Read(context.Background(), r.ID)
	if err != nil || v.Agent.ProcessAvailability != "UNAVAILABLE" || v.Agent.PID != 0 {
		t.Fatalf("guessed history %+v %v", v, err)
	}
}
