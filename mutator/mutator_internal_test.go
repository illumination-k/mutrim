package mutator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// Files with a cgo import and files that go/packages lists in Syntax but
// not in GoFiles (cgo-processed sources) are excluded, plain files are not.
func TestExcluded(t *testing.T) {
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	plain := parse("/pkg/plain.go", "package p\n")
	cgo := parse("/pkg/cgo.go", "package p\n\nimport \"C\"\n")
	foreign := parse("/tmp/cgo-processed.go", "package p\n")
	pkg := &packages.Package{Fset: fset, GoFiles: []string{"/pkg/plain.go", "/pkg/cgo.go"}}

	for f, want := range map[*ast.File]bool{plain: false, cgo: true, foreign: true} {
		if got := excluded(pkg, f); got != want {
			t.Errorf("excluded(%s) = %v, want %v", fset.Position(f.Package).Filename, got, want)
		}
	}
}

// An expression the type checker never saw is neither a bool nor a typed
// operand, so no operator builds a schemata call around it.
func TestContextWithoutTypeInfo(t *testing.T) {
	ctx := &Context{Info: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}}
	e := ast.NewIdent("unknown")
	if ctx.isBool(e) || ctx.isTypedOperand(e) {
		t.Error("an untyped expression must not be a bool or a typed operand")
	}
	if signatureOf(&types.Info{Defs: map[*ast.Ident]types.Object{}}, e) != nil {
		t.Error("an undefined identifier has no signature")
	}
}
