package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Operator is one mutation family. Sites is called for every node under
// every function body and returns the rewrites it can perform on that node.
type Operator interface {
	Name() string
	Sites(ctx *Context, n ast.Node) []Site
}

// Context is what an operator may consult besides the node itself.
type Context struct {
	Fset *token.FileSet
	Pkg  *types.Package
	Info *types.Info
	// Sig is the signature of the innermost enclosing function or literal.
	Sig *types.Signature
}

// CheckExpr type-checks e in the scope it appears in, using the package's
// existing type information. It is the local pre-filter for expression
// mutants: no whole-package re-check is needed.
func (c *Context) CheckExpr(e ast.Expr) error {
	return types.CheckExpr(c.Fset, c.Pkg, e.Pos(), e, &types.Info{})
}

// isBool reports whether e has type bool or untyped bool. Defined boolean
// types are excluded: the runtime helpers return plain bool, which is not
// assignable to them.
func (c *Context) isBool(e ast.Expr) bool {
	tv, ok := c.Info.Types[e]
	if !ok {
		return false
	}
	b, ok := types.Unalias(tv.Type).(*types.Basic)
	return ok && (b.Kind() == types.Bool || b.Kind() == types.UntypedBool)
}

// isConst reports whether e is a constant expression.
func (c *Context) isConst(e ast.Expr) bool {
	return c.Info.Types[e].Value != nil
}

// isMapIndex reports whether e is an index expression on a map, the one
// operand of ++/-- that is not addressable.
func (c *Context) isMapIndex(e ast.Expr) bool {
	ix, ok := ast.Unparen(e).(*ast.IndexExpr)
	if !ok {
		return false
	}
	_, isMap := c.Info.TypeOf(ix.X).Underlying().(*types.Map)
	return isMap
}

// callsRecover reports whether e contains a call to the builtin recover.
// Moving such a call into a closure would change what it returns.
func (c *Context) callsRecover(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			if b, ok := c.Info.Uses[id].(*types.Builtin); ok && b.Name() == "recover" {
				found = true
			}
		}
		return !found
	})
	return found
}

// Site is one rewrite at one node. Apply and Undo mutate the AST in place
// and must be exact inverses; nothing else touches the tree between them.
// Check, if set, is run between them and decides viability; a nil Check
// means the rewrite is well-typed by construction.
type Site struct {
	Node ast.Node
	// Pos overrides Node.Pos() for reporting, e.g. the operator token of a
	// binary expression. Zero means use Node.Pos().
	Pos         token.Pos
	Operator    string // filled in by Generate
	Description string
	Apply       func()
	Undo        func()
	Check       func() error
	// Schemata returns the node that replaces Node in schemata sources,
	// with the mutant selectable through the runtime. It is called after
	// the children of Node have been lowered and may return nil to decline.
	// A nil Schemata means the site can only be applied through Apply.
	Schemata func(l *Lowering) ast.Node
}

// DefaultOperators is the operator set used when Options.Operators is nil.
var DefaultOperators = []Operator{
	Relational,
	Arithmetic,
	Logical,
	Negation{},
	IncDec{},
	Return{},
}
