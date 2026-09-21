package mutator

import (
	"go/ast"
	"go/token"
)

// Negation negates the condition of if and for statements.
type Negation struct{}

func (Negation) Name() string { return "negation" }

func (Negation) Sites(ctx *Context, n ast.Node) []Site {
	var cond *ast.Expr
	switch s := n.(type) {
	case *ast.IfStmt:
		cond = &s.Cond
	case *ast.ForStmt:
		if s.Cond == nil {
			return nil
		}
		cond = &s.Cond
	default:
		return nil
	}
	orig := *cond
	negated := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: orig}}
	s := Site{
		Node:        orig,
		Description: "cond -> !(cond)",
		Apply:       func() { *cond = negated },
		Undo:        func() { *cond = orig },
	}
	if ctx.isBool(orig) {
		// *cond is read at lowering time so that an inner rewrite of the
		// condition (a relational site on the same node) is wrapped.
		s.Schemata = func(l *Lowering) ast.Node { return l.Call("Not", *cond) }
	}
	return []Site{s}
}

// IncDec swaps ++ and --.
type IncDec struct{}

func (IncDec) Name() string { return "incdec" }

func (IncDec) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.IncDecStmt)
	if !ok {
		return nil
	}
	from := s.Tok
	to := token.DEC
	if from == token.DEC {
		to = token.INC
	}
	return []Site{{
		Node:        s,
		Description: from.String() + " -> " + to.String(),
		Apply:       func() { s.Tok = to },
		Undo:        func() { s.Tok = from },
		Schemata:    incDecSchemata(ctx, s, to),
	}}
}

// incDecSchemata lowers `x++` to `mut.Inc(id, &x)`, which is valid wherever
// the statement was, including a for post statement. A map element is not
// addressable, so it becomes `if mut.Active(id) { m[k]-- } else { m[k]++ }`
// instead, which a for post statement cannot hold; that site is declined.
func incDecSchemata(ctx *Context, s *ast.IncDecStmt, to token.Token) func(*Lowering) ast.Node {
	fn := "Inc"
	if s.Tok == token.DEC {
		fn = "Dec"
	}
	if !ctx.isMapIndex(s.X) {
		return func(l *Lowering) ast.Node {
			var addr ast.Expr = &ast.UnaryExpr{Op: token.AND, X: s.X}
			if star, ok := ast.Unparen(s.X).(*ast.StarExpr); ok {
				addr = star.X // &*p is p
			}
			return &ast.ExprStmt{X: l.Call(fn, addr)}
		}
	}
	return func(l *Lowering) ast.Node {
		if l.Cursor.Name() == "Post" {
			return nil
		}
		return &ast.IfStmt{
			Cond: l.Active(),
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.IncDecStmt{X: s.X, Tok: to}}},
			Else: &ast.BlockStmt{List: []ast.Stmt{s}},
		}
	}
}
