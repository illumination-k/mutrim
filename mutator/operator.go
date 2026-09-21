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
