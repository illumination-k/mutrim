package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
)

// Return replaces the results of a return statement: each result on its own
// with its zero value and with one other value of its type (`return x, err`
// becomes `return x, nil`, the untested error path), and every result at
// once with its zero value. A result that already has the value is skipped,
// as are bare returns, `return f()` with a multi-value f, and result types
// without a spellable zero value (structs, arrays, type parameters).
//
// The rewrite is well-typed by construction; if it leaves an import or
// variable unused, the build fails and the runner counts the mutant as not
// viable. (Schemata keep the original return, so nothing becomes unused
// there.)
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
	for i := range ret.Results {
		z, ok := zeroExpr(results.At(i).Type())
		if !ok {
			return nil
		}
		zeros[i] = z
		allZero = allZero && isZeroLiteral(ret.Results[i])
	}

	var sites []Site
	for i := range ret.Results {
		for _, v := range returnValues(results.At(i).Type()) {
			if types.ExprString(ret.Results[i]) == types.ExprString(v) {
				continue // the result already has that value
			}
			mutated := slices.Clone(ret.Results)
			mutated[i] = v
			sites = append(sites, returnSite(ret, mutated,
				"result "+strconv.Itoa(i+1)+" -> "+types.ExprString(v)))
		}
	}
	// With a single result the all-zero rewrite is the per-result one.
	if len(ret.Results) > 1 && !allZero {
		sites = append(sites, returnSite(ret, zeros, "return -> zero values"))
	}
	return sites
}

// returnSite replaces the results of ret with mutated.
func returnSite(ret *ast.ReturnStmt, mutated []ast.Expr, description string) Site {
	orig := ret.Results
	return Site{
		Node:        ret,
		Description: description,
		Apply:       func() { ret.Results = mutated },
		Undo:        func() { ret.Results = orig },
		Wraps:       true,
		// `{ if mut.Active(id) { return <mutated> }; return x }`: a block is
		// valid wherever a return is and stays a terminating statement. The
		// other mutants of the same return nest inside it.
		Schemata: func(l *Lowering) ast.Node {
			return &ast.BlockStmt{List: []ast.Stmt{
				&ast.IfStmt{
					Cond: l.Active(),
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: mutated}}},
				},
				l.Stmt(),
			}}
		},
	}
}

// returnValues lists the values a result of type t is replaced with: its
// zero value, and one other value of the type. The extras are untyped
// constants, so they convert to a defined type as the zero value does.
func returnValues(t types.Type) []ast.Expr {
	zero, ok := zeroExpr(t)
	if !ok {
		return nil
	}
	values := []ast.Expr{zero}
	if u, ok := t.Underlying().(*types.Basic); ok {
		switch {
		case u.Info()&types.IsBoolean != 0:
			values = append(values, ast.NewIdent("true"))
		case u.Info()&types.IsString != 0:
			values = append(values, &ast.BasicLit{Kind: token.STRING, Value: filler})
		case u.Info()&types.IsNumeric != 0:
			values = append(values, &ast.BasicLit{Kind: token.INT, Value: "1"})
		}
	}
	return values
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
