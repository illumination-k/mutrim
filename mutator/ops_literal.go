package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
)

// Boolean flips a true or false literal, which the condition operator
// reaches only inside an if: `return true`, `x := false` and a field of a
// composite literal have no other site. Literals in a constant declaration
// are skipped, as in the constant operator.
type Boolean struct{}

func (Boolean) Name() string { return "boolean" }

func (Boolean) Sites(ctx *Context, n ast.Node) []Site {
	id, ok := n.(*ast.Ident)
	if !ok || (id.Name != "true" && id.Name != "false") {
		return nil
	}
	if ctx.Info.Uses[id] != types.Universe.Lookup(id.Name) || ctx.inConstDecl() {
		return nil
	}
	from, to := id.Name, "true"
	if from == "true" {
		to = "false"
	}
	s := Site{
		Node:        id,
		Description: from + " -> " + to,
		Apply:       func() { id.Name = to },
		Undo:        func() { id.Name = from },
	}
	// A defined bool type is declined: the runtime helper returns a plain
	// bool, which is not assignable to it.
	if ctx.isBool(id) {
		s.Schemata = func(l *Lowering) ast.Node { return l.Call("Cond", id, ast.NewIdent(to)) }
	}
	return []Site{s}
}

// String empties a string literal, or fills an empty one, so that error
// messages, map keys and switch subjects are mutated. Literals Go requires
// to stay literal (struct tags, constant declarations, array lengths) are
// skipped; the schemata form is a call, so it is declined where the
// literal's type is not the predeclared string.
type String struct{}

func (String) Name() string { return "string" }

// filler is what an empty string literal is replaced with.
const filler = `"mutrim"`

func (String) Sites(ctx *Context, n ast.Node) []Site {
	lit, ok := n.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING || ctx.mustBeConstant(lit) {
		return nil
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return nil
	}
	mutated := `""`
	if value == "" {
		mutated = filler
	}
	orig := lit.Value
	s := Site{
		Node:        lit,
		Description: orig + " -> " + mutated,
		Apply:       func() { lit.Value = mutated },
		Undo:        func() { lit.Value = orig },
	}
	if name, ok := ctx.universeName(ctx.Info.TypeOf(lit), lit.Pos()); ok && name == "string" {
		conv := &ast.CallExpr{Fun: ast.NewIdent(name), Args: []ast.Expr{lit}}
		replacement := &ast.BasicLit{Kind: token.STRING, Value: mutated}
		s.Schemata = func(l *Lowering) ast.Node { return l.Call("Const", conv, replacement) }
	}
	return []Site{s}
}

// Composite drops the elements of a slice or map literal. The rewrite
// leaves the literal's type in place, so it is well-typed by construction;
// the schemata form selects a nil slice or map instead, which has no
// elements either but cannot be spelled as a literal without repeating the
// type.
type Composite struct{}

func (Composite) Name() string { return "composite" }

func (Composite) Sites(ctx *Context, n ast.Node) []Site {
	cl, ok := n.(*ast.CompositeLit)
	if !ok || len(cl.Elts) == 0 {
		return nil
	}
	switch ctx.Info.TypeOf(cl).Underlying().(type) {
	case *types.Slice, *types.Map:
	default:
		return nil
	}
	orig := cl.Elts
	s := Site{
		Node:        cl,
		Description: "elements -> none",
		Apply:       func() { cl.Elts = nil },
		Undo:        func() { cl.Elts = orig },
		Wraps:       true,
	}
	// A literal with an elided type (an element of an outer literal) has no
	// form outside its context, so it cannot become a call argument.
	if cl.Type != nil {
		s.Schemata = func(l *Lowering) ast.Node { return l.Call("Const", l.Expr(), ast.NewIdent("nil")) }
	}
	return []Site{s}
}
