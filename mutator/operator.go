package mutator

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"
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
	// Path holds the nodes enclosing the one being visited, outermost
	// first: the function body down to the parent.
	Path []ast.Node
}

// parent returns the n-th enclosing node (1 is the direct parent), or nil.
func (c *Context) parent(n int) ast.Node {
	if n > len(c.Path) {
		return nil
	}
	return c.Path[len(c.Path)-n]
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

// inConstDecl reports whether the node being visited sits inside a
// constant declaration, where a rewrite into a call would not be constant.
func (c *Context) inConstDecl() bool {
	for _, n := range c.Path {
		if d, ok := n.(*ast.GenDecl); ok && d.Tok == token.CONST {
			return true
		}
	}
	return false
}

// isBuiltin reports whether call invokes the named builtin function.
func (c *Context) isBuiltin(call *ast.CallExpr, name string) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	b, ok := c.Info.Uses[id].(*types.Builtin)
	return ok && b.Name() == name
}

// callsRecover reports whether e contains a call to the builtin recover.
// Moving such a call into a closure would change what it returns.
func (c *Context) callsRecover(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && c.isBuiltin(call, "recover") {
			found = true
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
	Pos token.Pos
	// End overrides Node.End(), and must be set whenever Pos narrows the
	// site to part of Node. Zero means use Node.End().
	End         token.Pos
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
	// Wraps says that Schemata embeds the node's current lowering
	// (Lowering.Current) instead of rebuilding the replacement from the
	// original node's parts. Every site of one node but the first must
	// wrap, or the lowerings would replace each other; Lower orders the
	// rebuilding one first.
	Wraps bool
	// Ignored names the rule under which the operator reports the site
	// but keeps it out of the run, as a Filter would; empty keeps it.
	Ignored string
}

// Operators selects operators by name from a comma-separated spec: a name
// adds that operator, "default" adds DefaultOperators, and a name with a
// leading "-" removes one, so "default,-constant" is every default but
// constant. Operators outside DefaultOperators are opt-in and only a name
// selects them. An empty spec is DefaultOperators. The result keeps the
// order of AllOperators, so mutant listings do not depend on the spelling.
func Operators(spec string) ([]Operator, error) {
	if spec == "" {
		return DefaultOperators, nil
	}
	byName := map[string]Operator{}
	for _, op := range AllOperators {
		byName[op.Name()] = op
	}
	selected := map[string]bool{}
	for entry := range strings.SplitSeq(spec, ",") {
		name, remove := strings.CutPrefix(strings.TrimSpace(entry), "-")
		switch {
		case name == "default" && !remove:
			for _, op := range DefaultOperators {
				selected[op.Name()] = true
			}
		case byName[name] != nil:
			selected[name] = !remove
		default:
			return nil, fmt.Errorf("mutator: unknown operator %q (known: %s, default)", name, strings.Join(slices.Sorted(maps.Keys(byName)), ", "))
		}
	}
	var ops []Operator
	for _, op := range AllOperators {
		if selected[op.Name()] {
			ops = append(ops, op)
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("mutator: %q selects no operator", spec)
	}
	return ops, nil
}

// AllOperators is every operator mutrim implements, in the order mutants
// are listed in. DefaultOperators is the subset applied when none is
// named; the rest are opt-in through Operators.
var AllOperators = []Operator{
	Relational,
	Invert,
	Arithmetic,
	Logical,
	Bitwise,
	Negatives{},
	Negation{},
	Condition{},
	IncDec{},
	AssignOp{},
	VoidCall{},
	Assign{},
	Branch{},
	LoopCtrl{},
	LoopCond{},
	Constant{},
	Boolean{},
	String{},
	Composite{},
	Method{},
	Call{},
	Return{},
}

// DefaultOperators is the operator set used when Options.Operators is nil.
// Call is left out: replacing a call with a zero value produces many
// mutants equivalent to the original, so PIT keeps it opt-in too.
var DefaultOperators = defaultsExcept(Call{})

func defaultsExcept(opt ...Operator) []Operator {
	optional := map[string]bool{}
	for _, op := range opt {
		optional[op.Name()] = true
	}
	var ops []Operator
	for _, op := range AllOperators {
		if !optional[op.Name()] {
			ops = append(ops, op)
		}
	}
	return ops
}
