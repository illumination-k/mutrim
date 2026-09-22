package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
)

// methodSwaps maps a function or method to the sibling it is replaced with,
// keyed by the qualified name go/types gives it: "path.Name" for a
// package-level function, "(path.Type).Name" for a method. Only
// same-signature pairs are listed, so a swap is well-typed by construction.
var methodSwaps = map[string]string{
	"strings.HasPrefix":        "HasSuffix",
	"strings.HasSuffix":        "HasPrefix",
	"strings.TrimPrefix":       "TrimSuffix",
	"strings.TrimSuffix":       "TrimPrefix",
	"strings.CutPrefix":        "CutSuffix",
	"strings.CutSuffix":        "CutPrefix",
	"strings.TrimLeft":         "TrimRight",
	"strings.TrimRight":        "TrimLeft",
	"strings.ToLower":          "ToUpper",
	"strings.ToUpper":          "ToLower",
	"strings.Index":            "LastIndex",
	"strings.LastIndex":        "Index",
	"strings.IndexAny":         "LastIndexAny",
	"strings.LastIndexAny":     "IndexAny",
	"strings.IndexByte":        "LastIndexByte",
	"strings.LastIndexByte":    "IndexByte",
	"strings.Split":            "SplitAfter",
	"strings.SplitAfter":       "Split",
	"bytes.HasPrefix":          "HasSuffix",
	"bytes.HasSuffix":          "HasPrefix",
	"bytes.TrimPrefix":         "TrimSuffix",
	"bytes.TrimSuffix":         "TrimPrefix",
	"bytes.TrimLeft":           "TrimRight",
	"bytes.TrimRight":          "TrimLeft",
	"bytes.ToLower":            "ToUpper",
	"bytes.ToUpper":            "ToLower",
	"bytes.Index":              "LastIndex",
	"bytes.LastIndex":          "Index",
	"bytes.IndexByte":          "LastIndexByte",
	"bytes.LastIndexByte":      "IndexByte",
	"math.Floor":               "Ceil",
	"math.Ceil":                "Floor",
	"math.Max":                 "Min",
	"math.Min":                 "Max",
	"unicode.IsUpper":          "IsLower",
	"unicode.IsLower":          "IsUpper",
	"unicode.ToUpper":          "ToLower",
	"unicode.ToLower":          "ToUpper",
	"path.Dir":                 "Base",
	"path.Base":                "Dir",
	"path/filepath.Dir":        "Base",
	"path/filepath.Base":       "Dir",
	"sort.Slice":               "SliceStable",
	"sort.SliceStable":         "Slice",
	"slices.Min":               "Max",
	"slices.Max":               "Min",
	"slices.MinFunc":           "MaxFunc",
	"slices.MaxFunc":           "MinFunc",
	"slices.SortFunc":          "SortStableFunc",
	"slices.SortStableFunc":    "SortFunc",
	"(time.Time).Before":       "After",
	"(time.Time).After":        "Before",
	"(time.Duration).Round":    "Truncate",
	"(time.Duration).Truncate": "Round",
}

// Method replaces a call to a well-known library function with its
// mirror image: strings.HasPrefix becomes strings.HasSuffix, math.Floor
// becomes math.Ceil. Only the selected name changes, so the arguments and
// their evaluation order are untouched.
type Method struct{}

func (Method) Name() string { return "method" }

func (Method) Sites(ctx *Context, n ast.Node) []Site {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	fn, ok := ctx.Info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return nil
	}
	to, ok := methodSwaps[fn.FullName()]
	if !ok {
		return nil
	}
	from := sel.Sel.Name
	s := Site{
		Node:        call,
		Pos:         sel.Sel.Pos(),
		Description: from + " -> " + to,
		Apply:       func() { sel.Sel.Name = to },
		Undo:        func() { sel.Sel.Name = from },
		// The table is same-signature by construction; the check keeps a
		// wrong row from producing a mutant that cannot build.
		Check:    func() error { return ctx.CheckExpr(call) },
		Schemata: methodSchemata(ctx, call, sel, to),
	}
	return []Site{s}
}

// methodSchemata lowers a swap to `mut.Const(id, pkg.Orig, pkg.Mutant)(args)`,
// which picks a function value and then calls it, so the arguments are still
// evaluated once, in place. A generic function has no function value without
// instantiation, and a method value would evaluate its receiver twice, so
// those are declined.
func methodSchemata(ctx *Context, call *ast.CallExpr, sel *ast.SelectorExpr, to string) func(*Lowering) ast.Node {
	fn, _ := ctx.Info.Uses[sel.Sel].(*types.Func)
	sig := fn.Signature()
	if sig.TypeParams().Len() > 0 || sig.RecvTypeParams().Len() > 0 {
		return nil
	}
	if sig.Recv() != nil && containsCall(sel.X) {
		return nil
	}
	mutant := &ast.SelectorExpr{X: sel.X, Sel: ast.NewIdent(to)}
	return func(l *Lowering) ast.Node {
		return &ast.CallExpr{
			Fun:      l.Call("Const", sel, mutant),
			Args:     call.Args,
			Ellipsis: call.Ellipsis,
		}
	}
}

// Call replaces a call in expression position with the zero value of its
// result, so that the call never happens: the counterpart of voidcall for a
// function whose result is used. It is opt-in (`-operators default,call`),
// like PIT's NON_VOID_METHOD_CALLS: when the callee returns the zero value
// anyway, the mutant is equivalent to the original.
//
// Only a single result whose type can be spelled as a predeclared name
// (a basic type or error) is a site, since both rewrites need to name it.
// Calls in a go or defer statement, and calls whose arguments call recover,
// are skipped: moving them into a closure would change what they do.
type Call struct{}

func (Call) Name() string { return "call" }

func (Call) Sites(ctx *Context, n ast.Node) []Site {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	tv, ok := ctx.Info.Types[call]
	if !ok || !tv.IsValue() || tv.Value != nil {
		return nil
	}
	fun, known := ctx.Info.Types[ast.Unparen(call.Fun)]
	if !known || fun.IsType() || fun.IsBuiltin() {
		return nil // a conversion or a builtin is not a call that can be skipped
	}
	switch ctx.parent(1).(type) {
	case *ast.GoStmt, *ast.DeferStmt:
		return nil
	}
	if ctx.callsRecover(call) {
		return nil
	}
	name, ok := ctx.universeName(tv.Type, call.Pos())
	if !ok {
		return nil
	}
	zero, ok := zeroExpr(tv.Type)
	if !ok {
		return nil
	}

	results := func() *ast.FieldList {
		return &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent(name)}}}
	}
	origFun, args, ellipsis := call.Fun, call.Args, call.Ellipsis
	return []Site{{
		Node:        call,
		Description: "call -> " + types.ExprString(zero),
		// `func() T { return zero }()` is an expression of the call's type,
		// valid wherever the call was, including as a statement.
		Apply: func() {
			call.Fun = &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}, Results: results()},
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{zero}}}},
			}
			call.Args, call.Ellipsis = nil, token.NoPos
		},
		Undo:  func() { call.Fun, call.Args, call.Ellipsis = origFun, args, ellipsis },
		Wraps: true,
		Schemata: func(l *Lowering) ast.Node {
			lit := &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}, Results: results()},
				Body: &ast.BlockStmt{List: []ast.Stmt{
					&ast.IfStmt{
						Cond: l.Active(),
						Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{zero}}}},
					},
					&ast.ReturnStmt{Results: []ast.Expr{l.Expr()}},
				}},
			}
			return &ast.CallExpr{Fun: lit}
		},
	}}
}
