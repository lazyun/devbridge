package controlplane_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlplane"
)

func TestSessionInitializationAndBindingAreDurableAndIndependent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	projectValue, err := plane.Projects.Create(ctx, "Only Project")
	if err != nil {
		t.Fatal(err)
	}
	consoleProject, err := plane.Projects.Create(ctx, "Console Project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.ProjectSelection.Select(ctx, consoleProject.ID); err != nil {
		t.Fatal(err)
	}

	ref := aiprojectselection.ClientBindingReference{CallerScope: "controlplane-session-barrier"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "session-barrier-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if initialized.AISessionID == "" || initialized.BoundProjectID != nil || initialized.State != aisession.ProjectSelectionRequired {
		t.Fatalf("unbound initialization=%+v", initialized)
	}
	bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "session-barrier-bind")
	if err != nil {
		t.Fatal(err)
	}
	if bound.Context.Session.ProjectID != projectValue.ID || !bound.IsNewBinding || bound.Receipt.Replayed {
		t.Fatalf("binding=%+v", bound)
	}
	currentConsole, err := plane.ProjectSelection.Current(ctx)
	if err != nil || currentConsole.CurrentSelectedProjectID != consoleProject.ID {
		t.Fatalf("session binding consulted or changed Console selection: %+v err=%v", currentConsole, err)
	}
	tasks, err := plane.Tasks.List(ctx)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("session binding created Task records: %+v err=%v", tasks, err)
	}
	replayed, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "session-barrier-bind")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.IsNewBinding || !replayed.Receipt.Replayed || replayed.Context.Session.ID != initialized.AISessionID {
		t.Fatalf("binding replay=%+v", replayed)
	}

	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	readback, err := plane.AIContext.InitializeSession(ctx, ref, "", initialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	if readback.BoundProjectID == nil || *readback.BoundProjectID != projectValue.ID || readback.AISessionID != initialized.AISessionID {
		t.Fatalf("durable readback=%+v", readback)
	}
	foreign := aiprojectselection.ClientBindingReference{CallerScope: "controlplane-session-barrier-foreign"}
	if _, err := plane.AIContext.InitializeSession(ctx, foreign, "", initialized.AISessionID); !errors.Is(err, aisession.ErrScopeMismatch) {
		t.Fatalf("foreign caller readback error=%v", err)
	}
}

func TestSessionBindingIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "Concurrent Project")
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "controlplane-concurrent-bind"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "concurrent-initialize", "")
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, bindErr := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "concurrent-bind")
			errs <- bindErr
		}()
	}
	wg.Wait()
	close(errs)
	for bindErr := range errs {
		if bindErr != nil {
			t.Fatalf("concurrent bind error=%v", bindErr)
		}
	}
	current, err := plane.AIContext.GetSession(ctx, initialized.AISessionID)
	if err != nil || current.Session.ProjectID != projectValue.ID {
		t.Fatalf("atomic binding=%+v err=%v", current, err)
	}
}
