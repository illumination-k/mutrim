// Package mutator rewrites Go ASTs to produce mutants and filters them with
// go/types so that only mutants which still compile are reported.
//
// The package is independent of Bazel: it loads packages with go/packages and
// can emit a go build -overlay file so a single mutant runs under `go test`.
package mutator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Mutant is one entry of mutants.json.
type Mutant struct {
	ID          string `json:"id"`
	Pkg         string `json:"pkg"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Col         int    `json:"col"`
	Func        string `json:"func"`
	Operator    string `json:"operator"`
	Description string `json:"description"`
	Viable      bool   `json:"viable"`
	// Ignored means an inline //mutrim:disable directive suppressed the
	// site: the mutant is reported but never built or executed.
	Ignored bool `json:"ignored,omitempty"`
	// Reason is the text the directive gave for ignoring the mutant.
	Reason string `json:"reason,omitempty"`

	site site
}

// site is a Site together with the information needed to derive its ID.
type site struct {
	Site
	file     *ast.File
	funcName string
	astPath  string
}

func (s site) position() token.Pos {
	if s.Pos.IsValid() {
		return s.Pos
	}
	return s.Node.Pos()
}

// Options controls Generate.
type Options struct {
	// Operators to apply; nil means DefaultOperators.
	Operators []Operator
	// TypeCheck runs each site's local go/types check after the rewrite and
	// marks failing mutants as not viable.
	TypeCheck bool
}

// Generate enumerates the mutants of pkg.
func Generate(pkg *packages.Package, opts Options) []Mutant {
	ops := opts.Operators
	if ops == nil {
		ops = DefaultOperators
	}

	ctx := &Context{Fset: pkg.Fset, Pkg: pkg.Types, Info: pkg.TypesInfo}
	var sites []site
	dis := map[*ast.File]disables{}
	for _, f := range pkg.Syntax {
		if excluded(pkg, f) {
			continue
		}
		dis[f] = parseDisables(pkg.Fset, f)
		sites = append(sites, collectSites(f, ctx, ops)...)
	}

	mutants := make([]Mutant, 0, len(sites))
	for _, s := range sites {
		pos := pkg.Fset.Position(s.position())
		m := Mutant{
			ID:          mutantID(pkg.PkgPath, s),
			Pkg:         pkg.PkgPath,
			File:        pos.Filename,
			Line:        pos.Line,
			Col:         pos.Column,
			Func:        s.funcName,
			Operator:    s.Operator,
			Description: s.Description,
			Viable:      true,
			site:        s,
		}
		switch reason, ignored := dis[s.file].find(pos.Line, s.Operator); {
		case ignored:
			m.Ignored, m.Reason = true, reason
		case opts.TypeCheck && s.Check != nil:
			s.Apply()
			m.Viable = s.Check() == nil
			s.Undo()
		}
		mutants = append(mutants, m)
	}
	return mutants
}

// mutantID hashes everything that identifies a mutant except its source
// position, so that edits elsewhere in the file keep the ID stable.
func mutantID(pkgPath string, s site) string {
	h := sha256.New()
	for _, part := range []string{pkgPath, s.funcName, s.astPath, s.Operator, s.Description} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// collectSites walks every top-level function body in f and asks each
// operator for the sites it can mutate.
func collectSites(f *ast.File, ctx *Context, ops []Operator) []site {
	var out []site
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		w := &walker{
			ctx:      ctx,
			ops:      ops,
			file:     f,
			funcName: funcName(fn),
			sigs:     []*types.Signature{signatureOf(ctx.Info, fn.Name)},
			counters: []int{0},
		}
		ast.Inspect(fn.Body, w.visit)
		out = append(out, w.out...)
	}
	return out
}

type walker struct {
	ctx      *Context
	ops      []Operator
	file     *ast.File
	funcName string

	nodes    []ast.Node
	path     []string
	counters []int
	sigs     []*types.Signature
	out      []site
}

func (w *walker) visit(n ast.Node) bool {
	if n == nil {
		w.pop()
		return false
	}
	w.push(n)

	ctx := *w.ctx
	ctx.Sig = w.sigs[len(w.sigs)-1]
	ctx.Path = w.nodes[:len(w.nodes)-1]
	astPath := strings.Join(w.path, "/")
	for _, op := range w.ops {
		for _, s := range op.Sites(&ctx, n) {
			s.Operator = op.Name()
			w.out = append(w.out, site{
				Site:     s,
				file:     w.file,
				funcName: w.funcName,
				astPath:  astPath,
			})
		}
	}
	return true
}

func (w *walker) push(n ast.Node) {
	top := len(w.counters) - 1
	idx := w.counters[top]
	w.counters[top]++
	w.counters = append(w.counters, 0)
	w.path = append(w.path, fmt.Sprintf("%T[%d]", n, idx))
	w.nodes = append(w.nodes, n)
	if lit, ok := n.(*ast.FuncLit); ok {
		sig, _ := w.ctx.Info.TypeOf(lit).(*types.Signature)
		w.sigs = append(w.sigs, sig)
	}
}

func (w *walker) pop() {
	n := w.nodes[len(w.nodes)-1]
	w.nodes = w.nodes[:len(w.nodes)-1]
	w.path = w.path[:len(w.path)-1]
	w.counters = w.counters[:len(w.counters)-1]
	if _, ok := n.(*ast.FuncLit); ok {
		w.sigs = w.sigs[:len(w.sigs)-1]
	}
}

func signatureOf(info *types.Info, name *ast.Ident) *types.Signature {
	fn, ok := info.Defs[name].(*types.Func)
	if !ok {
		return nil
	}
	sig, _ := fn.Type().(*types.Signature)
	return sig
}

// funcName renders a declaration as "Name" or "(*T).Name".
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

// excluded reports whether f must not be mutated: generated code, protobuf
// output, mocks, cgo, and anything outside the package's own GoFiles
// (which is how cgo-processed files show up).
func excluded(pkg *packages.Package, f *ast.File) bool {
	name := pkg.Fset.Position(f.Package).Filename
	base := filepath.Base(name)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, ".pb.go"),
		strings.HasPrefix(base, "mock_"),
		ast.IsGenerated(f):
		return true
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"C"` {
			return true
		}
	}
	for _, g := range pkg.GoFiles {
		if g == name {
			return false
		}
	}
	return true
}
