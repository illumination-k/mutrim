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
	Info *types.Info
	// Sig is the signature of the innermost enclosing function or literal.
	Sig *types.Signature
}

// Site is one rewrite at one node. Apply and Undo mutate the AST in place
// and must be exact inverses; nothing else touches the tree between them.
type Site struct {
	Node ast.Node
	// Pos overrides Node.Pos() for reporting, e.g. the operator token of a
	// binary expression. Zero means use Node.Pos().
	Pos         token.Pos
	Operator    string // filled in by Generate
	Description string
	Apply       func()
	Undo        func()
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
