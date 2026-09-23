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
	ID   string `json:"id"`
	Pkg  string `json:"pkg"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	// EndLine and EndCol are one past the last character of the mutated
	// span: the operator token of a binary expression, the whole node
	// otherwise. Reports that highlight the source need them.
	EndLine     int    `json:"end_line"`
	EndCol      int    `json:"end_col"`
	Func        string `json:"func"`
	Operator    string `json:"operator"`
	Description string `json:"description"`
	Viable      bool   `json:"viable"`
	// Ignored names the rule that excluded the mutant from the run, a
	// Filter rule or the inline directive that disabled the site, and is
	// empty when the mutant was kept. An ignored mutant is neither
	// embedded nor executed, and counts towards no score.
	Ignored string `json:"ignored,omitempty"`
	// Reason is the text an inline directive gave for ignoring the
	// mutant; filters give none.
	Reason string `json:"reason,omitempty"`
	// Equivalent names the static rule proving that the mutant computes
	// what the original does, so no test can kill it; empty when no rule
	// applies. Like an ignored mutant it is neither embedded nor
	// executed, and it counts towards no score.
	Equivalent string `json:"equivalent,omitempty"`
	// Class groups the mutant for the per-class score of report.json:
	// ClassErrPath, ClassConcurrency or ClassDefault.
	Class string `json:"class"`
	// Diff is the unified diff of the rewrite (Options.Diff), so a
	// reviewer or a test-writing LLM need not re-apply the mutant. It is
	// omitted for an ignored or not viable mutant.
	Diff string `json:"diff,omitempty"`

	site site
}

// site is a Site together with the information needed to derive its ID.
type site struct {
	Site
	file     *ast.File
	fn       *ast.FuncDecl
	funcName string
	astPath  string
	// stmt is the innermost statement enclosing the site other than a
	// block, the span of a stmt diff; nil when there is none.
	stmt ast.Stmt
	// arid marks a site on or inside a node the Filter's arid rules
	// cover; only the walk knows the enclosing nodes.
	arid bool
}

func (s site) position() token.Pos {
	if s.Pos.IsValid() {
		return s.Pos
	}
	return s.Node.Pos()
}

func (s site) class() string {
	if s.Class != "" {
		return s.Class
	}
	if c, ok := operatorClass[s.Operator]; ok {
		return c
	}
	return ClassDefault
}

func (s site) end() token.Pos {
	if s.End.IsValid() {
		return s.End
	}
	return s.Node.End()
}

// Options controls Generate.
type Options struct {
	// Operators to apply; nil means DefaultOperators.
	Operators []Operator
	// TypeCheck runs each site's local go/types check after the rewrite and
	// marks failing mutants as not viable.
	TypeCheck bool
	// Filter selects the sites to mutate; the zero Filter keeps all of
	// them. Rejected sites are still reported, marked Ignored.
	Filter Filter
	// Diff records Mutant.Diff with this much context; DiffNone skips it.
	Diff DiffContext
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
		sites = append(sites, collectSites(f, ctx, ops, opts.Filter)...)
	}

	d := &differ{fset: pkg.Fset, ctx: opts.Diff, files: map[*ast.File][]string{}, funcs: map[*ast.FuncDecl][]string{}}
	mutants := make([]Mutant, 0, len(sites))
	for _, s := range sites {
		pos, end := pkg.Fset.Position(s.position()), pkg.Fset.Position(s.end())
		m := Mutant{
			ID:          mutantID(pkg.PkgPath, s),
			Pkg:         pkg.PkgPath,
			File:        pos.Filename,
			Line:        pos.Line,
			Col:         pos.Column,
			EndLine:     end.Line,
			EndCol:      end.Column,
			Func:        s.funcName,
			Operator:    s.Operator,
			Description: s.Description,
			Viable:      true,
			Class:       s.class(),
			site:        s,
		}
		// A directive states the author's intent at the site, so it is
		// read before the run-wide filter; the operator's own rule is the
		// fallback.
		if m.Ignored, m.Reason = dis[s.file].find(pos.Line, s.Operator); m.Ignored == "" {
			m.Ignored = opts.Filter.ignore(&m)
		}
		if m.Ignored == "" {
			m.Ignored = s.Ignored
		}
		if m.Ignored == "" {
			m.Equivalent = s.Equivalent
		}
		if !m.Excluded() && opts.TypeCheck && s.Check != nil {
			s.Apply()
			m.Viable = s.Check() == nil
			s.Undo()
		}
		if opts.Diff != DiffNone && m.Ignored == "" && m.Viable {
			m.Diff = d.diff(m.File, s)
		}
		mutants = append(mutants, m)
	}
	return mutants
}

// Excluded reports whether the mutant is kept out of the run, ignored or
// proven equivalent, so it is never embedded nor executed.
func (m Mutant) Excluded() bool {
	return m.Ignored != "" || m.Equivalent != ""
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
func collectSites(f *ast.File, ctx *Context, ops []Operator, filter Filter) []site {
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
			fn:       fn,
			funcName: funcName(fn),
			arid:     filter.aridNodes(ctx.Info, fn),
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
	fn       *ast.FuncDecl
	funcName string
	// arid holds the arid nodes of the function; aridDepth counts the
	// enclosing ones, so that every site under an arid node is ignored.
	arid      map[ast.Node]bool
	aridDepth int

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
				fn:       w.fn,
				funcName: w.funcName,
				astPath:  astPath,
				stmt:     w.stmt(),
				// A site may sit on a child of n, like the body a branch
				// site empties, so its own node is looked up too.
				arid: w.aridDepth > 0 || w.arid[s.Node],
			})
		}
	}
	return true
}

// stmt returns the innermost statement on the walk's path other than a
// block, or nil.
func (w *walker) stmt() ast.Stmt {
	for i := len(w.nodes) - 1; i >= 0; i-- {
		if s, ok := w.nodes[i].(ast.Stmt); ok {
			if _, block := s.(*ast.BlockStmt); !block {
				return s
			}
		}
	}
	return nil
}

func (w *walker) push(n ast.Node) {
	top := len(w.counters) - 1
	idx := w.counters[top]
	w.counters[top]++
	w.counters = append(w.counters, 0)
	w.path = append(w.path, fmt.Sprintf("%T[%d]", n, idx))
	w.nodes = append(w.nodes, n)
	if w.arid[n] {
		w.aridDepth++
	}
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
	if w.arid[n] {
		w.aridDepth--
	}
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
