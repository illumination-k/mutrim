package mutator

import (
	"go/ast"
	"go/token"
)

// BinaryOp swaps the operator of a binary expression according to Table.
type BinaryOp struct {
	OpName string
	Table  map[token.Token]token.Token
}

func (b BinaryOp) Name() string { return b.OpName }

func (b BinaryOp) Sites(ctx *Context, n ast.Node) []Site {
	e, ok := n.(*ast.BinaryExpr)
	if !ok {
		return nil
	}
	to, ok := b.Table[e.Op]
	if !ok {
		return nil
	}
	from := e.Op
	return []Site{{
		Node:        e,
		Pos:         e.OpPos,
		Description: from.String() + " -> " + to.String(),
		Apply:       func() { e.Op = to },
		Undo:        func() { e.Op = from },
		Check:       func() error { return ctx.CheckExpr(e) },
		Schemata:    binarySchemata(ctx, e, to),
	}}
}

// binarySchemata picks the runtime helper for a binary-operator swap.
// Constant expressions are left alone (a call is not a constant), as are
// boolean results of a defined type, which the helpers' plain bool is not
// assignable to. Operands need no check: go/types records an untyped
// constant operand with the type it converted to, so the generic helper
// infers the expression's type from either operand.
//
//   - == <-> !=: mut.Not(id, a == b)
//   - < <= > >=: mut.Cmp(id, a, b, "<", "<=")
//   - + - * / %: mut.Arith(id, a, b, "+", "-"), ArithInt when % is involved
//   - && ||: mut.And(id, a, func() bool { return b }), likewise Or
func binarySchemata(ctx *Context, e *ast.BinaryExpr, to token.Token) func(*Lowering) ast.Node {
	if ctx.isConst(e) || opClass(e.Op) != opClass(to) {
		return nil
	}
	switch opClass(e.Op) {
	case classEquality:
		if !ctx.isBool(e) {
			return nil
		}
		return func(l *Lowering) ast.Node { return l.Call("Not", e) }
	case classOrdered:
		if !ctx.isBool(e) {
			return nil
		}
		return func(l *Lowering) ast.Node { return l.Call("Cmp", e.X, e.Y, opLit(e.Op), opLit(to)) }
	case classArith:
		fn := "Arith"
		if e.Op == token.REM || to == token.REM {
			fn = "ArithInt"
		}
		return func(l *Lowering) ast.Node { return l.Call(fn, e.X, e.Y, opLit(e.Op), opLit(to)) }
	case classLogical:
		if !ctx.isBool(e) || !ctx.isBool(e.X) || !ctx.isBool(e.Y) || ctx.callsRecover(e.Y) {
			return nil
		}
		fn := "And"
		if e.Op == token.LOR {
			fn = "Or"
		}
		return func(l *Lowering) ast.Node { return l.Call(fn, e.X, boolClosure(e.Y)) }
	}
	return nil
}

type opClassKind int

const (
	classNone opClassKind = iota
	classEquality
	classOrdered
	classArith
	classLogical
)

func opClass(t token.Token) opClassKind {
	switch t {
	case token.EQL, token.NEQ:
		return classEquality
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return classOrdered
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		return classArith
	case token.LAND, token.LOR:
		return classLogical
	}
	return classNone
}

// Relational flips comparison operators: boundary for ordered comparisons,
// negation for equality.
var Relational = BinaryOp{OpName: "relational", Table: map[token.Token]token.Token{
	token.LSS: token.LEQ,
	token.LEQ: token.LSS,
	token.GTR: token.GEQ,
	token.GEQ: token.GTR,
	token.EQL: token.NEQ,
	token.NEQ: token.EQL,
}}

// Arithmetic swaps + - * / %.
var Arithmetic = BinaryOp{OpName: "arithmetic", Table: map[token.Token]token.Token{
	token.ADD: token.SUB,
	token.SUB: token.ADD,
	token.MUL: token.QUO,
	token.QUO: token.MUL,
	token.REM: token.MUL,
}}

// Logical swaps && and ||.
var Logical = BinaryOp{OpName: "logical", Table: map[token.Token]token.Token{
	token.LAND: token.LOR,
	token.LOR:  token.LAND,
}}
