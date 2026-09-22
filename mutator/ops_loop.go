package mutator

import (
	"go/ast"
	"go/token"
)

// LoopCtrl swaps break and continue. Only unlabeled statements whose
// nearest enclosing breakable statement is a loop are sites: a break in a
// switch or select leaves that statement instead, so swapping it would
// change which statement the mutant is about.
type LoopCtrl struct{}

func (LoopCtrl) Name() string { return "loopctrl" }

func (LoopCtrl) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.BranchStmt)
	if !ok || s.Label != nil || (s.Tok != token.BREAK && s.Tok != token.CONTINUE) {
		return nil
	}
	if !ctx.inLoopBody() {
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

// inLoopBody reports whether the nearest enclosing statement that break
// leaves is a loop, so that break and continue are each other's mutant.
func (c *Context) inLoopBody() bool {
	for i := 1; i <= len(c.Path); i++ {
		switch c.parent(i).(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
			return false
		}
	}
	return false
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
