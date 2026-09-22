package mutator

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"strconv"
	"strings"
)

// Constant replaces a numeric literal c with c+1. Literals that must stay
// constant are not sites: array lengths, const declarations and the keys
// of array and slice literals. The rewrite is a literal too, so it is
// valid wherever the original was; the schemata form is a call, so it
// is declined where the value must be constant after all (an operand of
// a constant shift) and where the runtime helper could not be given the
// literal's type: a defined type, or a type the context leaves untyped.
type Constant struct{}

func (Constant) Name() string { return "constant" }

func (Constant) Sites(ctx *Context, n ast.Node) []Site {
	lit, ok := n.(*ast.BasicLit)
	if !ok || (lit.Kind != token.INT && lit.Kind != token.FLOAT) || ctx.mustBeConstant(lit) {
		return nil
	}
	mutated, ok := plusOne(lit)
	if !ok {
		return nil
	}
	orig := lit.Value
	s := Site{
		Node:        lit,
		Description: orig + " -> " + mutated,
		Apply:       func() { lit.Value = mutated },
		Undo:        func() { lit.Value = orig },
	}
	typeName, ok := ctx.basicTypeName(lit)
	if !ok {
		return []Site{s} // the build decides: overflow is a compile error
	}
	replacement := &ast.BasicLit{Kind: lit.Kind, Value: mutated}
	conv := func(x ast.Expr) ast.Expr {
		return &ast.CallExpr{Fun: ast.NewIdent(typeName), Args: []ast.Expr{x}}
	}
	// `T(c+1)` fails to type-check when c+1 overflows T.
	s.Check = func() error { return types.CheckExpr(ctx.Fset, ctx.Pkg, lit.Pos(), conv(replacement), &types.Info{}) }
	if shift, ok := ctx.parent(1).(*ast.BinaryExpr); ok && opClass(shift.Op) == classShift && ctx.isConst(shift) {
		return []Site{s}
	}
	s.Schemata = func(l *Lowering) ast.Node { return l.Call("Const", conv(lit), replacement) }
	return []Site{s}
}

// mustBeConstant reports whether lit sits where Go requires a literal or a
// constant: a constant declaration, an array length, the key of an array or
// slice literal, or a struct tag. The enclosing expressions are walked, not
// only the parent, since a constant context extends through them
// (`[2*3]int`, `[len("ab")]int`).
func (c *Context) mustBeConstant(lit *ast.BasicLit) bool {
	if c.inConstDecl() {
		return true
	}
	child := ast.Node(lit)
	for i := 1; i <= len(c.Path); i++ {
		parent := c.parent(i)
		switch p := parent.(type) {
		case *ast.ArrayType:
			if p.Len == child {
				return true
			}
		case *ast.Field:
			if p.Tag == child {
				return true
			}
		case *ast.KeyValueExpr:
			if p.Key != child {
				return false
			}
			cl, ok := c.parent(i + 1).(*ast.CompositeLit)
			if !ok {
				return false
			}
			switch c.Info.TypeOf(cl).Underlying().(type) {
			case *types.Array, *types.Slice:
				return true
			}
		case ast.Expr:
			// keep walking: the context of a whole expression is the
			// context of the literal inside it
		default:
			return false
		}
		child = parent
	}
	return false
}

// basicTypeName returns the name of lit's type when it is a predeclared
// basic type that the scope at lit still resolves to the universe's.
// Inside a constant expression such as -1 or 2*3 the literal itself
// stays untyped, so the type of the enclosing constant is used.
func (c *Context) basicTypeName(lit *ast.BasicLit) (string, bool) {
	t := c.Info.TypeOf(lit)
	for i := 1; isUntyped(t); i++ {
		p, ok := c.parent(i).(ast.Expr)
		if !ok || !c.isConst(p) {
			break
		}
		t = c.Info.TypeOf(p)
	}
	return c.universeName(t, lit.Pos())
}

// universeName spells t as the predeclared name it is, when the scope at
// pos still resolves that name to the universe object: a basic type or
// error. Anything else (a defined type, an untyped constant, a composite
// type) has no name a rewrite may spell.
func (c *Context) universeName(t types.Type, pos token.Pos) (string, bool) {
	var name string
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		if isUntyped(u) {
			return "", false
		}
		name = u.Name()
	case *types.Named:
		if u.Obj().Pkg() != nil || u.Obj().Name() != "error" {
			return "", false
		}
		name = "error"
	default:
		return "", false
	}
	_, obj := c.Pkg.Scope().Innermost(pos).LookupParent(name, pos)
	return name, obj == types.Universe.Lookup(name)
}

func isUntyped(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}

// plusOne spells lit+1 as a literal of the same kind.
func plusOne(lit *ast.BasicLit) (string, bool) {
	v := constant.MakeFromLiteral(lit.Value, lit.Kind, 0)
	if v.Kind() == constant.Unknown {
		return "", false
	}
	v = constant.BinaryOp(v, token.ADD, constant.MakeInt64(1))
	if lit.Kind == token.INT {
		return v.ExactString(), true
	}
	f, _ := constant.Float64Val(v)
	if math.IsInf(f, 0) {
		return "", false
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0" // keep the float kind
	}
	return s, true
}
