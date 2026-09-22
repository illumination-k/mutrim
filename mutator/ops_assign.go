package mutator

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// assignSwaps is the operator swap of a compound assignment, following the
// arithmetic and bitwise tables.
var assignSwaps = map[token.Token]token.Token{
	token.ADD_ASSIGN:     token.SUB_ASSIGN,
	token.SUB_ASSIGN:     token.ADD_ASSIGN,
	token.MUL_ASSIGN:     token.QUO_ASSIGN,
	token.QUO_ASSIGN:     token.MUL_ASSIGN,
	token.REM_ASSIGN:     token.MUL_ASSIGN,
	token.AND_ASSIGN:     token.OR_ASSIGN,
	token.OR_ASSIGN:      token.AND_ASSIGN,
	token.XOR_ASSIGN:     token.AND_ASSIGN,
	token.AND_NOT_ASSIGN: token.AND_ASSIGN,
	token.SHL_ASSIGN:     token.SHR_ASSIGN,
	token.SHR_ASSIGN:     token.SHL_ASSIGN,
}

// binaryOfAssign is the binary operator a compound assignment applies.
var binaryOfAssign = map[token.Token]token.Token{
	token.ADD_ASSIGN:     token.ADD,
	token.SUB_ASSIGN:     token.SUB,
	token.MUL_ASSIGN:     token.MUL,
	token.QUO_ASSIGN:     token.QUO,
	token.REM_ASSIGN:     token.REM,
	token.AND_ASSIGN:     token.AND,
	token.OR_ASSIGN:      token.OR,
	token.XOR_ASSIGN:     token.XOR,
	token.AND_NOT_ASSIGN: token.AND_NOT,
	token.SHL_ASSIGN:     token.SHL,
	token.SHR_ASSIGN:     token.SHR,
}

// AssignOp mutates a compound assignment: `x += y` becomes `x -= y` and,
// as a second mutant, `x = y`, which drops the accumulation. A compound
// assignment has exactly one operand on each side, so the rewrites are
// local to the statement.
type AssignOp struct{}

func (AssignOp) Name() string { return "assignop" }

func (AssignOp) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.AssignStmt)
	if !ok {
		return nil
	}
	from, ok := binaryOfAssign[s.Tok]
	if !ok {
		return nil // plain = or :=
	}
	lhs, rhs := s.Lhs[0], s.Rhs[0]

	sites := make([]Site, 0, 2)
	if swap, ok := assignSwaps[s.Tok]; ok {
		tok, to := s.Tok, binaryOfAssign[swap]
		sites = append(sites, Site{
			Node:        s,
			Pos:         s.TokPos,
			End:         s.TokPos + token.Pos(len(tok.String())),
			Description: tok.String() + " -> " + swap.String(),
			Apply:       func() { s.Tok = swap },
			Undo:        func() { s.Tok = tok },
			// `x op y` stands for the whole statement: a swap Go rejects
			// there (`s -= t` on strings) fails this check too.
			Check:    func() error { return ctx.CheckExpr(&ast.BinaryExpr{X: lhs, OpPos: s.TokPos, Op: to, Y: rhs}) },
			Schemata: assignOpSchemata(s, from, to),
		})
	}
	tok := s.Tok
	sites = append(sites, Site{
		Node:        s,
		Pos:         s.TokPos,
		End:         s.TokPos + token.Pos(len(tok.String())),
		Description: tok.String() + " -> =",
		Apply:       func() { s.Tok = token.ASSIGN },
		Undo:        func() { s.Tok = tok },
		Check:       func() error { return assignableTo(ctx, rhs, lhs) },
		Wraps:       true,
		// `if mut.Active(id) { x = y } else { x op= y }`: both forms
		// evaluate x and y once, in the same order.
		Schemata: func(l *Lowering) ast.Node {
			if name := l.Cursor.Name(); name == "Init" || name == "Post" {
				return nil // an if statement is not a simple statement
			}
			plain := &ast.AssignStmt{Lhs: s.Lhs, TokPos: s.TokPos, Tok: token.ASSIGN, Rhs: s.Rhs}
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{plain}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	})
	return sites
}

// assignOpSchemata lowers `x op= y` to `x = mut.Arith(id, x, y, op, to)`,
// reusing the binary helpers. The left operand is evaluated twice, once as
// the target and once as an argument, so a call in it is declined.
func assignOpSchemata(s *ast.AssignStmt, from, to token.Token) func(*Lowering) ast.Node {
	var fn string
	switch opClass(from) {
	case classArith:
		fn = "Arith"
		if from == token.REM || to == token.REM {
			fn = "ArithInt"
		}
	case classBitwise:
		fn = "Bit"
	case classShift:
		fn = "Shift"
	default:
		return nil
	}
	lhs := s.Lhs[0]
	if containsCall(lhs) {
		return nil
	}
	return func(l *Lowering) ast.Node {
		call := l.Call(fn, lhs, s.Rhs[0], opLit(from), opLit(to))
		return &ast.AssignStmt{Lhs: s.Lhs, TokPos: s.TokPos, Tok: token.ASSIGN, Rhs: []ast.Expr{call}}
	}
}

// Assign drops the store of an assignment: the right-hand side is still
// evaluated, but nothing is written, as if the statement were gone. Short
// variable declarations are left alone, since removing one leaves the
// names undefined.
type Assign struct{}

func (Assign) Name() string { return "assign" }

func (Assign) Sites(ctx *Context, n ast.Node) []Site {
	s, ok := n.(*ast.AssignStmt)
	if !ok || s.Tok != token.ASSIGN {
		return nil
	}
	stored := false
	for _, l := range s.Lhs {
		if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
			stored = true
		}
	}
	if !stored {
		return nil // already stores nothing
	}
	for _, r := range s.Rhs {
		if isNil(ctx, r) {
			return nil // `_ = nil` is not a valid statement
		}
	}

	blanks := make([]ast.Expr, len(s.Lhs))
	for i := range blanks {
		blanks[i] = ast.NewIdent("_")
	}
	orig := s.Lhs
	return []Site{{
		Node:        s,
		Description: "store -> dropped",
		Apply:       func() { s.Lhs = blanks },
		Undo:        func() { s.Lhs = orig },
		Wraps:       true,
		Schemata: func(l *Lowering) ast.Node {
			if name := l.Cursor.Name(); name == "Init" || name == "Post" {
				return nil // an if statement is not a simple statement
			}
			dropped := &ast.AssignStmt{Lhs: blanks, TokPos: s.TokPos, Tok: token.ASSIGN, Rhs: s.Rhs}
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: []ast.Stmt{dropped}},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}}
}

// assignableTo reports whether `lhs = rhs` type-checks.
func assignableTo(ctx *Context, rhs, lhs ast.Expr) error {
	rt, lt := ctx.Info.TypeOf(rhs), ctx.Info.TypeOf(lhs)
	if rt == nil || lt == nil {
		return fmt.Errorf("mutator: no type for %s or %s", types.ExprString(rhs), types.ExprString(lhs))
	}
	if !types.AssignableTo(rt, lt) {
		return fmt.Errorf("mutator: cannot assign %s to %s", rt, lt)
	}
	return nil
}

// isNil reports whether e is the predeclared nil.
func isNil(ctx *Context, e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && ctx.Info.Uses[id] == types.Universe.Lookup("nil")
}

// containsCall reports whether e evaluates a call, so that evaluating it
// twice could repeat a side effect.
func containsCall(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			found = true
		case *ast.UnaryExpr:
			found = n.Op == token.ARROW // a channel receive
		}
		return !found
	})
	return found
}
