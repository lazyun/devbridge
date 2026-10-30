// Package runtimeaudit inventories production runtime writers and call sites.
// The same AST rows are used by the architecture tests and evidence ledger.
package runtimeaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Kind   string `json:"kind"`
	File   string `json:"file"`
	Symbol string `json:"symbol"`
	Line   int    `json:"line"`
	Callee string `json:"callee"`
}

var watchedCalls = map[string]string{
	"PrepareAdmittedExecution":     "CanonicalPreparationRun",
	"ClaimWorkspacePreparation":    "CanonicalWorkspaceClaim",
	"RecordWorkspaceObservations":  "CanonicalWorkspaceObservation",
	"admitExecutionTx":             "ExecutionAdmission",
	"AdmitExecution":               "ExecutionAdmission",
	"authorizeAndCreateRunTx":      "LegacyExecutionRun",
	"ConsumeAndCreateRun":          "LegacyExecutionRun",
	"AuthorizeAndCreateRun":        "LegacyExecutionRun",
	"AuthorizeAndStart":            "LegacyExecutionStart",
	"ResumeReserved":               "LegacyExecutionStart",
	"resumeReserved":               "LegacyExecutionStart",
	"ResumeReservedForProject":     "LegacyExecutionStart",
	"CreatePreparationRun":         "CanonicalPreparationRun",
	"continueAdmittedExecution":    "CanonicalPreparationRun",
	"BindPreparedRuntimeWorkset":   "CanonicalRuntimeWorkset",
	"BindRuntimeWorkset":           "LegacyRuntimeWorkset",
	"StartAuthorizedTurn":          "CanonicalDispatch",
	"StartRecoveryStatusTurn":      "LegacyRecoveryDispatch",
	"StartRecoveryStatusChildTurn": "RecoveryChildDispatch",
	"CreateRun":                    "PotentialRunCreation",
	"ClaimRecoveryChildThread":     "RecoveryChildThread",
	"RecordRecoveryChildThread":    "RecoveryChildThread",
	"ClaimRecoveryChildTurn":       "RecoveryChildTurn",
	"RecordRecoveryChildTurn":      "RecoveryChildTurn",
	"BeforeStart":                  "LegacyRecoveryDispatch",
	"BindTurn":                     "LegacyRecoveryDispatch",
	"StartTurn":                    "RuntimeDispatch",
}

// Scan excludes tests and migration DDL. SQL is counted only when it is an
// argument to an actual SQL method call, so comments and log text cannot be
// mistaken for writers.
func Scan(internalRoot string) ([]Entry, error) {
	set := token.NewFileSet()
	var entries []Entry
	err := filepath.WalkDir(internalRoot, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(item.Name(), "migration") {
			return nil
		}
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(filepath.Dir(internalRoot), path)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			symbol := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				symbol = receiverName(fn.Recv.List[0].Type) + "." + symbol
			}
			if kind, watched := watchedCalls[fn.Name.Name]; watched {
				entries = append(entries, Entry{Kind: kind + "Entry", File: rel, Symbol: symbol, Line: set.Position(fn.Pos()).Line, Callee: fn.Name.Name})
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := callName(call.Fun)
				if kind, watched := watchedCalls[callee]; watched {
					entries = append(entries, Entry{Kind: kind, File: rel, Symbol: symbol, Line: set.Position(call.Pos()).Line, Callee: callee})
				}
				if !sqlMethod(callee) {
					return true
				}
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					for _, writer := range sqlWriters(value) {
						entries = append(entries, Entry{Kind: writer, File: rel, Symbol: symbol, Line: set.Position(literal.Pos()).Line, Callee: callee})
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Kind < b.Kind
	})
	return entries, nil
}

func receiverName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	default:
		return "unknown-receiver"
	}
}

func callName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func sqlMethod(name string) bool {
	switch name {
	case "Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext", "Prepare", "PrepareContext":
		return true
	default:
		return false
	}
}

func sqlWriters(query string) []string {
	upper := strings.ToUpper(strings.Join(strings.Fields(query), " "))
	var out []string
	for _, item := range []struct{ needle, kind string }{
		{"INSERT INTO RUNS", "RunWriter"},
		{"INSERT INTO RUNTIME_SCOPE_PLANS", "ScopePlanWriter"},
		{"INSERT INTO RUNTIME_PREPARATION_AUTHORITIES", "PreparationAuthorityWriter"},
		{"INSERT INTO RUNTIME_WORKSPACE_OBSERVATIONS", "WorkspaceObservationWriter"},
		{"INSERT INTO RUNTIME_WORKSET_BINDINGS", "WorksetBindingWriter"},
		{"INSERT INTO RUNTIME_START_AUTHORITIES", "StartAuthorityWriter"},
		{"INSERT INTO RUN_REPOSITORY_BINDINGS", "RepositoryBindingWriter"},
	} {
		if strings.Contains(upper, item.needle) {
			out = append(out, item.kind)
		}
	}
	if strings.Contains(upper, "RUNTIME_WORKSET_JSON") && (strings.Contains(upper, "UPDATE RUNS SET") || strings.Contains(upper, "INSERT INTO RUNS")) {
		out = append(out, "RuntimeWorksetWriter")
	}
	return out
}
