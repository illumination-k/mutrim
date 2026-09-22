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
	schemata, wraps := binarySchemata(ctx, e, to)
	return []Site{{
		Node:        e,
		Pos:         e.OpPos,
		End:         e.OpPos + token.Pos(len(from.String())),
		Description: from.String() + " -> " + to.String(),
		Apply:       func() { e.Op = to },
		Undo:        func() { e.Op = from },
		Check:       func() error { return ctx.CheckExpr(e) },
		Schemata:    schemata,
		Wraps:       wraps,
	}}
}

// binarySchemata picks the runtime helper for a binary-operator swap and
// reports whether the result wraps the node's lowering so far (Site.Wraps)
// instead of rebuilding it from the operands. Constant expressions are left
// alone (a call is not a constant), as are boolean results of a defined
// type, which the helpers' plain bool is not assignable to. Operands need no
// check: go/types records an untyped constant operand with the type it
// converted to, so the generic helper infers the expression's type from
// either operand.
//
//   - == <-> !=: mut.Not(id, a == b)
//   - < -> >=, <= -> >: mut.Not(id, a < b), since negating the comparison is
//     the swap (for a NaN operand the two differ; the mutant stands either way)
//   - < -> <=, > -> >=: mut.Cmp(id, a, b, "<", "<=")
//   - + - * / %: mut.Arith(id, a, b, "+", "-"), ArithInt when % is involved
//   - && ||: mut.And(id, a, func() bool { return b }), likewise Or
func binarySchemata(ctx *Context, e *ast.BinaryExpr, to token.Token) (fn func(*Lowering) ast.Node, wraps bool) {
	if ctx.isConst(e) || opClass(e.Op) != opClass(to) {
		return nil, false
	}
	negate := func(l *Lowering) ast.Node { return l.Call("Not", l.Expr()) }
	switch opClass(e.Op) {
	case classEquality:
		if !ctx.isBool(e) {
			return nil, false
		}
		return negate, true
	case classOrdered:
		if !ctx.isBool(e) {
			return nil, false
		}
		if to == negatedOp[e.Op] {
			return negate, true
		}
		return func(l *Lowering) ast.Node { return l.Call("Cmp", e.X, e.Y, opLit(e.Op), opLit(to)) }, false
	case classArith:
		helper := "Arith"
		if e.Op == token.REM || to == token.REM {
			helper = "ArithInt"
		}
		return func(l *Lowering) ast.Node { return l.Call(helper, e.X, e.Y, opLit(e.Op), opLit(to)) }, false
	case classLogical:
		if !ctx.isBool(e) || !ctx.isBool(e.X) || !ctx.isBool(e.Y) || ctx.callsRecover(e.Y) {
			return nil, false
		}
		helper := "And"
		if e.Op == token.LOR {
			helper = "Or"
		}
		return func(l *Lowering) ast.Node { return l.Call(helper, e.X, boolClosure(e.Y)) }, false
	case classBitwise:
		return func(l *Lowering) ast.Node { return l.Call("Bit", e.X, e.Y, opLit(e.Op), opLit(to)) }, false
	case classShift:
		// A constant left operand would give the helper its default type
		// (`1 << n` in an int64 context becomes an int), so it is declined.
		if ctx.isConst(e.X) {
			return nil, false
		}
		return func(l *Lowering) ast.Node { return l.Call("Shift", e.X, e.Y, opLit(e.Op), opLit(to)) }, false
	}
	return nil, false
}

// negatedOp is the comparison that negates each ordered comparison.
var negatedOp = map[token.Token]token.Token{
	token.LSS: token.GEQ,
	token.LEQ: token.GTR,
	token.GTR: token.LEQ,
	token.GEQ: token.LSS,
}

type opClassKind int

const (
	classNone opClassKind = iota
	classEquality
	classOrdered
	classArith
	classLogical
	classBitwise
	classShift
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
	case token.AND, token.OR, token.XOR, token.AND_NOT:
		return classBitwise
	case token.SHL, token.SHR:
		return classShift
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

// Invert negates a comparison: `a < b` becomes `a >= b`, which relational's
// boundary swap does not produce. == and != are already each other's
// negation, so they are relational's. Inside an if or for condition the
// mutant duplicates negation's; both are kept, since collapsing subsumed
// mutants is the minimizer's job, not the mutator's.
var Invert = BinaryOp{OpName: "invert", Table: negatedOp}

// Arithmetic swaps + - * / %.
var Arithmetic = BinaryOp{OpName: "arithmetic", Table: map[token.Token]token.Token{
	token.ADD: token.SUB,
	token.SUB: token.ADD,
	token.MUL: token.QUO,
	token.QUO: token.MUL,
	token.REM: token.MUL,
}}

// Bitwise swaps & and |, turns ^ and &^ into &, and swaps << and >>.
var Bitwise = BinaryOp{OpName: "bitwise", Table: map[token.Token]token.Token{
	token.AND:     token.OR,
	token.OR:      token.AND,
	token.XOR:     token.AND,
	token.AND_NOT: token.AND,
	token.SHL:     token.SHR,
	token.SHR:     token.SHL,
}}

// Logical swaps && and ||.
var Logical = BinaryOp{OpName: "logical", Table: map[token.Token]token.Token{
	token.LAND: token.LOR,
	token.LOR:  token.LAND,
}}
