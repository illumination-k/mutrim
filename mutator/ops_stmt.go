package mutator

import (
	"go/ast"
	"go/token"
)

// Negation negates the condition of if and for statements.
type Negation struct{}

func (Negation) Name() string { return "negation" }

func (Negation) Sites(ctx *Context, n ast.Node) []Site {
	var cond *ast.Expr
	switch s := n.(type) {
	case *ast.IfStmt:
		cond = &s.Cond
	case *ast.ForStmt:
		if s.Cond == nil {
			return nil
		}
		cond = &s.Cond
	default:
		return nil
	}
	orig := *cond
	negated := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: orig}}
	s := Site{
		Node:        orig,
		Description: "cond -> !(cond)",
		Apply:       func() { *cond = negated },
		Undo:        func() { *cond = orig },
	}
	if ctx.isBool(orig) {
		// *cond is read at lowering time so that an inner rewrite of the
		// condition (a relational site on the same node) is wrapped.
		s.Schemata = func(l *Lowering) ast.Node { return l.Call("Not", *cond) }
	}
	return []Site{s}
}

// Condition forces the condition of an if statement to true and to false.
// The condition is still evaluated, so its side effects are kept.
type Condition struct{}

func (Condition) Name() string { return "condition" }

func (Condition) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.IfStmt)
	if !ok {
		return nil
	}
	cond := &s.Cond
	orig := *cond
	sites := make([]Site, 0, 2)
	for _, value := range []string{"true", "false"} {
		forced := ast.NewIdent(value)
		site := Site{
			Node:        orig,
			Description: "cond -> " + value,
			Apply:       func() { *cond = forced },
			Undo:        func() { *cond = orig },
		}
		if ctx.isBool(orig) {
			// *cond is read at lowering time, like Negation's.
			site.Schemata = func(l *Lowering) ast.Node { return l.Call("Cond", *cond, forced) }
		}
		sites = append(sites, site)
	}
	return sites
}

// VoidCall removes a call statement whose callee returns nothing. A call
// to panic is kept: removing it could leave a function without a
// terminating statement, and the schemata source must compile.
type VoidCall struct{}

func (VoidCall) Name() string { return "voidcall" }

func (VoidCall) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	call, ok := ast.Unparen(s.X).(*ast.CallExpr)
	if !ok || !ctx.Info.Types[call].IsVoid() || ctx.isBuiltin(call, "panic") {
		return nil
	}
	orig := s.X
	return []Site{{
		Node:        s,
		Description: "call -> removed",
		// `func() {}()` is a statement that does nothing and is valid
		// wherever the call was, including a for post statement.
		Apply: func() {
			s.X = &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{}}}
		},
		Undo: func() { s.X = orig },
		// `if !mut.Active(id) { f() }` is not a simple statement, so a
		// call in an if/for/switch init or a for post is declined.
		Schemata: func(l *Lowering) ast.Node {
			if name := l.Cursor.Name(); name == "Init" || name == "Post" {
				return nil
			}
			return &ast.IfStmt{
				Cond: &ast.UnaryExpr{Op: token.NOT, X: l.Active()},
				Body: &ast.BlockStmt{List: []ast.Stmt{s}},
			}
		},
	}}
}

// IncDec swaps ++ and --.
type IncDec struct{}

func (IncDec) Name() string { return "incdec" }

func (IncDec) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.IncDecStmt)
	if !ok {
		return nil
	}
	from := s.Tok
	to := token.DEC
	if from == token.DEC {
		to = token.INC
	}
	return []Site{{
		Node:        s,
		Description: from.String() + " -> " + to.String(),
		Apply:       func() { s.Tok = to },
		Undo:        func() { s.Tok = from },
		Schemata:    incDecSchemata(ctx, s, to),
	}}
}

// incDecSchemata lowers `x++` to `mut.Inc(id, &x)`, which is valid wherever
// the statement was, including a for post statement. A map element is not
// addressable, so it becomes `if mut.Active(id) { m[k]-- } else { m[k]++ }`
// instead, which a for post statement cannot hold; that site is declined.
func incDecSchemata(ctx *Context, s *ast.IncDecStmt, to token.Token) func(*Lowering) ast.Node {
	fn := "Inc"
	if s.Tok == token.DEC {
		fn = "Dec"
	}
	if !ctx.isMapIndex(s.X) {
		return func(l *Lowering) ast.Node {
			var addr ast.Expr = &ast.UnaryExpr{Op: token.AND, X: s.X}
			if star, ok := ast.Unparen(s.X).(*ast.StarExpr); ok {
				addr = star.X // &*p is p
			}
			return &ast.ExprStmt{X: l.Call(fn, addr)}
		}
	}
	return func(l *Lowering) ast.Node {
		if l.Cursor.Name() == "Post" {
			return nil
		}
		return &ast.IfStmt{
			Cond: l.Active(),
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.IncDecStmt{X: s.X, Tok: to}}},
			Else: &ast.BlockStmt{List: []ast.Stmt{s}},
		}
	}
}
