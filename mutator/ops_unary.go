package mutator

import (
	"go/ast"
	"go/token"
)

// Negatives drops a unary operator: -x and !x both become x. The minus is
// rewritten as +x, which is the identity and valid on every operand -x is
// valid on; the negation is rewritten as !!(x), which is x for every
// operand ! is valid on.
type Negatives struct{}

func (Negatives) Name() string { return "negatives" }

func (Negatives) Sites(ctx *Context, n ast.Node) []Site {
	e, ok := n.(*ast.UnaryExpr)
	if !ok {
		return nil
	}
	switch e.Op {
	case token.SUB:
		s := Site{
			Node:        e,
			Description: "-x -> x",
			Apply:       func() { e.Op = token.ADD },
			Undo:        func() { e.Op = token.SUB },
		}
		if !ctx.isConst(e) { // a call is not a constant
			s.Schemata = func(l *Lowering) ast.Node { return l.Call("Neg", e.X) }
		}
		return []Site{s}
	case token.NOT:
		orig := e.X
		doubled := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: orig}}
		s := Site{
			Node:        e,
			Description: "!x -> x",
			Apply:       func() { e.X = doubled },
			Undo:        func() { e.X = orig },
			Wraps:       true,
		}
		if !ctx.isConst(e) && ctx.isBool(e) {
			// mut.Not(id, !x) is !x normally and !!x, that is x, when active.
			s.Schemata = func(l *Lowering) ast.Node { return l.Call("Not", l.Expr()) }
		}
		return []Site{s}
	}
	return nil
}
