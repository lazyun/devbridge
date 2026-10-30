// Package skeletonparity compares production declarations with the public
// Task.phase vocabulary and, when present, the optional local Skeleton contract.
// It never executes Skeleton code or production workflows.
package skeletonparity

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestProductionTaskPhaseContractParity(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate project root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "internal", "skeletonparity", "testdata", "task-phases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTaskPhaseParity(data, filepath.Join(root, "internal", "task")); err != nil {
		t.Fatalf("production Task.phase / public vocabulary parity: %v", err)
	}

	t.Run("LocalSkeleton", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, "skeleton", "registry.json"))
		if os.IsNotExist(err) {
			t.Skip("optional local skeleton/registry.json is not included in the public repository")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := validateTaskPhaseParity(data, filepath.Join(root, "internal", "task")); err != nil {
			t.Fatalf("production Task.phase / local Skeleton parity: %v", err)
		}
	})
}

type contractState struct {
	ID                    string `json:"id"`
	Flow                  string `json:"flow"`
	PersistenceMode       string `json:"persistenceMode"`
	PersistedPhaseLiteral string `json:"persistedPhaseLiteral"`
}

func validateTaskPhaseParity(data []byte, taskDir string) error {
	var registry struct {
		States []contractState `json:"states"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return fmt.Errorf("invalid Skeleton registry: %w", err)
	}
	byLiteral := map[string]string{}
	ids := map[string]bool{}
	for _, state := range registry.States {
		if state.Flow != "TASK" || state.PersistenceMode != "PERSISTED_PHASE" {
			continue
		}
		if state.ID == "" || state.PersistedPhaseLiteral == "" {
			return fmt.Errorf("PARITY_EMPTY_STATE: persisted Task phase requires an ID and literal")
		}
		if ids[state.ID] {
			return fmt.Errorf("PARITY_DUPLICATE_STATE: contract repeats %s", state.ID)
		}
		if previous, exists := byLiteral[state.PersistedPhaseLiteral]; exists {
			return fmt.Errorf("PARITY_DUPLICATE_STATE: %s and %s share phase %q", previous, state.ID, state.PersistedPhaseLiteral)
		}
		ids[state.ID] = true
		byLiteral[state.PersistedPhaseLiteral] = state.ID
	}
	if len(byLiteral) == 0 {
		return fmt.Errorf("PARITY_SCOPE_EMPTY: Task persisted-phase contract is empty")
	}
	observed, err := productionTaskPhases(taskDir)
	if err != nil {
		return fmt.Errorf("PARITY_PRODUCTION_SOURCE_INVALID: %w", err)
	}
	seen := map[string]bool{}
	var problems []string
	for _, literal := range observed {
		if seen[literal] {
			problems = append(problems, fmt.Sprintf("PARITY_DUPLICATE_STATE: production repeats phase %q", literal))
		}
		seen[literal] = true
		if _, exists := byLiteral[literal]; !exists {
			problems = append(problems, fmt.Sprintf("PARITY_EXTRA_STATE: undeclared production phase %q", literal))
		}
	}
	for literal, id := range byLiteral {
		if !seen[literal] {
			problems = append(problems, fmt.Sprintf("PARITY_MISSING_STATE: production is missing %s (%q)", id, literal))
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// Resolve constant types so conversions, aliases and inherited expressions
// cannot hide extra or duplicate Phase values. Only source declarations are
// read; no production package is imported or executed.
func productionTaskPhases(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var constants []ast.Decl
	typeSpecs := map[string][]*ast.TypeSpec{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch group.Tok {
			case token.CONST:
				constants = append(constants, group)
			case token.TYPE:
				for _, raw := range group.Specs {
					spec := raw.(*ast.TypeSpec)
					typeSpecs[spec.Name.Name] = append(typeSpecs[spec.Name.Name], spec)
				}
			}
		}
	}
	// Include the constant type dependency closure, without function bodies,
	// imports or unrelated structs. Unresolvable declarations fail closed.
	declarations := append([]ast.Decl(nil), constants...)
	needed := map[string]bool{"Phase": true}
	collectTypes := func(node ast.Node) {
		ast.Inspect(node, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && len(typeSpecs[id.Name]) > 0 {
				needed[id.Name] = true
			}
			return true
		})
	}
	for _, decl := range constants {
		collectTypes(decl)
	}
	added := map[string]bool{}
	for {
		progress := false
		for name := range needed {
			if added[name] {
				continue
			}
			added[name], progress = true, true
			for _, spec := range typeSpecs[name] {
				declarations = append(declarations, &ast.GenDecl{Tok: token.TYPE, Specs: []ast.Spec{spec}})
				collectTypes(spec.Type)
			}
		}
		if !progress {
			break
		}
	}
	file := &ast.File{Name: ast.NewIdent("task"), Decls: declarations}
	conf := types.Config{IgnoreFuncBodies: true}
	pkg, err := conf.Check("production/task/constants", fset, []*ast.File{file}, nil)
	if err != nil {
		return nil, err
	}
	phase, ok := pkg.Scope().Lookup("Phase").(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("Phase type is missing")
	}
	basic, ok := phase.Type().Underlying().(*types.Basic)
	if !ok || basic.Kind() != types.String || phase.IsAlias() {
		return nil, fmt.Errorf("Phase must remain a defined string type")
	}
	var observed []string
	for _, name := range pkg.Scope().Names() {
		value, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok || !types.Identical(value.Type(), phase.Type()) {
			continue
		}
		if value.Val().Kind() != constant.String {
			return nil, fmt.Errorf("Phase constant %s has no string value", name)
		}
		observed = append(observed, constant.StringVal(value.Val()))
	}
	return observed, nil
}

func TestTaskPhaseParityRejectsDrift(t *testing.T) {
	const registry = `{"states":[{"id":"TASK_A","flow":"TASK","persistenceMode":"PERSISTED_PHASE","persistedPhaseLiteral":"A"},{"id":"TASK_B","flow":"TASK","persistenceMode":"PERSISTED_PHASE","persistedPhaseLiteral":"B"}]}`
	for _, tc := range []struct {
		name, source, contract, code string
	}{
		{name: "baseline", source: `A Phase = "A"; B Phase = "B"`},
		{name: "missing", source: `A Phase = "A"`, code: "PARITY_MISSING_STATE"},
		{name: "extra", source: `A Phase = "A"; B Phase = "B"; C Phase = "C"`, code: "PARITY_EXTRA_STATE"},
		{name: "duplicate", source: `A Phase = "A"; B Phase = "B"; Alias Phase = "A"`, code: "PARITY_DUPLICATE_STATE"},
		{name: "inferred alias", source: `A Phase = "A"; B Phase = "B"; Alias = A`, code: "PARITY_DUPLICATE_STATE"},
		{name: "inherited value", source: `A Phase = "A"; B Phase = "B"; Inherited`, code: "PARITY_DUPLICATE_STATE"},
		{name: "conversion", source: `A = Phase("A"); B Phase = "B"`},
		{name: "named alias conversion", source: `A Phase = "A"; B Phase = "B"; C = PhaseAlias("C")`, code: "PARITY_EXTRA_STATE"},
		{name: "constant expression", source: `A Phase = "A"; B Phase = "B"; C = A + "C"`, code: "PARITY_EXTRA_STATE"},
		{name: "unresolvable", source: `A Phase = "A"; B Phase = "B"; C = unresolved`, code: "PARITY_PRODUCTION_SOURCE_INVALID"},
		{name: "empty registry", contract: `{"states":[]}`, code: "PARITY_SCOPE_EMPTY"},
		{name: "missing ID", contract: strings.Replace(registry, `"TASK_A"`, `""`, 1), code: "PARITY_EMPTY_STATE"},
		{name: "missing literal", contract: strings.Replace(registry, `"persistedPhaseLiteral":"A"`, `"persistedPhaseLiteral":""`, 1), code: "PARITY_EMPTY_STATE"},
		{name: "duplicate ID", contract: strings.Replace(registry, `"TASK_B"`, `"TASK_A"`, 1), code: "PARITY_DUPLICATE_STATE"},
		{name: "duplicate literal", contract: strings.Replace(registry, `"persistedPhaseLiteral":"B"`, `"persistedPhaseLiteral":"A"`, 1), code: "PARITY_DUPLICATE_STATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := "package task\ntype Phase string\ntype PhaseAlias = Phase\nconst (\n" + tc.source + "\n)\n"
			if err := os.WriteFile(filepath.Join(dir, "phase.go"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			data := tc.contract
			if data == "" {
				data = registry
			}
			err := validateTaskPhaseParity([]byte(data), dir)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}
