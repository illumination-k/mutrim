package mutator

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

// Rules a site is reported as in Mutant.Equivalent. Each is a static
// proof that the mutant computes what the original does, so no test can
// kill it (the EMS rules of Kushigian et al., ISSTA 2024).
const (
	// equivIdentityOperand: both operators are the identity on the left
	// operand for the constant right one (`x * 1` -> `x / 1`, `x + 0` ->
	// `x - 0` on integers, `x << 0` -> `x >> 0`).
	equivIdentityOperand = "identity-operand"
	// equivNonNegative: one operand is never negative (len, cap or an
	// unsigned value) and the other a constant, and both comparisons
	// agree on every value the operand can take (`len(s) > -1` -> `len(s)
	// >= -1`).
	equivNonNegative = "non-negative-operand"
)

// rightIdentity is the right operand that makes each operator the
// identity on its left operand.
var rightIdentity = map[token.Token]int64{
	token.ADD: 0,
	token.SUB: 0,
	token.MUL: 1,
	token.QUO: 1,
	token.SHL: 0,
	token.SHR: 0,
}

// equivalentSwap returns the rule proving that `x from y` and `x to y`
// are equal for every x, or "" when no rule does.
func (c *Context) equivalentSwap(from, to token.Token, x, y ast.Expr) string {
	if c.identityOperand(from, to, x, y) {
		return equivIdentityOperand
	}
	if c.nonNegativeCompare(from, to, x, y) {
		return equivNonNegative
	}
	return ""
}

func (c *Context) identityOperand(from, to token.Token, x, y ast.Expr) bool {
	idFrom, okFrom := rightIdentity[from]
	idTo, okTo := rightIdentity[to]
	if !okFrom || !okTo || idFrom != idTo || !c.constEquals(y, idFrom) {
		return false
	}
	b := c.basic(x)
	switch {
	case b == nil:
		return false
	case b.Info()&types.IsInteger != 0:
		return true
	case b.Info()&types.IsFloat != 0:
		// x*1 and x/1 are x exactly, but x+0 and x-0 tell -0 apart.
		return idFrom == 1
	}
	return false
}

// constEquals reports whether e is an integer or floating-point constant
// equal to v.
func (c *Context) constEquals(e ast.Expr, v int64) bool {
	val := c.Info.Types[e].Value
	if val == nil || (val.Kind() != constant.Int && val.Kind() != constant.Float) {
		return false
	}
	return constant.Compare(val, token.EQL, constant.MakeInt64(v))
}

// nonNegativeCompare reports whether swapping the comparison from for to
// changes nothing because one operand is never negative and the other is
// a constant. Such a comparison only depends on whether the operand is
// below, at or above the constant, so it is enough that both operators
// agree in each of these cases the operand can reach.
func (c *Context) nonNegativeCompare(from, to token.Token, x, y ast.Expr) bool {
	if opClass(from) != classOrdered || opClass(to) != classOrdered {
		return false
	}
	operand, bound := x, y
	if c.isConst(x) {
		// `c < len(s)` is `len(s) > c`.
		operand, bound = y, x
		from, to = mirrored[from], mirrored[to]
	}
	if !c.nonNegative(operand) {
		return false
	}
	val := c.Info.Types[bound].Value
	if val == nil || (val.Kind() != constant.Int && val.Kind() != constant.Float) {
		return false
	}
	zero := constant.MakeInt64(0)
	for _, cmp := range []token.Token{token.LSS, token.EQL, token.GTR} {
		// operand below the bound needs bound > 0, at it bound >= 0; above
		// it is always reachable.
		reachable := cmp == token.GTR ||
			(cmp == token.EQL && constant.Compare(val, token.GEQ, zero)) ||
			(cmp == token.LSS && constant.Compare(val, token.GTR, zero))
		if reachable && holds(from, cmp) != holds(to, cmp) {
			return false
		}
	}
	return true
}

// mirrored is the comparison with its operands swapped.
var mirrored = map[token.Token]token.Token{
	token.LSS: token.GTR,
	token.LEQ: token.GEQ,
	token.GTR: token.LSS,
	token.GEQ: token.LEQ,
}

// holds reports whether `a op b` is true when a relates to b as rel (LSS,
// EQL or GTR).
func holds(op, rel token.Token) bool {
	switch op {
	case token.LSS:
		return rel == token.LSS
	case token.LEQ:
		return rel != token.GTR
	case token.GTR:
		return rel == token.GTR
	case token.GEQ:
		return rel != token.LSS
	}
	return false
}

// nonNegative reports whether e can never be negative: a call of len or
// cap, or a value of an unsigned type.
func (c *Context) nonNegative(e ast.Expr) bool {
	if call, ok := ast.Unparen(e).(*ast.CallExpr); ok && (c.isBuiltin(call, "len") || c.isBuiltin(call, "cap")) {
		return true
	}
	b := c.basic(e)
	return b != nil && b.Info()&types.IsUnsigned != 0
}

// basic is the underlying basic type of e, or nil.
func (c *Context) basic(e ast.Expr) *types.Basic {
	t := c.Info.TypeOf(e)
	if t == nil {
		return nil
	}
	b, _ := t.Underlying().(*types.Basic)
	return b
}
