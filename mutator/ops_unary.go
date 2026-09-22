package mutator

import (
	"go/ast"
	"go/token"
)

// Negatives drops a unary minus: -x becomes x. The rewrite spells it as
// +x, which is the identity and valid on every operand -x is valid on.
type Negatives struct{}

func (Negatives) Name() string { return "negatives" }

func (Negatives) Sites(ctx *Context, n ast.Node) []Site {
	e, ok := n.(*ast.UnaryExpr)
	if !ok || e.Op != token.SUB {
		return nil
	}
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
}
