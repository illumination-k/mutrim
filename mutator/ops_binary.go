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
	}}
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
