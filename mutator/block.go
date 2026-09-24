package mutator

import (
	"go/ast"

	"golang.org/x/tools/go/packages"
)

// Block is the head of a block of code: a function or function literal
// body, the body of an if, an else, a for or a range, a case or a select
// clause. Lower puts `mut.Reach(id)` there, a trace-only site that is never
// a mutant, so the runner learns which tests reach code that has no mutant
// site at all (straight-line calls and assignments), which site coverage
// is blind to. It is one entry of blocks.json.
type Block struct {
	// ID is a content hash like Mutant.ID, stable across edits elsewhere
	// in the file, and never equal to a mutant's.
	ID      string `json:"id"`
	Pkg     string `json:"pkg"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"end_line"`
	EndCol  int    `json:"end_col"`
	Func    string `json:"func"`

	node ast.Node // the block or clause Lower prepends mut.Reach to
	file *ast.File
}

// block is a block head the walker found, with the path its ID derives from.
type block struct {
	node    ast.Node
	astPath string
}

// Blocks lists the blocks of pkg's functions, in the files Generate
// mutates. Unlike mutants they ignore every filter: coverage of a function
// no mutant is kept for is still coverage.
func Blocks(pkg *packages.Package) []Block {
	ctx := &Context{Fset: pkg.Fset, Pkg: pkg.Types, Info: pkg.TypesInfo}
	out := []Block{}
	for _, f := range pkg.Syntax {
		if excluded(pkg, f) {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			w := newWalker(ctx, nil, f, fn, Filter{})
			ast.Inspect(fn.Body, w.visit)
			for _, b := range w.blocks {
				pos, end := pkg.Fset.Position(b.node.Pos()), pkg.Fset.Position(b.node.End())
				out = append(out, Block{
					ID:      blockID(pkg.PkgPath, w.funcName, b.astPath),
					Pkg:     pkg.PkgPath,
					File:    pos.Filename,
					Line:    pos.Line,
					Col:     pos.Column,
					EndLine: end.Line,
					EndCol:  end.Column,
					Func:    w.funcName,
					node:    b.node,
					file:    f,
				})
			}
		}
	}
	return out
}

// isBlock reports whether n, a child of parent (nil for a function's
// body), is the head of a block. The body of a switch or select holds
// clauses, not statements, so it is none; its clauses are.
func isBlock(n, parent ast.Node) bool {
	switch n.(type) {
	case *ast.CaseClause, *ast.CommClause:
		return true
	case *ast.BlockStmt:
		switch parent.(type) {
		case nil, *ast.FuncLit, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			return true
		}
	}
	return false
}

// blockID hashes a block as mutantID hashes a mutant, with an operator
// name no mutant has.
func blockID(pkgPath, funcName, astPath string) string {
	return hashID(pkgPath, funcName, astPath, "block")
}

// reach prepends `mut.Reach(id)` to the statements of n, a block head as
// the mutants embedded in it left it.
func (l *Lowering) reach(n ast.Node) {
	stmt := &ast.ExprStmt{X: l.Call("Reach")}
	switch n := n.(type) {
	case *ast.BlockStmt:
		n.List = append([]ast.Stmt{stmt}, n.List...)
	case *ast.CaseClause:
		n.Body = append([]ast.Stmt{stmt}, n.Body...)
	case *ast.CommClause:
		n.Body = append([]ast.Stmt{stmt}, n.Body...)
	}
}
