package controlapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
)

type runtimeRunnerStub struct {
	startValue  run.Run
	startErr    error
	cancelValue run.Run
	cancelErr   error
}

func (s runtimeRunnerStub) StartRun(context.Context, string) (run.Run, error) {
	return s.startValue, s.startErr
}

func (s runtimeRunnerStub) CancelRun(context.Context, string) (run.Run, error) {
	return s.cancelValue, s.cancelErr
}

func TestControlAPIRuntimeStartAndCancelSeam(t *testing.T) {
	runner := runtimeRunnerStub{
		startValue:  run.Run{ID: "run-1", TaskID: "task-1", Status: run.StatusPreparing},
		cancelValue: run.Run{ID: "run-1", TaskID: "task-1", Status: run.StatusCanceled},
	}
	facade := &Facade{deps: Dependencies{Runner: runner, Runs: scopeRuns{"run-1": runner.startValue}}}
	started, err := facade.StartRun(context.Background(), "task-1")
	if err != nil || started.Status != run.StatusPreparing {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	canceled, err := facade.CancelRun(context.Background(), "run-1")
	if err != nil || canceled.Status != run.StatusCanceled {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
}

func TestControlAPIMapsSameTaskRunConflict(t *testing.T) {
	runner := runtimeRunnerStub{startErr: &execution.TaskAlreadyHasActiveRunError{TaskID: "task-1", RunID: "run-live"}}
	facade := &Facade{deps: Dependencies{Runner: runner}}
	_, err := facade.StartRun(context.Background(), "task-1")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeTaskAlreadyHasActiveRun {
		t.Fatalf("err=%v", err)
	}
}
