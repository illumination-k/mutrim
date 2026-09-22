package mutator

import (
	"go/ast"
	"go/token"
)

// Branch empties the body of an if, an else, a case or a select clause, so
// that the branch is entered and does nothing. The rewrite may leave a
// variable or an import unused, or a function without a terminating
// statement; those mutants fail to build and count as not viable. The
// schemata form keeps the body and skips it at runtime instead, so it is
// declined where the body's last statement is what makes the enclosing
// construct terminating.
type Branch struct{}

func (Branch) Name() string { return "branch" }

func (Branch) Sites(ctx *Context, n ast.Node) []Site {
	switch s := n.(type) {
	case *ast.IfStmt:
		sites := blockSite(s.Body, "if body", s.Else != nil)
		if els, ok := s.Else.(*ast.BlockStmt); ok {
			sites = append(sites, blockSite(els, "else body", true)...)
		}
		return sites
	case *ast.CaseClause:
		return clauseSite(s, s.Body, clauseName(s.List == nil), func(body []ast.Stmt) ast.Node {
			return &ast.CaseClause{Case: s.Case, List: s.List, Colon: s.Colon, Body: body}
		}, func(body []ast.Stmt) { s.Body = body })
	case *ast.CommClause:
		return clauseSite(s, s.Body, clauseName(s.Comm == nil), func(body []ast.Stmt) ast.Node {
			return &ast.CommClause{Case: s.Case, Comm: s.Comm, Colon: s.Colon, Body: body}
		}, func(body []ast.Stmt) { s.Body = body })
	}
	return nil
}

// blockSite empties a block statement in place. terminates says whether the
// block's last statement may be what makes the enclosing statement
// terminating, in which case the schemata form would not compile.
func blockSite(body *ast.BlockStmt, what string, terminates bool) []Site {
	if body == nil || len(body.List) == 0 {
		return nil
	}
	orig := body.List
	s := Site{
		Node:        body,
		Description: what + " -> empty",
		Apply:       func() { body.List = nil },
		Undo:        func() { body.List = orig },
		Wraps:       true,
	}
	if !terminates || !isTerminating(orig[len(orig)-1]) {
		s.Schemata = func(l *Lowering) ast.Node {
			block := l.Stmt().(*ast.BlockStmt)
			return &ast.BlockStmt{Lbrace: block.Lbrace, List: []ast.Stmt{skip(l, block.List)}, Rbrace: block.Rbrace}
		}
	}
	return []Site{s}
}

// clauseSite is blockSite for a case or select clause, whose body is a
// statement list rather than a block. A clause body that ends in a
// terminating statement decides whether the switch terminates, so its
// schemata form is always declined; a fallthrough must stay last, which
// isTerminating reports too.
func clauseSite(clause ast.Node, body []ast.Stmt, what string, rebuild func([]ast.Stmt) ast.Node, set func([]ast.Stmt)) []Site {
	if len(body) == 0 {
		return nil
	}
	s := Site{
		Node:        clause,
		Description: what + " -> empty",
		Apply:       func() { set(nil) },
		Undo:        func() { set(body) },
		Wraps:       true,
	}
	if !isTerminating(body[len(body)-1]) {
		s.Schemata = func(l *Lowering) ast.Node { return rebuild([]ast.Stmt{skip(l, body)}) }
	}
	return []Site{s}
}

// clauseName distinguishes the default clause of a switch or select, so that
// the mutants of one statement have distinct descriptions.
func clauseName(isDefault bool) string {
	if isDefault {
		return "default body"
	}
	return "case body"
}

// skip wraps a statement list in `if !mut.Active(id) { ... }`.
func skip(l *Lowering, body []ast.Stmt) ast.Stmt {
	return &ast.IfStmt{
		Cond: &ast.UnaryExpr{Op: token.NOT, X: l.Active()},
		Body: &ast.BlockStmt{List: body},
	}
}

// isTerminating reports whether s may be a terminating statement in the
// sense of the Go spec, which a function with results needs at the end of
// its body. It is deliberately conservative: a statement it cannot judge
// (a switch, a select, a label) counts as terminating, so the rewrite that
// would hide it is declined.
func isTerminating(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.GOTO || s.Tok == token.FALLTHROUGH
	case *ast.ExprStmt:
		call, ok := ast.Unparen(s.X).(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := ast.Unparen(call.Fun).(*ast.Ident)
		return ok && id.Name == "panic"
	case *ast.BlockStmt:
		return len(s.List) > 0 && isTerminating(s.List[len(s.List)-1])
	case *ast.IfStmt:
		return s.Else != nil && isTerminating(s.Body) && isTerminating(s.Else)
	case *ast.LabeledStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.ForStmt:
		return true
	}
	return false
}
