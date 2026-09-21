package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Return replaces every result of a return statement with its zero value.
// Statements that already return only zero values, bare returns, and result
// types without a spellable zero value (structs, arrays, type parameters)
// are skipped. The rewrite is well-typed by construction; if it leaves an
// import or variable unused, the build fails and the runner counts the
// mutant as not viable.
type Return struct{}

func (Return) Name() string { return "return" }

func (Return) Sites(ctx *Context, n ast.Node) []Site {
	ret, ok := n.(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 || ctx.Sig == nil {
		return nil
	}
	results := ctx.Sig.Results()
	if results.Len() != len(ret.Results) {
		return nil // return f() with a multi-value f
	}

	zeros := make([]ast.Expr, len(ret.Results))
	allZero := true
	for i, r := range ret.Results {
		z, ok := zeroExpr(results.At(i).Type())
		if !ok {
			return nil
		}
		zeros[i] = z
		allZero = allZero && isZeroLiteral(r)
	}
	if allZero {
		return nil
	}

	orig := ret.Results
	return []Site{{
		Node:        ret,
		Description: "return -> zero values",
		Apply:       func() { ret.Results = zeros },
		Undo:        func() { ret.Results = orig },
	}}
}

// zeroExpr spells the zero value of t, if it can be written without naming t.
func zeroExpr(t types.Type) (ast.Expr, bool) {
	t = types.Unalias(t)
	if _, ok := t.(*types.TypeParam); ok {
		return nil, false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsBoolean != 0:
			return ast.NewIdent("false"), true
		case u.Info()&types.IsString != 0:
			return &ast.BasicLit{Kind: token.STRING, Value: `""`}, true
		case u.Info()&types.IsNumeric != 0:
			return &ast.BasicLit{Kind: token.INT, Value: "0"}, true
		case u.Kind() == types.UnsafePointer:
			return ast.NewIdent("nil"), true
		}
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return ast.NewIdent("nil"), true
	}
	return nil, false
}

func isZeroLiteral(e ast.Expr) bool {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return e.Name == "nil" || e.Name == "false"
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT, token.FLOAT:
			return e.Value == "0" || e.Value == "0.0"
		case token.STRING:
			return e.Value == `""` || e.Value == "``"
		}
	}
	return false
}
