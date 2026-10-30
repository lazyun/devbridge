package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestLifecycleReceiptReplayRequiresNoWriter(t *testing.T) {
	for _, action := range []featurearea.LifecycleAction{featurearea.ActionActivate, featurearea.ActionDecompose, featurearea.ActionRetire} {
		t.Run(string(action), func(t *testing.T) {
			f, service := lifecycleFixture(t, "readback-"+string(action))
			ctx := context.Background()
			freshArea := f.area
			freshArea.ID += "-fresh"
			if err := NewFeatureAreaStore(f.db).Create(ctx, freshArea); err != nil {
				t.Fatal(err)
			}
			input := decompositionInput(f)
			request := "lifecycle-readback"
			input.RequestID = request
			call := func(ctx context.Context) (featurearea.LifecycleResult, error) {
				if action == featurearea.ActionDecompose {
					return service.Decompose(ctx, input)
				}
				var area featurearea.FeatureArea
				var err error
				if action == featurearea.ActionActivate {
					area, err = service.Activate(ctx, f.session.ID, f.area.ID, request)
				} else {
					area, err = service.Retire(ctx, f.session.ID, f.area.ID, request)
				}
				return featurearea.LifecycleResult{Parent: area}, err
			}
			want, err := call(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"warm", "advanced", "cold"} {
				if phase == "advanced" {
					switch action {
					case featurearea.ActionActivate:
						if _, err = service.Retire(ctx, f.session.ID, f.area.ID, "later-retirement"); err != nil {
							t.Fatal(err)
						}
					case featurearea.ActionDecompose:
						if _, err = service.Activate(ctx, f.session.ID, want.Children[0].ID, "activate-child"); err != nil {
							t.Fatal(err)
						}
					case featurearea.ActionRetire:
						if err = NewFeatureAreaStore(f.db).FinishRetirement(ctx, f.area.ID, ""); err != nil {
							t.Fatal(err)
						}
						want.Parent, err = NewFeatureAreaStore(f.db).Get(ctx, f.area.ID)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if phase == "cold" {
					if err = f.db.Close(); err != nil {
						t.Fatal(err)
					}
					f.db = openTestDB(t, f.root)
					defer f.db.Close()
					service = ownershipLifecycleService(t, f)
				}
				if _, err = f.db.sql.Exec(`CREATE TRIGGER lifecycle_no_writer BEFORE UPDATE ON runtime_authority BEGIN SELECT RAISE(ABORT,'lifecycle replay attempted write'); END`); err != nil {
					t.Fatal(err)
				}
				for _, readCtx := range []context.Context{ctx, runtimeepoch.WithFence(ctx, "obsolete-lifecycle-reader", "")} {
					got, err := call(readCtx)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("%s replay got=%+v want=%+v err=%v", phase, got, want, err)
					}
					conflictAction := featurearea.ActionRetire
					if action == featurearea.ActionRetire {
						conflictAction = featurearea.ActionActivate
					}
					_, err = NewFeatureAreaStore(f.db).ApplyLifecycle(readCtx, featurearea.LifecycleMutation{ProjectID: f.project.ID, FeatureAreaID: f.area.ID, RequestID: request, Action: conflictAction, PayloadSHA256: "changed"})
					if !errors.Is(err, featurearea.ErrConflict) {
						t.Errorf("%s changed action/payload: %v", phase, err)
					}
					_, err = NewFeatureAreaStore(f.db).ApplyLifecycle(readCtx, featurearea.LifecycleMutation{ProjectID: f.project.ID, FeatureAreaID: f.area.ID, RequestID: request, Action: action, PayloadSHA256: "changed"})
					if !errors.Is(err, featurearea.ErrConflict) {
						t.Errorf("%s same action with changed payload: %v", phase, err)
					}
					_, err = NewFeatureAreaStore(f.db).ApplyLifecycle(readCtx, featurearea.LifecycleMutation{ProjectID: "foreign-project", FeatureAreaID: f.area.ID, RequestID: request, Action: action})
					if err == nil {
						t.Fatal("foreign project replayed lifecycle")
					}
					if _, err = service.Activate(readCtx, f.session.ID, freshArea.ID, "fresh-without-writer"); err == nil || (!errors.Is(err, runtimeepoch.ErrFenced) && !strings.Contains(err.Error(), "lifecycle replay attempted write")) {
						t.Fatalf("otherwise eligible fresh mutation must require writer: %v", err)
					}
				}
				if _, err = f.db.sql.Exec(`DROP TRIGGER lifecycle_no_writer`); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
