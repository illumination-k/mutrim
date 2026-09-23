package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// Mutant classes: the groups report.json scores separately, so the score of
// error-handling code can be read next to the overall one. A mutant's class
// is its operator's unless the site sets one, as the return and condition
// sites on an error path do.
const (
	ClassDefault     = "default"
	ClassErrPath     = "errpath"
	ClassConcurrency = "concurrency"
)

// operatorClass maps the operators whose every mutant is of a class other
// than ClassDefault.
var operatorClass = map[string]string{
	"errpath":     ClassErrPath,
	"concurrency": ClassConcurrency,
}

// ErrPath mutates the error path, the code tests reach least (Lima et al.,
// 2021): a wrapping fmt.Errorf that stops wrapping (`%w` -> `%v`) or
// returns the wrapped error as is, errors.Is and errors.As forced to true
// and false, a panic statement removed and a recover that sees no panic.
// A result replaced by nil (return) and a nil check forced (condition) are
// the other operators' mutants, tagged with ClassErrPath.
type ErrPath struct{}

func (ErrPath) Name() string { return "errpath" }

func (ErrPath) Sites(ctx *Context, n ast.Node) []Site {
	switch n := n.(type) {
	case *ast.CallExpr:
		fn := calleeFunc(ctx, n)
		switch {
		case fn != nil && fn.FullName() == "fmt.Errorf":
			return errorfSites(ctx, n)
		case fn != nil && (fn.FullName() == "errors.Is" || fn.FullName() == "errors.As"):
			return errorsIsSites(ctx, n, fn.Name())
		case ctx.isBuiltin(n, "recover"):
			return recoverSites(ctx, n)
		}
	case *ast.ExprStmt:
		return panicSites(ctx, n)
	}
	return nil
}

// errorfSites degrades a wrapping fmt.Errorf with a literal format:
// `%w` becomes `%v`, so errors.Is and errors.As no longer see the cause,
// and `fmt.Errorf("ctx: %w", err)` becomes `err`, so the context is lost.
// The lowerings keep the format constant for vet's printf check and
// evaluate the arguments once, in whichever call the mutant selects.
func errorfSites(ctx *Context, call *ast.CallExpr) []Site {
	if len(call.Args) < 2 || call.Ellipsis.IsValid() || ctx.callsRecover(call) {
		return nil
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}
	format, err := strconv.Unquote(lit.Value)
	if err != nil {
		return nil
	}
	verbs := formatVerbs(format)
	errType := types.Universe.Lookup("error").Type()
	if _, ok := ctx.universeName(errType, call.Pos()); !ok || !strings.ContainsRune(string(verbs), 'w') {
		return nil // the lowerings' closures name error
	}

	degraded := &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(replaceVerb(format, 'w', 'v'))}
	origFmt := call.Args[0]
	sites := []Site{{
		Node:        call,
		Pos:         lit.Pos(),
		End:         lit.End(),
		Description: "%w -> %v",
		Apply:       func() { call.Args[0] = degraded },
		Undo:        func() { call.Args[0] = origFmt },
		Wraps:       true,
		// The arguments are read when lowering, after their own sites
		// replaced them in place.
		Schemata: func(l *Lowering) ast.Node {
			args := append([]ast.Expr{degraded}, call.Args[1:]...)
			return selectCall(l, "error", &ast.CallExpr{Fun: call.Fun, Args: args})
		},
	}}

	if string(verbs) == "w" && len(call.Args) == 2 && types.Identical(ctx.Info.TypeOf(call.Args[1]), errType) {
		fun, args, pos := call.Fun, call.Args, call.Pos()
		sites = append(sites, Site{
			Node:        call,
			Description: "fmt.Errorf -> err",
			// `func(err error) error { return err }(err)` is the wrapped
			// error, evaluated where the call was.
			Apply: func() {
				call.Fun = &ast.FuncLit{
					Type: &ast.FuncType{
						Params:  &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("err")}, Type: ast.NewIdent("error")}}},
						Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}}},
					},
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("err")}}}},
				}
				call.Args = args[1:]
			},
			Undo:     func() { call.Fun, call.Args = fun, args },
			Check:    func() error { return ctx.checkExprAt(call, pos) },
			Wraps:    true,
			Schemata: func(l *Lowering) ast.Node { return selectCall(l, "error", call.Args[1]) },
		})
	}
	return sites
}

// selectCall builds `func() T { if mut.Active(id) { return mutant }; return
// <current> }()`, which evaluates only the selected expression.
func selectCall(l *Lowering, result string, mutant ast.Expr) ast.Expr {
	return &ast.CallExpr{Fun: &ast.FuncLit{
		Type: &ast.FuncType{Params: &ast.FieldList{}, Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent(result)}}}},
		Body: &ast.BlockStmt{List: []ast.Stmt{
			&ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{mutant}}}},
			},
			&ast.ReturnStmt{Results: []ast.Expr{l.Expr()}},
		}},
	}}
}

// formatVerbs lists the verbs of a printf format in order, `%%` excluded.
func formatVerbs(format string) []rune {
	var verbs []rune
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		// Skip flags, width, precision and argument indexes.
		for i < len(format) && strings.IndexByte("+-# 0123456789.*[]", format[i]) >= 0 {
			i++
		}
		if i < len(format) && format[i] != '%' {
			verbs = append(verbs, rune(format[i]))
		}
	}
	return verbs
}

// replaceVerb replaces every verb from in format with to.
func replaceVerb(format string, from, to byte) string {
	b := []byte(format)
	for i := 0; i < len(b); i++ {
		if b[i] != '%' {
			continue
		}
		i++
		for i < len(b) && strings.IndexByte("+-# 0123456789.*[]", b[i]) >= 0 {
			i++
		}
		if i < len(b) && b[i] == from {
			b[i] = to
		}
	}
	return string(b)
}

// errorsIsSites forces errors.Is and errors.As to true and to false: a
// sentinel check that always or never matches. The arguments are still
// evaluated, and errors.As no longer sets its target.
func errorsIsSites(ctx *Context, call *ast.CallExpr, name string) []Site {
	if !ctx.isBool(call) || call.Ellipsis.IsValid() {
		return nil
	}
	fun, pos := call.Fun, call.Pos()
	sites := make([]Site, 0, 2)
	for _, value := range []string{"true", "false"} {
		forced := ast.NewIdent(value)
		sites = append(sites, Site{
			Node:        call,
			Description: "errors." + name + " -> " + value,
			// `func(error, any) bool { return v }(err, target)` takes the
			// arguments of both functions.
			Apply: func() {
				call.Fun = &ast.FuncLit{
					Type: &ast.FuncType{
						Params:  &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}, {Type: ast.NewIdent("any")}}},
						Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("bool")}}},
					},
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{forced}}}},
				}
			},
			Undo:     func() { call.Fun = fun },
			Check:    func() error { return ctx.checkExprAt(call, pos) },
			Wraps:    true,
			Schemata: func(l *Lowering) ast.Node { return l.Call("Cond", l.Expr(), forced) },
		})
	}
	return sites
}

// recoverSites makes recover return nil while it still stops the panic:
// the deferred handler believes nothing went wrong, and the error it would
// have reported is swallowed. A recover whose value is discarded is
// skipped, since the mutant would be equivalent. Both forms keep recover()
// an argument evaluated by the deferred function itself, so it still
// recovers.
func recoverSites(ctx *Context, call *ast.CallExpr) []Site {
	if _, ok := ctx.parent(1).(*ast.ExprStmt); ok {
		return nil
	}
	fun, args, pos := call.Fun, call.Args, call.Pos()
	return []Site{{
		Node:        call,
		Description: "recover() -> nil",
		// `func(any) any { return nil }(recover())`, rebuilt in place so
		// the parent keeps pointing at the call.
		Apply: func() {
			call.Fun = &ast.FuncLit{
				Type: &ast.FuncType{
					Params:  &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("any")}}},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("any")}}},
				},
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("nil")}}}},
			}
			call.Args = []ast.Expr{&ast.CallExpr{Fun: fun}}
		},
		Undo:     func() { call.Fun, call.Args = fun, args },
		Check:    func() error { return ctx.checkExprAt(call, pos) },
		Wraps:    true,
		Schemata: func(l *Lowering) ast.Node { return l.Call("Const", l.Expr(), ast.NewIdent("nil")) },
	}}
}

// panicSites removes a panic statement, the error path's last resort. A
// panic the function needs as its terminating statement is kept, or the
// function would miss a return.
func panicSites(ctx *Context, s *ast.ExprStmt) []Site {
	call, ok := ast.Unparen(s.X).(*ast.CallExpr)
	if !ok || !ctx.isBuiltin(call, "panic") || ctx.Sig == nil {
		return nil
	}
	if ctx.Sig.Results().Len() > 0 && terminates(ctx.funcBody().List, s) {
		return nil
	}
	orig := s.X
	return []Site{{
		Node:        s,
		Description: "panic -> removed",
		Apply: func() {
			s.X = &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{}}}
		},
		Undo:  func() { s.X = orig },
		Wraps: true,
		Schemata: func(l *Lowering) ast.Node {
			if name := l.Cursor.Name(); name == "Init" || name == "Post" {
				return nil
			}
			return &ast.IfStmt{
				Cond: &ast.UnaryExpr{Op: token.NOT, X: l.Active()},
				Body: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// funcBody returns the body of the innermost function enclosing the node
// being visited.
func (c *Context) funcBody() *ast.BlockStmt {
	for i := len(c.Path) - 1; i >= 0; i-- {
		if lit, ok := c.Path[i].(*ast.FuncLit); ok {
			return lit.Body
		}
	}
	body, _ := c.Path[0].(*ast.BlockStmt)
	return body
}

// terminates reports whether s is what makes the statement list list
// terminating, as the spec defines it: its last non-empty statement, or
// that of a block, if/else branch, switch or select clause, or labeled
// statement standing last. A for statement terminates by itself.
func terminates(list []ast.Stmt, s ast.Stmt) bool {
	var last ast.Stmt
	for _, st := range list {
		if _, empty := st.(*ast.EmptyStmt); !empty {
			last = st
		}
	}
	switch l := last.(type) {
	case nil:
		return false
	case *ast.BlockStmt:
		return terminates(l.List, s)
	case *ast.LabeledStmt:
		return terminates([]ast.Stmt{l.Stmt}, s)
	case *ast.IfStmt:
		return terminates(l.Body.List, s) || (l.Else != nil && terminates([]ast.Stmt{l.Else}, s))
	case *ast.SwitchStmt:
		return clausesTerminate(l.Body, s)
	case *ast.TypeSwitchStmt:
		return clausesTerminate(l.Body, s)
	case *ast.SelectStmt:
		return clausesTerminate(l.Body, s)
	}
	return last == s
}

func clausesTerminate(body *ast.BlockStmt, s ast.Stmt) bool {
	for _, c := range body.List {
		switch c := c.(type) {
		case *ast.CaseClause:
			if terminates(c.Body, s) {
				return true
			}
		case *ast.CommClause:
			if terminates(c.Body, s) {
				return true
			}
		}
	}
	return false
}

// isNilCheck reports whether e is `x == nil` or `x != nil`.
func (c *Context) isNilCheck(e ast.Expr) bool {
	b, ok := ast.Unparen(e).(*ast.BinaryExpr)
	if !ok || (b.Op != token.EQL && b.Op != token.NEQ) {
		return false
	}
	return c.Info.Types[b.X].IsNil() || c.Info.Types[b.Y].IsNil()
}
