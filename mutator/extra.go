package mutator

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// ExtraOperator is the operator of the mutants GenerateExtra reads from
// outside the generator: an LLM's proposals, rewrites mined from a
// project's bug fixes (Brown et al., FSE 2017).
const ExtraOperator = "extra"

// Ignored and Equivalent rules of an extra mutant.
const (
	// reasonDuplicate ignores an extra mutant whose rewrite another mutant
	// already makes, generated or extra; Reason names that mutant.
	reasonDuplicate = "duplicate"
	// reasonIdentical proves an extra mutant equivalent whose rewrite
	// prints like the original.
	reasonIdentical = "identical"
)

// Extra is one externally proposed mutant, an entry of `gen -extra`. The
// site is found by its source text, not by an AST path, so that a
// proposal needs nothing but the source to be written.
type Extra struct {
	// File is the source file, matched by its path or a trailing part of
	// it ("calc.go", "internal/calc/calc.go").
	File string `json:"file"`
	// Func narrows the search to one function, spelled like Mutant.Func
	// ("Add", "(*Tree).Insert"); empty searches the whole file.
	Func string `json:"func,omitempty"`
	// Line and Col narrow the search to the expression or statement that
	// starts there; zero matches any.
	Line int `json:"line,omitempty"`
	Col  int `json:"col,omitempty"`
	// Original is the source text of the replaced expression or
	// statement. Spacing and line breaks need not match the file.
	Original string `json:"original"`
	// Replacement replaces it: an expression when Original and it both
	// parse as one, otherwise zero or more statements for the statement
	// Original is.
	Replacement string `json:"replacement"`
	// Description becomes Mutant.Description, e.g. the rationale an LLM
	// gave; empty is "original -> replacement".
	Description string `json:"description,omitempty"`
}

// PackageExtras splits extras into the entries naming a file of pkg and
// the rest, for the packages of a multi-package gen run.
func PackageExtras(pkg *packages.Package, extras []Extra) (mine, rest []Extra) {
	for _, e := range extras {
		if extraFile(pkg, e.File) != nil {
			mine = append(mine, e)
		} else {
			rest = append(rest, e)
		}
	}
	return mine, rest
}

// extraFile returns the mutable file of pkg that name designates, or nil.
func extraFile(pkg *packages.Package, name string) *ast.File {
	want := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./")
	abs, _ := filepath.Abs(name)
	for _, f := range pkg.Syntax {
		file := pkg.Fset.Position(f.Package).Filename
		slashed := filepath.ToSlash(file)
		if (file == abs || slashed == want || strings.HasSuffix(slashed, "/"+want)) && !excluded(pkg, f) {
			return f
		}
	}
	return nil
}

// GenerateExtra turns the extras of pkg into mutants of ExtraOperator,
// under the same directives, filter (except arid, since a proposal is
// deliberate), type check and diff as Generate's, which produced
// generated from the same syntax trees; call it before Lower. The type
// check re-checks the whole package with the rewrite in place, so a
// context-dependent failure, an unused variable, is caught too; a failing
// entry is not viable, and Reason holds the error. An entry whose rewrite
// generated or an earlier entry already makes is ignored as "duplicate".
// An entry that names no single site is returned as an error, not a
// mutant.
func GenerateExtra(pkg *packages.Package, generated []Mutant, extras []Extra, opts Options) ([]Mutant, []error) {
	op := &extraOp{pkg: pkg, funcs: map[ast.Node]string{}}
	var errs []error
	files := map[*ast.File]bool{}
	for i, e := range extras {
		en, err := parseExtra(e)
		if err == nil {
			if en.file = extraFile(pkg, e.File); en.file == nil {
				err = fmt.Errorf("no file %s in package %s", e.File, pkg.PkgPath)
			}
		}
		if err != nil {
			errs = append(errs, extraError(i, e, err))
			continue
		}
		en.index = i
		op.entries = append(op.entries, en)
		files[en.file] = true
	}

	ctx := &Context{Fset: pkg.Fset, Pkg: pkg.Types, Info: pkg.TypesInfo}
	var sites []site
	dis := map[*ast.File]disables{}
	for _, f := range pkg.Syntax {
		if !files[f] {
			continue
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				op.funcs[fn.Body] = funcName(fn)
			}
		}
		dis[f] = parseDisables(pkg.Fset, f)
		sites = append(sites, collectSites(f, ctx, []Operator{op}, Filter{})...)
	}

	found := map[*extraEntry][]site{}
	for i, s := range sites {
		found[op.order[i]] = append(found[op.order[i]], s)
	}
	b := newBuilder(pkg, opts, dis)
	d := newDedup(pkg.Fset, generated)
	mutants := make([]Mutant, 0, len(op.entries))
	for _, en := range op.entries {
		switch n := len(found[en]); {
		case n == 0:
			errs = append(errs, extraError(en.index, en.Extra, errors.New("original not found")))
			continue
		case n > 1:
			errs = append(errs, extraError(en.index, en.Extra, fmt.Errorf("original matches %d sites; give line and col", n)))
			continue
		}
		m, err := b.mutant(found[en][0])
		if err != nil {
			m.Reason = err.Error()
		}
		d.mark(&m)
		mutants = append(mutants, m)
	}
	return mutants, errs
}

func extraError(i int, e Extra, err error) error {
	return fmt.Errorf("extra %d (%s: %s): %w", i, e.File, e.Original, err)
}

// extraEntry is an Extra with its original parsed.
type extraEntry struct {
	Extra
	index int
	file  *ast.File
	// orig is the parsed original, whose node type a site must have;
	// text its canonical rendering.
	orig ast.Node
	text string
	expr bool
	// key is the canonical replacement, which identifies the mutant.
	key         string
	description string
}

func parseExtra(e Extra) (*extraEntry, error) {
	en := &extraEntry{Extra: e}
	fset := token.NewFileSet()
	origExpr, oerr := parser.ParseExprFrom(fset, "", e.Original, parser.SkipObjectResolution)
	replExpr, rerr := parser.ParseExpr(e.Replacement)
	replText := ""
	if oerr == nil && rerr == nil {
		en.orig, en.expr = origExpr, true
		en.key = canonical(token.NewFileSet(), replExpr)
		replText = en.key
	} else {
		stmts, err := parseStmts(fset, e.Original)
		if err != nil {
			return nil, fmt.Errorf("original: %w", err)
		}
		if len(stmts) != 1 {
			return nil, fmt.Errorf("original is %d statements, want one expression or statement", len(stmts))
		}
		repl, err := parseStmts(token.NewFileSet(), e.Replacement)
		if err != nil {
			return nil, fmt.Errorf("replacement: %w", err)
		}
		en.orig = stmts[0]
		en.key = canonical(token.NewFileSet(), &ast.BlockStmt{List: repl})
		replText = strings.TrimSuffix(strings.TrimPrefix(en.key, "{"), "}")
		if replText == "" {
			replText = "removed"
		}
	}
	en.text = canonical(fset, en.orig)
	en.description = cmp.Or(e.Description, en.text+" -> "+replText)
	return en, nil
}

// parseStmts parses src as the body of a function.
func parseStmts(fset *token.FileSet, src string) ([]ast.Stmt, error) {
	f, err := parser.ParseFile(fset, "", "package p; func _() {\n"+src+"\n}", parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return f.Decls[0].(*ast.FuncDecl).Body.List, nil
}

// replacement parses the entry's replacement afresh for one site, without
// positions, so that it prints where it is placed.
func (en *extraEntry) replacement() (ast.Expr, []ast.Stmt) {
	if en.expr {
		e, _ := parser.ParseExpr(en.Replacement)
		stripPos(e)
		return e, nil
	}
	stmts, _ := parseStmts(token.NewFileSet(), en.Replacement)
	for _, s := range stmts {
		stripPos(s)
	}
	return nil, stmts
}

// extraOp is the operator GenerateExtra walks the files with: a node is a
// site of every entry whose original it is. order records the entry of
// each site returned, in the order the walk collects them.
type extraOp struct {
	pkg     *packages.Package
	entries []*extraEntry
	funcs   map[ast.Node]string // function bodies to the function's name
	order   []*extraEntry
}

func (*extraOp) Name() string { return ExtraOperator }

func (o *extraOp) Sites(ctx *Context, n ast.Node) []Site {
	parent := ctx.parent(1)
	if parent == nil {
		return nil // a function body, which is no expression nor slotted statement
	}
	var out []Site
	text := ""
	for _, en := range o.entries {
		if !o.at(ctx, en, n) {
			continue
		}
		if text == "" {
			text = canonical(ctx.Fset, n)
		}
		if text != en.text {
			continue
		}
		var s Site
		if en.expr {
			s = o.exprSite(ctx, en, n.(ast.Expr), parent)
		} else {
			s = o.stmtSite(en, n.(ast.Stmt), parent)
		}
		out = append(out, s)
		o.order = append(o.order, en)
	}
	return out
}

// at reports whether n may be the site of en, save for its text: the
// file, function, position and kind of node match, and an expression is
// a value.
func (o *extraOp) at(ctx *Context, en *extraEntry, n ast.Node) bool {
	if reflect.TypeOf(n) != reflect.TypeOf(en.orig) {
		return false
	}
	pos := ctx.Fset.Position(n.Pos())
	if pos.Filename != ctx.Fset.Position(en.file.Package).Filename ||
		en.Func != "" && o.funcs[ctx.Path[0]] != en.Func ||
		en.Line != 0 && pos.Line != en.Line ||
		en.Col != 0 && pos.Column != en.Col {
		return false
	}
	if e, ok := n.(ast.Expr); ok {
		return ctx.Info.Types[e].IsValue()
	}
	return ctx.stmtSlot(n.(ast.Stmt)) != nil
}

// exprSite replaces an expression. Schemata spell it
// `func() T { if mut.Active(id) { return repl }; return orig }()`, so only
// one of the two is evaluated; the expression must then be a value of a
// type the file can name, not one that must be addressable or constant.
func (o *extraOp) exprSite(ctx *Context, en *extraEntry, e ast.Expr, parent ast.Node) Site {
	repl, _ := en.replacement()
	t, pos := resultType(ctx.Info.TypeOf(e)), e.Pos()
	typ, lowerErr := exprLowering(ctx, en.file, e, t)
	return Site{
		Node:        e,
		Description: en.description,
		Apply:       func() { replaceChild(parent, e, repl) },
		Undo:        func() { replaceChild(parent, repl, e) },
		Check: func() error {
			if lowerErr != nil {
				return lowerErr
			}
			info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
			if err := types.CheckExpr(ctx.Fset, ctx.Pkg, pos, repl, info); err != nil {
				return err
			}
			if got := info.Types[repl].Type; !types.AssignableTo(got, t) {
				return fmt.Errorf("replacement has type %s, want %s", got, t)
			}
			return recheck(o.pkg)
		},
		Wraps: true,
		Schemata: func(l *Lowering) ast.Node {
			if lowerErr != nil {
				return nil
			}
			result, _ := parser.ParseExpr(typ)
			stripPos(result)
			return &ast.CallExpr{Fun: &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}, Results: &ast.FieldList{List: []*ast.Field{{Type: result}}}},
				Body: &ast.BlockStmt{List: []ast.Stmt{
					&ast.IfStmt{Cond: l.Active(), Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{repl}}}}},
					&ast.ReturnStmt{Results: []ast.Expr{l.Expr()}},
				}},
			}}
		},
	}
}

// exprLowering spells the type of e for its schemata closure, or says why
// the closure cannot stand where e does.
func exprLowering(ctx *Context, file *ast.File, e ast.Expr, t types.Type) (string, error) {
	tv := ctx.Info.Types[e]
	if _, tuple := t.(*types.Tuple); tuple {
		return "", errors.New("mutator: a multi-value expression cannot be replaced")
	}
	if isUntyped(t) {
		return "", errors.New("mutator: an untyped constant cannot be replaced")
	}
	if tv.Value != nil && (ctx.inConstDecl() || inArrayLen(ctx, e)) {
		return "", errors.New("mutator: a constant is required here")
	}
	if ctx.callsRecover(e) {
		return "", errors.New("mutator: moving recover() into a closure changes it")
	}
	if addressed(ctx, e) {
		return "", errors.New("mutator: the expression is assigned to or must be addressable")
	}
	typ := types.TypeString(t, func(p *types.Package) string {
		if p == ctx.Pkg {
			return ""
		}
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == p.Path() {
				if imp.Name == nil {
					return p.Name()
				}
				return imp.Name.Name
			}
		}
		return p.Path() // unresolvable: checkSpelled rejects it
	})
	if err := ctx.checkSpelled("(*"+typ+")(nil)", e.Pos(), types.NewPointer(t)); err != nil {
		return "", fmt.Errorf("mutator: type %s cannot be spelled here: %w", t, err)
	}
	return typ, nil
}

// resultType is the type the schemata closure of an expression of type t
// returns: t, or bool for an untyped boolean, which a comparison in a
// condition stays.
func resultType(t types.Type) types.Type {
	if b, ok := t.(*types.Basic); ok && b.Kind() == types.UntypedBool {
		return types.Typ[types.Bool]
	}
	return t
}

// inArrayLen reports whether e is the length of an array type.
func inArrayLen(ctx *Context, e ast.Expr) bool {
	a, ok := ctx.parent(1).(*ast.ArrayType)
	return ok && a.Len == e
}

// addressed reports whether e stands where a call's result cannot: it is
// assigned to, its address is taken, or it is the operand of a selector
// or an array index, which may need it addressable.
func addressed(ctx *Context, e ast.Expr) bool {
	switch p := ctx.parent(1).(type) {
	case *ast.UnaryExpr:
		return p.Op == token.AND
	case *ast.AssignStmt:
		return slices.Contains(p.Lhs, e)
	case *ast.IncDecStmt:
		return true
	case *ast.RangeStmt:
		return p.Key == e || p.Value == e
	case *ast.SelectorExpr:
		switch ctx.Info.TypeOf(e).Underlying().(type) {
		case *types.Pointer, *types.Interface:
			return false
		}
		return p.X == e
	case *ast.IndexExpr, *ast.SliceExpr:
		_, array := ctx.Info.TypeOf(e).Underlying().(*types.Array)
		return array
	}
	return false
}

// stmtSite replaces a statement. Schemata spell it
// `if mut.Active(id) { repl } else { orig }`, so the statement must not
// declare anything the statements after it use, nor carry a label.
func (o *extraOp) stmtSite(en *extraEntry, s ast.Stmt, parent ast.Node) Site {
	_, repl := en.replacement()
	var applied ast.Stmt = &ast.BlockStmt{List: repl}
	if len(repl) == 1 && !declares(repl[0]) {
		applied = repl[0]
	}
	var lowerErr error
	switch {
	case declares(s):
		lowerErr = errors.New("mutator: a declaring statement cannot be replaced")
	case isLabeled(parent) || isLabeled(s):
		lowerErr = errors.New("mutator: a labeled statement cannot be replaced")
	case isFallthrough(s):
		lowerErr = errors.New("mutator: a fallthrough cannot be replaced")
	}
	return Site{
		Node:        s,
		Description: en.description,
		Apply:       func() { replaceChild(parent, s, applied) },
		Undo:        func() { replaceChild(parent, applied, s) },
		Check: func() error {
			if lowerErr != nil {
				return lowerErr
			}
			return recheck(o.pkg)
		},
		Wraps: true,
		Schemata: func(l *Lowering) ast.Node {
			if lowerErr != nil {
				return nil
			}
			return &ast.IfStmt{
				Cond: l.Active(),
				Body: &ast.BlockStmt{List: repl},
				Else: &ast.BlockStmt{List: []ast.Stmt{l.Stmt()}},
			}
		},
	}
}

func declares(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.AssignStmt:
		return s.Tok == token.DEFINE
	case *ast.DeclStmt:
		return true
	}
	return false
}

func isLabeled(n ast.Node) bool {
	_, ok := n.(*ast.LabeledStmt)
	return ok
}

func isFallthrough(s ast.Stmt) bool {
	b, ok := s.(*ast.BranchStmt)
	return ok && b.Tok == token.FALLTHROUGH
}

// replaceChild replaces the child old of parent with repl.
func replaceChild(parent, old, repl ast.Node) {
	astutil.Apply(parent, func(c *astutil.Cursor) bool {
		if c.Node() == old {
			c.Replace(repl)
			return false
		}
		return true
	}, nil)
}

// recheck type-checks the package's syntax trees as they are now, against
// the dependencies its first check imported.
func recheck(pkg *packages.Package) error {
	imports := map[string]*types.Package{"unsafe": types.Unsafe}
	for _, p := range pkg.Types.Imports() {
		imports[p.Path()] = p
	}
	conf := types.Config{
		GoVersion:   pkg.Types.GoVersion(),
		FakeImportC: true,
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if p, ok := imports[path]; ok {
				return p, nil
			}
			return nil, fmt.Errorf("mutator: %s was not imported by the first check", path)
		}),
	}
	_, err := conf.Check(pkg.PkgPath, pkg.Fset, pkg.Syntax, nil)
	return err
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// stripPos zeroes every position under n.
func stripPos(n ast.Node) {
	posType := reflect.TypeFor[token.Pos]()
	ast.Inspect(n, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		v := reflect.ValueOf(n).Elem()
		for _, f := range v.Fields() {
			if f.Type() == posType {
				f.SetInt(0)
			}
		}
		return true
	})
}

// canonical renders n on one line with the spacing gofmt would choose
// dropped, so that source text compares regardless of layout: a space is
// kept only between two word characters, and a trailing comma before a
// closing bracket is dropped.
func canonical(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, n); err != nil {
		return ""
	}
	var out []byte
	space := false
	for _, c := range buf.Bytes() {
		if c == ' ' || c == '\t' || c == '\n' {
			space = true
			continue
		}
		if space && len(out) > 0 && isWord(out[len(out)-1]) && isWord(c) {
			out = append(out, ' ')
		}
		space = false
		if (c == '}' || c == ')' || c == ']') && len(out) > 0 && out[len(out)-1] == ',' {
			out = out[:len(out)-1]
		}
		out = append(out, c)
	}
	return string(out)
}

func isWord(c byte) bool {
	return c == '_' || c == '.' || c == '"' || c == '\'' || c == '`' ||
		'0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

// dedup finds the extra mutants whose rewrite another mutant makes: the
// function prints the same with either applied.
type dedup struct {
	fset      *token.FileSet
	generated map[*ast.FuncDecl][]Mutant
	// seen maps a function's canonical text to the mutant making it,
	// filled per function on first use; orig is the unmutated text.
	seen map[*ast.FuncDecl]map[string]string
	orig map[*ast.FuncDecl]string
}

func newDedup(fset *token.FileSet, generated []Mutant) *dedup {
	d := &dedup{fset: fset, generated: map[*ast.FuncDecl][]Mutant{}, seen: map[*ast.FuncDecl]map[string]string{}, orig: map[*ast.FuncDecl]string{}}
	for _, m := range generated {
		if m.Ignored == "" && m.Viable && m.site.fn != nil {
			d.generated[m.site.fn] = append(d.generated[m.site.fn], m)
		}
	}
	return d
}

// mark ignores m as a duplicate of a mutant seen before, or records it,
// and marks it equivalent when its rewrite changes nothing.
func (d *dedup) mark(m *Mutant) {
	if m.Ignored != "" || !m.Viable {
		return
	}
	fn := m.site.fn
	seen, ok := d.seen[fn]
	if !ok {
		seen = map[string]string{}
		for _, g := range d.generated[fn] {
			if text := d.applied(g.site); text != "" {
				seen[text] = cmp.Or(seen[text], g.ID)
			}
		}
		d.seen[fn], d.orig[fn] = seen, canonical(d.fset, fn)
	}
	text := d.applied(m.site)
	switch {
	case text == d.orig[fn]:
		m.Equivalent = reasonIdentical
	case seen[text] != "":
		m.Ignored, m.Reason, m.Equivalent, m.Diff = reasonDuplicate, "of "+seen[text], "", ""
	default:
		seen[text] = m.ID
	}
}

// applied is the canonical text of the site's function with it applied.
func (d *dedup) applied(s site) string {
	s.Apply()
	defer s.Undo()
	return canonical(d.fset, s.fn)
}
