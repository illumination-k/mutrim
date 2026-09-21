package mutator

import (
	"go/ast"
	"go/token"
)

// Negation negates the condition of if and for statements.
type Negation struct{}

func (Negation) Name() string { return "negation" }

func (Negation) Sites(_ *Context, n ast.Node) []Site {
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
	return []Site{{
		Node:        orig,
		Description: "cond -> !(cond)",
		Apply:       func() { *cond = negated },
		Undo:        func() { *cond = orig },
	}}
}

// IncDec swaps ++ and --.
type IncDec struct{}

func (IncDec) Name() string { return "incdec" }

func (IncDec) Sites(_ *Context, n ast.Node) []Site {
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
	}}
}
