package localmcp_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeaudit"
)

// The shared scanner excludes *_test.go. Parse symbol bodies, then give that
// same scanner a temporary source inventory; do not match a particular filename.
func scalarFixtureViolations(t *testing.T, set *token.FileSet, decls []ast.Decl) ([]string, map[string]bool) {
	t.Helper()
	var out bytes.Buffer
	if err := printer.Fprint(&out, set, &ast.File{Name: ast.NewIdent("fixture"), Decls: decls}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := runtimeaudit.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	violations := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Kind] = true
		switch row.Kind {
		case "CanonicalPreparationRun", "CanonicalDispatch":
		default:
			violations = append(violations, row.Kind)
		}
	}
	for _, decl := range decls {
		ast.Inspect(decl, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok {
					seen[s.Sel.Name] = true
					switch s.Sel.Name {
					case "Update", "Transition", "Exec", "ExecContext", "ClaimWorkspacePreparation", "RecordWorkspaceObservations", "BindPreparedRuntimeWorkset":
						violations = append(violations, s.Sel.Name)
					}
				}
			}
			if a, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range a.Lhs {
					if s, ok := lhs.(*ast.SelectorExpr); ok && s.Sel.Name == "RuntimeEpoch" {
						violations = append(violations, "epoch assignment")
					}
				}
			}
			return true
		})
	}
	return violations, seen
}

func TestLocalMCPScalarFixtureUsesCanonicalPreparation(t *testing.T) {
	set := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{"admitScalarExecution": false, "prepareScalarExecutionForLocalMCP": false, "authorizeScalarExecutionForLocalMCP": false, "completeScalarExecutionForLocalMCP": false, "scalarFixtureFacts": false, "assertScalarLayer": false, "assertScalarRunIdentity": false, "assertScalarActiveRun": false}
	var decls []ast.Decl
	staleTest := false
	for _, path := range files {
		f, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name == "TestLocalMCPScalarStaleEpochRejected" {
				staleTest = true
			}
			if _, ok := names[fn.Name.Name]; ok {
				names[fn.Name.Name] = true
				decls = append(decls, d)
			}
		}
	}
	for name, found := range names {
		if !found {
			t.Fatalf("missing fixture symbol %s", name)
		}
	}
	if !staleTest {
		t.Fatal("missing stale epoch negative regression")
	}
	bad, seen := scalarFixtureViolations(t, set, decls)
	if len(bad) != 0 {
		t.Fatalf("fixture bypasses canonical protocol: %v", bad)
	}
	for _, kind := range []string{"CanonicalPreparationRun", "PrepareReservedExecution", "CanonicalDispatch", "RuntimeEpoch", "Get", "ValidatePreparedStart"} {
		if !seen[kind] {
			t.Fatalf("missing canonical/durable readback %s", kind)
		}
	}
}

func TestLocalMCPScalarFixtureArchitectureRejectsBypasses(t *testing.T) {
	for _, body := range []string{
		`db.Exec("INSERT INTO runs(id) VALUES('x')")`,
		`db.Exec("INSERT INTO runtime_workspace_observations(observation_id) VALUES('x')")`,
		`db.Exec("INSERT INTO runtime_workset_bindings(plan_id) VALUES('x')")`,
		`db.Exec("INSERT INTO runtime_start_authorities(authority_id) VALUES('x')")`,
		`runs.Update(ctx,id,fn)`, `runs.Transition(ctx,id,status)`,
		`r.RuntimeEpoch = epoch`, `s.AuthorizeAndCreateRun(ctx)`, `s.ConsumeAndCreateRun(ctx)`, `s.ResumeReserved(ctx)`,
	} {
		set := token.NewFileSet()
		f, err := parser.ParseFile(set, "injected.go", "package fixture\nfunc injected(){"+body+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		bad, _ := scalarFixtureViolations(t, set, f.Decls)
		if len(bad) == 0 {
			t.Fatalf("undetected bypass: %s", body)
		}
	}
}
