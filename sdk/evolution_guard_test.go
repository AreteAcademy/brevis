package sdk_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// evolveHome is the one file allowed to compare an Evolution with ==.
//
// The predicates have to be written somewhere, and this is where they live.
const evolveHome = "internal/core/evolve.go"

// An Evolution is never compared with == or != outside evolve.go.
//
// This test is the whole reason the predicates exist, and it is worth saying
// why plainly: with two values in the enum, `e.MayAdd()` and
// `e == EvolveAdditive` are the same expression. Replacing one with the other
// changes no behaviour, so a green suite proves NOTHING about that refactor.
// This is the only thing that can fail.
//
// What it protects against is the day a third value is added. `== EvolveAdditive`
// answers "no" for it, silently, in six places — and four of those would make
// the new mode LESS capable than EvolveAdditive, not more. One of them,
// load/table.go, would return early from evolveTable and ship the feature
// inert on BigQuery.
//
// That is not hypothetical here. core/metadata.go records the SDK paying for
// it once already:
//
//	the feature would have been inert the day it shipped. It was found by
//	loading against a real Postgres, not by reading the code.
//
// Tests are exempt: asserting that a value IS a particular mode is what a test
// is for. This is about the code that decides.
func TestAnEvolutionIsNeverComparedWithEquality(t *testing.T) {
	var found []string
	var visited int

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// path, never d.Name(): the root's name IS ".", so the hidden-dir
			// rule written on the name skips the whole walk on its first call
			// and the test passes by visiting nothing. It did exactly that,
			// which is the failure this test exists to catch, in this test.
			if path == "." {
				return nil
			}
			if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.ToSlash(path) == evolveHome {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || (be.Op != token.EQL && be.Op != token.NEQ) {
				return true
			}
			if !namesAnEvolution(be.X) && !namesAnEvolution(be.Y) {
				return true
			}
			found = append(found, fmt.Sprintf("%s: %s",
				fset.Position(be.Pos()), be.Op))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A walk that visited nothing passes, and would pass forever.
	if visited < 20 {
		t.Fatalf("the walk visited %d files, which is not this module. The "+
			"test is not looking at the code it claims to check", visited)
	}

	if len(found) > 0 {
		t.Errorf("an Evolution is compared with equality in %d place(s):\n  %s\n\n"+
			"Use the predicate instead — e.MayAdd(), e.FromPayload(). A comparison "+
			"answers \"no\" for every value added to the enum after the one it names, "+
			"and it does it silently: the mode is not refused, it simply does nothing. "+
			"If a comparison here is genuinely right, it belongs in %s beside the "+
			"predicates.",
			len(found), strings.Join(found, "\n  "), evolveHome)
	}
}

// namesAnEvolution reports whether the expression mentions the Evolve field or
// one of the Evolve* constants.
//
// Syntactic, and deliberately: loading type information for every package in
// the module to answer one question would make this slow enough to be
// disabled. The names are the convention and the convention is what a reader
// follows.
func namesAnEvolution(e ast.Expr) bool {
	var yes bool
	ast.Inspect(e, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.Ident:
			if strings.HasPrefix(t.Name, "Evolve") {
				yes = true
			}
		case *ast.SelectorExpr:
			if t.Sel.Name == "Evolve" || strings.HasPrefix(t.Sel.Name, "Evolve") {
				yes = true
			}
		}
		return !yes
	})
	return yes
}
