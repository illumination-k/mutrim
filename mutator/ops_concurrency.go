package mutator

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Concurrency mimics the real-world Go concurrency bugs of Tu et al.
// ("Understanding real-world concurrency bugs in Go", ASPLOS 2019) by
// inverting their fixes: a defer removed or run immediately, a goroutine
// run inline, a send removed, a channel's buffer changed, a select case
// that never fires, an atomic operation made plain and a sync.Once
// bypassed. It is opt-in (`-operators default,concurrency`); its mutants
// are best run from a test binary built with -race, and with
// `run -confirm-kills` since their kills are nondeterministic.
//
// Removing a close, a Lock or Unlock, a WaitGroup Add, Done or Wait, or a
// context cancel is a call statement removed, which voidcall already does.
type Concurrency struct{}

func (Concurrency) Name() string { return "concurrency" }

func (Concurrency) Sites(ctx *Context, n ast.Node) []Site {
	switch s := n.(type) {
	case *ast.DeferStmt:
		return deferSites(ctx, s)
	case *ast.GoStmt:
		return goSites(ctx, s)
	case *ast.SendStmt:
		return sendSites(ctx, s)
	case *ast.CallExpr:
		return chanBufferSites(ctx, s)
	case *ast.CommClause:
		return selectCaseSites(ctx, s)
	case *ast.ExprStmt:
		return append(atomicSites(ctx, s), onceSites(ctx, s)...)
	}
	return nil
}

// deferSites drops `defer f()` and runs it on the spot instead. The
// lowerings keep the defer inside an if, which still defers to the end of
// the function.
func deferSites(ctx *Context, s *ast.DeferStmt) []Site {
	orig := s.Call
	noop := &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{}}}
	removed := Site{
		Node:        s,
		Description: "defer -> removed",
		Apply:       func() { s.Call = noop },
		Undo:        func() { s.Call = orig },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			return &ast.IfStmt{
				Cond: &ast.UnaryExpr{Op: token.NOT, X: l.Active()},
				Body: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}
	return append([]Site{removed}, inlineSite(ctx, s, s.Call, "defer f() -> f()")...)
}

// goSites runs the goroutine's function on the calling goroutine.
func goSites(ctx *Context, s *ast.GoStmt) []Site {
	return inlineSite(ctx, s, s.Call, "go f() -> f()")
}

// inlineSite replaces a go or defer statement with its call. The call is
// read from the statement when lowering, since the lowerings of its
// children replace them in place.
func inlineSite(ctx *Context, s ast.Stmt, call *ast.CallExpr, description string) []Site {
	slot := ctx.stmtSlot(s)
	if slot == nil {
		return nil
	}
	return []Site{{
		Node:        s,
		Description: description,
		Apply:       func() { *slot = &ast.ExprStmt{X: call} },
		Undo:        func() { *slot = s },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			var call *ast.CallExpr
			switch s := s.(type) {
			case *ast.DeferStmt:
				call = s.Call
			case *ast.GoStmt:
				call = s.Call
			}
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: call}}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// sendSites drops a send statement: the receiver waits for a value that
// never comes. A send in a select case or a for init or post statement has
// no statement list to be dropped from and is skipped.
func sendSites(ctx *Context, s *ast.SendStmt) []Site {
	slot := ctx.stmtSlot(s)
	if slot == nil {
		return nil
	}
	return []Site{{
		Node:        s,
		Description: "send -> removed",
		Apply:       func() { *slot = &ast.EmptyStmt{Implicit: true} },
		Undo:        func() { *slot = s },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			return &ast.IfStmt{
				Cond: &ast.UnaryExpr{Op: token.NOT, X: l.Active()},
				Body: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// chanBufferSites changes the buffer of `make(chan T, n)`: an unbuffered
// channel gets a buffer of 1 (the fix of a blocked sender, undone), a
// buffered one loses its buffer, and a buffer computed at run time grows
// by one. A constant buffer growing is the constant operator's mutant.
func chanBufferSites(ctx *Context, call *ast.CallExpr) []Site {
	if !ctx.isBuiltin(call, "make") || len(call.Args) == 0 {
		return nil
	}
	if _, ok := ctx.Info.TypeOf(call.Args[0]).Underlying().(*types.Chan); !ok {
		return nil
	}
	if len(call.Args) == 1 {
		args := call.Args
		return []Site{{
			Node:        call,
			Description: "unbuffered -> buffer 1",
			Apply:       func() { call.Args = []ast.Expr{args[0], intLit("1")} },
			Undo:        func() { call.Args = args },
			Wraps:       true,
			Schemata: func(l *Lowering) ast.Node {
				cur, ok := l.Expr().(*ast.CallExpr)
				if !ok {
					return nil
				}
				return &ast.CallExpr{Fun: cur.Fun, Args: []ast.Expr{cur.Args[0], l.Call("Const", intLit("0"), intLit("1"))}}
			},
		}}
	}

	size := &call.Args[1]
	orig := *size
	if b, ok := ctx.Info.TypeOf(orig).Underlying().(*types.Basic); !ok || b.Info()&types.IsInteger == 0 {
		return nil // an untyped float constant would make mut.Const's result a float
	}
	var sites []Site
	if v := ctx.Info.Types[orig].Value; v == nil || v.ExactString() != "0" {
		sites = append(sites, Site{
			Node:        orig,
			Description: "buffer -> 0",
			Apply:       func() { *size = intLit("0") },
			Undo:        func() { *size = orig },
			Wraps:       true,
			Schemata:    func(l *Lowering) ast.Node { return l.Call("Const", l.Expr(), intLit("0")) },
		})
	}
	// The lowering evaluates the size twice, so a call in it is declined.
	if !ctx.isConst(orig) && !containsCall(orig) {
		sites = append(sites, Site{
			Node:        orig,
			Description: "buffer -> buffer+1",
			Apply:       func() { *size = plusOneExpr(orig) },
			Undo:        func() { *size = orig },
			Wraps:       true,
			Schemata: func(l *Lowering) ast.Node {
				return l.Call("Const", l.Expr(), plusOneExpr(l.Expr()))
			},
		})
	}
	return sites
}

// selectCaseSites makes a select case never fire, by replacing its channel
// with a nil channel of the same type: the missing case of a select that
// blocks forever or never observes a cancellation.
func selectCaseSites(ctx *Context, clause *ast.CommClause) []Site {
	ch := commChan(clause.Comm)
	if ch == nil {
		return nil
	}
	orig := *ch
	t := ctx.Info.TypeOf(orig)
	if _, ok := types.Unalias(t).(*types.TypeParam); ok || t == nil {
		return nil // a type parameter has no nil to spell
	}
	// The overlay needs the nil channel spelled with its type. The
	// rewrite is an identifier holding the conversion's source, which the
	// printer writes verbatim; Check type-checks the parsed conversion.
	spelled := "(" + types.TypeString(t, func(p *types.Package) string {
		if p == ctx.Pkg {
			return ""
		}
		return p.Name()
	}) + ")(nil)"
	return []Site{{
		Node:        orig,
		Description: "case -> never ready",
		Apply:       func() { *ch = ast.NewIdent(spelled) },
		Undo:        func() { *ch = orig },
		Check:       func() error { return ctx.checkSpelled(spelled, orig.Pos(), t) },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			return l.Call("Const", l.Expr(), ast.NewIdent("nil"))
		},
	}}
}

// commChan returns the channel operand of a select case, or nil for the
// default clause.
func commChan(comm ast.Stmt) *ast.Expr {
	var rx ast.Expr
	switch s := comm.(type) {
	case *ast.SendStmt:
		return &s.Chan
	case *ast.ExprStmt:
		rx = s.X
	case *ast.AssignStmt:
		rx = s.Rhs[0]
	default:
		return nil
	}
	u, ok := ast.Unparen(rx).(*ast.UnaryExpr)
	if !ok || u.Op != token.ARROW {
		return nil
	}
	return &u.X
}

// atomicWrites maps the sync/atomic functions made plain to the assignment
// operator that replaces them: `atomic.AddInt64(&x, d)` becomes `x += d`,
// a data race the race detector reports.
var atomicWrites = map[string]token.Token{
	"AddInt32": token.ADD_ASSIGN, "AddInt64": token.ADD_ASSIGN, "AddUint32": token.ADD_ASSIGN,
	"AddUint64": token.ADD_ASSIGN, "AddUintptr": token.ADD_ASSIGN,
	"StoreInt32": token.ASSIGN, "StoreInt64": token.ASSIGN, "StoreUint32": token.ASSIGN,
	"StoreUint64": token.ASSIGN, "StoreUintptr": token.ASSIGN, "StorePointer": token.ASSIGN,
}

// atomicSites replaces an atomic add or store statement on `&x` with its
// plain counterpart.
func atomicSites(ctx *Context, s *ast.ExprStmt) []Site {
	call, ok := ast.Unparen(s.X).(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return nil
	}
	fn := calleeFunc(ctx, call)
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "sync/atomic" || fn.Signature().Recv() != nil {
		return nil
	}
	tok, ok := atomicWrites[fn.Name()]
	if !ok {
		return nil
	}
	addr, ok := ast.Unparen(call.Args[0]).(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return nil
	}
	slot := ctx.stmtSlot(s)
	if slot == nil {
		return nil
	}
	plain := func() ast.Stmt {
		return &ast.AssignStmt{Lhs: []ast.Expr{addr.X}, Tok: tok, Rhs: []ast.Expr{call.Args[1]}}
	}
	return []Site{{
		Node:        s,
		Description: "atomic." + fn.Name() + " -> " + tok.String(),
		Apply:       func() { *slot = plain() },
		Undo:        func() { *slot = s },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{plain()}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// onceSites calls the function of `once.Do(f)` every time.
func onceSites(ctx *Context, s *ast.ExprStmt) []Site {
	call, ok := ast.Unparen(s.X).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil
	}
	fn := calleeFunc(ctx, call)
	if fn == nil || fn.FullName() != "(*sync.Once).Do" {
		return nil
	}
	orig := s.X
	direct := func() ast.Expr { return &ast.CallExpr{Fun: call.Args[0]} }
	return []Site{{
		Node:        s,
		Description: "once.Do(f) -> f()",
		Apply:       func() { s.X = direct() },
		Undo:        func() { s.X = orig },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			if name := l.Cursor.Name(); name == "Init" || name == "Post" {
				return nil
			}
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: direct()}}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

func intLit(v string) *ast.BasicLit { return &ast.BasicLit{Kind: token.INT, Value: v} }

// plusOneExpr builds `e + 1`, parenthesizing e unless it is an operand.
func plusOneExpr(e ast.Expr) ast.Expr {
	switch e.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.ParenExpr:
	default:
		e = &ast.ParenExpr{X: e}
	}
	return &ast.BinaryExpr{X: e, Op: token.ADD, Y: intLit("1")}
}
