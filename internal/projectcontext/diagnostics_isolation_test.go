package projectcontext

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

var diagnosticsCreated = time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
var diagnosticsPublished = time.Date(2026, 11, 4, 2, 3, 4, 0, time.UTC)
var diagnosticsLater = time.Date(2026, 11, 4, 3, 4, 5, 0, time.UTC)

// Every expected value owns a new literal slice, never a returned/store/input slice.
func diagnosticsOracle(status Status) ProjectContext {
	return ProjectContext{
		ProjectID: "synthetic-project", Status: status,
		RootGuidanceIdentity: "synthetic-identity", ContextFingerprint: "synthetic-fingerprint",
		Diagnostics: []string{"diagnostic-one", "diagnostic-two"}, Result: "synthetic-guidance",
		CreatedAt: diagnosticsCreated, UpdatedAt: diagnosticsPublished,
	}
}

func diagnosticsAssert(t *testing.T, label string, got, want ProjectContext) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v; want independent literal %#v", label, got, want)
	}
}

func diagnosticsGet(t *testing.T, s *MemoryStore) ProjectContext {
	t.Helper()
	got, err := s.Get(context.Background(), "synthetic-project")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func diagnosticsSeed(t *testing.T, status Status, input []string) (*MemoryStore, ProjectContext) {
	t.Helper()
	s := NewMemoryStore()
	created, err := s.EnsureRequired(context.Background(), "synthetic-project", diagnosticsCreated)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsAssert(t, "new required metadata", created, ProjectContext{
		ProjectID: "synthetic-project", Status: StatusRequired,
		CreatedAt: diagnosticsCreated, UpdatedAt: diagnosticsCreated,
	})
	result, err := s.MarkBootstrapped(context.Background(), "synthetic-project", status,
		"synthetic-fingerprint", "synthetic-identity", "synthetic-guidance", input, diagnosticsPublished)
	if err != nil {
		t.Fatal(err)
	}
	return s, result
}

func TestDiagnosticsInputIsolationAndEmptyShapes(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusNoGuidance} {
		for _, shape := range []string{"nil", "empty", "nonempty"} {
			t.Run(string(status)+"/"+shape, func(t *testing.T) {
				var input []string
				switch shape {
				case "empty":
					input = make([]string, 0, 4)
				case "nonempty":
					input = make([]string, 2, 4)
					copy(input, []string{"diagnostic-one", "diagnostic-two"})
				}
				s, returned := diagnosticsSeed(t, status, input)
				want := diagnosticsOracle(status)
				if shape != "nonempty" {
					want.Diagnostics = nil
				}
				diagnosticsAssert(t, "publication return", returned, want)
				previous := diagnosticsGet(t, s)
				diagnosticsAssert(t, "initial read", previous, want)
				if len(input) != 0 {
					input[0] = "caller-input-mutated"
				}
				input = append(input, "caller-input-appended")
				diagnosticsAssert(t, "input mutation leaves publication return", returned, want)
				diagnosticsAssert(t, "input mutation leaves previous read", previous, want)
				diagnosticsAssert(t, "input mutation leaves store", diagnosticsGet(t, s), want)
				returned.Diagnostics = append(returned.Diagnostics, "returned-append")
				diagnosticsAssert(t, "append alone leaves stored length", diagnosticsGet(t, s), want)
			})
		}
	}
}

func TestDiagnosticsReturnMutationIsolation(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusNoGuidance} {
		for _, method := range []string{"MarkBootstrapped", "EnsureRequired", "Get", "MarkNoGuidance"} {
			if method == "MarkNoGuidance" && status != StatusNoGuidance {
				continue
			}
			t.Run(string(status)+"/"+method, func(t *testing.T) {
				input := []string{"diagnostic-one", "diagnostic-two"}
				s, published := diagnosticsSeed(t, status, input)
				previous := diagnosticsGet(t, s)
				var returned ProjectContext
				var err error
				switch method {
				case "MarkBootstrapped":
					returned = published
				case "EnsureRequired":
					returned, err = s.EnsureRequired(context.Background(), "synthetic-project", diagnosticsLater)
				case "Get":
					returned, err = s.Get(context.Background(), "synthetic-project")
				case "MarkNoGuidance":
					returned, err = s.MarkNoGuidance(context.Background(), "synthetic-project", diagnosticsLater)
				}
				if err != nil {
					t.Fatal(err)
				}
				diagnosticsAssert(t, "returned snapshot before mutation", returned, diagnosticsOracle(status))
				returned.Diagnostics[0] = "return-mutated"
				returned.Diagnostics[1] = "return-mutated-again"
				if !reflect.DeepEqual(input, []string{"diagnostic-one", "diagnostic-two"}) {
					t.Errorf("return aliases input: %q", input)
				}
				diagnosticsAssert(t, "return mutation leaves previous Get snapshot", previous, diagnosticsOracle(status))
				diagnosticsAssert(t, "return mutation must not change store", diagnosticsGet(t, s), diagnosticsOracle(status))
			})
		}
	}
}

func TestDiagnosticsSiblingReturnIsolation(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusNoGuidance} {
		t.Run(string(status), func(t *testing.T) {
			s, publication := diagnosticsSeed(t, status, []string{"diagnostic-one", "diagnostic-two"})
			first, err := s.EnsureRequired(context.Background(), "synthetic-project", diagnosticsLater)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.EnsureRequired(context.Background(), "synthetic-project", diagnosticsLater)
			if err != nil {
				t.Fatal(err)
			}
			previous := diagnosticsGet(t, s)
			first.Diagnostics[0] = "first-result-only"
			diagnosticsAssert(t, "first return mutation leaves publication result", publication, diagnosticsOracle(status))
			diagnosticsAssert(t, "first return mutation leaves second return", second, diagnosticsOracle(status))
			diagnosticsAssert(t, "first return mutation leaves prior Get", previous, diagnosticsOracle(status))
			second.Diagnostics[1] = "second-result-only"
			firstWant := diagnosticsOracle(status)
			firstWant.Diagnostics[0] = "first-result-only"
			diagnosticsAssert(t, "second return mutation leaves first return", first, firstWant)
			diagnosticsAssert(t, "sibling mutations leave store", diagnosticsGet(t, s), diagnosticsOracle(status))
		})
	}
}

func TestDiagnosticsStoredReplacementPreservesSnapshots(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusNoGuidance} {
		t.Run(string(status), func(t *testing.T) {
			s, publication := diagnosticsSeed(t, status, []string{"diagnostic-one", "diagnostic-two"})
			ensured, err := s.EnsureRequired(context.Background(), "synthetic-project", diagnosticsLater)
			if err != nil {
				t.Fatal(err)
			}
			previous := diagnosticsGet(t, s)
			var noGuidance ProjectContext
			if status == StatusNoGuidance {
				noGuidance, err = s.MarkNoGuidance(context.Background(), "synthetic-project", diagnosticsLater)
				if err != nil {
					t.Fatal(err)
				}
			}
			replacementInput := []string{"replacement-one", "replacement-two"}
			replacement, err := s.MarkBootstrapped(context.Background(), "synthetic-project", status,
				"replacement-fingerprint", "replacement-identity", "replacement-guidance", replacementInput, diagnosticsLater)
			if err != nil {
				t.Fatal(err)
			}
			wantReplacement := ProjectContext{ProjectID: "synthetic-project", Status: status,
				ContextFingerprint: "replacement-fingerprint", RootGuidanceIdentity: "replacement-identity",
				Result: "replacement-guidance", Diagnostics: []string{"replacement-one", "replacement-two"},
				CreatedAt: diagnosticsCreated, UpdatedAt: diagnosticsLater}
			replacementInput[0] = "replacement-input-mutated"
			diagnosticsAssert(t, "replacement return detached from input", replacement, wantReplacement)
			diagnosticsAssert(t, "new stored metadata", diagnosticsGet(t, s), wantReplacement)
			diagnosticsAssert(t, "old publication snapshot", publication, diagnosticsOracle(status))
			diagnosticsAssert(t, "old EnsureRequired snapshot", ensured, diagnosticsOracle(status))
			diagnosticsAssert(t, "old Get snapshot", previous, diagnosticsOracle(status))
			if status == StatusNoGuidance {
				diagnosticsAssert(t, "old MarkNoGuidance snapshot", noGuidance, diagnosticsOracle(status))
			}
			publication.Diagnostics[0] = "old-return-mutated-after-replacement"
			diagnosticsAssert(t, "old return mutation leaves replacement", diagnosticsGet(t, s), wantReplacement)
		})
	}
}

func TestDiagnosticsOrdinaryMetadataBranches(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if _, err := s.MarkNoGuidance(ctx, "missing", diagnosticsCreated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkNoGuidance missing: %v", err)
	}
	if _, err := s.MarkBootstrapped(ctx, "missing", StatusReady, "f", "i", "g", []string{"d"}, diagnosticsCreated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkBootstrapped missing: %v", err)
	}
	created, err := s.EnsureRequired(ctx, "synthetic-project", diagnosticsCreated)
	if err != nil {
		t.Fatal(err)
	}
	created.Diagnostics = append(created.Diagnostics, "caller-only")
	noGuidance, err := s.MarkNoGuidance(ctx, "synthetic-project", diagnosticsPublished)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsAssert(t, "required to no-guidance", noGuidance, ProjectContext{
		ProjectID: "synthetic-project", Status: StatusNoGuidance, CreatedAt: diagnosticsCreated, UpdatedAt: diagnosticsPublished})
	s, _ = diagnosticsSeed(t, StatusReady, []string{"diagnostic-one", "diagnostic-two"})
	if _, err := s.MarkNoGuidance(ctx, "synthetic-project", diagnosticsLater); !errors.Is(err, ErrConflict) {
		t.Fatalf("READY to no-guidance conflict: %v", err)
	}
	diagnosticsAssert(t, "conflict preserves metadata", diagnosticsGet(t, s), diagnosticsOracle(StatusReady))
}

func TestDiagnosticsConcurrentReturnOwnership(t *testing.T) {
	ctx := context.Background()
	s, publication := diagnosticsSeed(t, StatusNoGuidance, []string{"diagnostic-one", "diagnostic-two"})
	ensured, err := s.EnsureRequired(ctx, "synthetic-project", diagnosticsLater)
	if err != nil {
		t.Fatal(err)
	}
	noGuidance, err := s.MarkNoGuidance(ctx, "synthetic-project", diagnosticsLater)
	if err != nil {
		t.Fatal(err)
	}
	previous := diagnosticsGet(t, s)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, snapshot := range []ProjectContext{publication, ensured, noGuidance, previous} {
		wg.Add(1)
		go func(owned ProjectContext) {
			defer wg.Done()
			<-start
			for n := 0; n < 200; n++ {
				owned.Diagnostics[0] = "caller-owned-change"
				owned.Diagnostics[1] = "caller-owned-change-two"
			}
		}(snapshot)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 200; n++ {
			got, err := s.Get(ctx, "synthetic-project")
			if err != nil {
				t.Error(err)
				return
			}
			diagnosticsAssert(t, "concurrent caller mutations leave store", got, diagnosticsOracle(StatusNoGuidance))
		}
	}()
	close(start)
	wg.Wait()
	diagnosticsAssert(t, "final concurrent state", diagnosticsGet(t, s), diagnosticsOracle(StatusNoGuidance))
}
