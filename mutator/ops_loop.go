package mutator

import (
	"go/ast"
	"go/token"
)

// LoopCtrl swaps break and continue. Only unlabeled statements whose
// nearest enclosing breakable statement is a loop are sites: a break in a
// switch or select leaves that statement instead, so swapping it would
// change which statement the mutant is about. A continue of an
// unconditional for that no break leaves is not a site either: the break
// would stop the loop from being a terminating statement, and a function
// ending in it would no longer compile.
type LoopCtrl struct{}

func (LoopCtrl) Name() string { return "loopctrl" }

func (LoopCtrl) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.BranchStmt)
	if !ok || s.Label != nil || (s.Tok != token.BREAK && s.Tok != token.CONTINUE) {
		return nil
	}
	loop, label := ctx.enclosingLoop()
	if loop == nil {
		return nil
	}
	if f, ok := loop.(*ast.ForStmt); ok && s.Tok == token.CONTINUE && f.Cond == nil && !breaks(f.Body, label) {
		return nil
	}
	from, to := s.Tok, token.CONTINUE
	if from == token.CONTINUE {
		to = token.BREAK
	}
	return []Site{{
		Node:        s,
		Description: from.String() + " -> " + to.String(),
		Apply:       func() { s.Tok = to },
		Undo:        func() { s.Tok = from },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.BranchStmt{Tok: to}}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// enclosingLoop returns the nearest enclosing statement that break leaves
// if it is a loop, so that break and continue are each other's mutant, with
// the label of that loop ("" if none).
func (c *Context) enclosingLoop() (loop ast.Stmt, label string) {
	for i := 1; i <= len(c.Path); i++ {
		switch n := c.parent(i).(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			if l, ok := c.parent(i + 1).(*ast.LabeledStmt); ok {
				label = l.Label.Name
			}
			return n.(ast.Stmt), label
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
			return nil, ""
		}
	}
	return nil, ""
}

// breaks reports whether body holds a break that leaves the loop it is the
// body of: an unlabeled one outside any nested breakable statement, or one
// naming label.
func breaks(body *ast.BlockStmt, label string) bool {
	found := false
	var visit func(root ast.Node, nested bool)
	visit = func(root ast.Node, nested bool) {
		ast.Inspect(root, func(n ast.Node) bool {
			if found {
				return false
			}
			switch n := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.BranchStmt:
				if n.Tok == token.BREAK && (n.Label == nil && !nested || n.Label != nil && n.Label.Name == label) {
					found = true
				}
			case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				if !nested {
					visit(n, true)
					return false
				}
			}
			return true
		})
	}
	visit(body, false)
	return found
}

// LoopCond makes a loop run zero times: the condition of a for statement is
// forced to false (it is still evaluated, so its side effects are kept),
// and a range loop breaks before its first iteration. Forcing a condition
// to true is not a mutant: the loop would never end, and a timeout is a
// kill that says nothing.
type LoopCond struct{}

func (LoopCond) Name() string { return "loopcond" }

func (LoopCond) Sites(ctx *Context, n ast.Node) []Site {
	switch s := n.(type) {
	case *ast.ForStmt:
		if s.Cond == nil {
			return nil
		}
		cond := &s.Cond
		orig := *cond
		forced := ast.NewIdent("false")
		site := Site{
			Node:        orig,
			Description: "cond -> false",
			Apply:       func() { *cond = forced },
			Undo:        func() { *cond = orig },
			Wraps:       true,
		}
		if ctx.isBool(orig) {
			site.Schemata = func(l *Lowering) ast.Node { return l.Call("Cond", l.Expr(), forced) }
		}
		return []Site{site}
	case *ast.RangeStmt:
		body := s.Body
		if body == nil {
			return nil
		}
		orig := body.List
		broken := append([]ast.Stmt{&ast.BranchStmt{Tok: token.BREAK}}, orig...)
		return []Site{{
			Node:        body,
			Description: "range -> no iteration",
			Apply:       func() { body.List = broken },
			Undo:        func() { body.List = orig },
			Wraps:       true,
			Schemata: func(l *Lowering) ast.Node {
				guard := &ast.IfStmt{
					Cond: l.Active(),
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.BranchStmt{Tok: token.BREAK}}},
				}
				block := l.Stmt().(*ast.BlockStmt)
				return &ast.BlockStmt{
					Lbrace: block.Lbrace,
					List:   append([]ast.Stmt{guard}, block.List...),
					Rbrace: block.Rbrace,
				}
			},
		}}
	}
	return nil
}
