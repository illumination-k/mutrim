package report

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// Exported is an entry of `mutrim export-survivors`: a mutant no test
// killed, with what an external loop needs to write a test that kills it
// or to judge it equivalent (Foster et al., FSE 2025; Tian et al., ISSTA
// 2024), so that loop reads no other file. mutrim itself calls no model.
type Exported struct {
	ID          string        `json:"id"`
	Pkg         string        `json:"pkg"`
	File        string        `json:"file"`
	Line        int           `json:"line"`
	Col         int           `json:"col"`
	Func        string        `json:"func"`
	Operator    string        `json:"operator"`
	Description string        `json:"description"`
	Class       string        `json:"class"`
	Status      runner.Status `json:"status"`
	// Diff is the mutant's unified diff (gen -diff-context).
	Diff string `json:"diff,omitempty"`
	// FuncSource is the source of the mutated function, doc comment
	// included; empty when the file is not among the sources.
	FuncSource string `json:"func_source,omitempty"`
	// Tests are the rows that reach the mutant and still pass against it;
	// none for NO_COVERAGE.
	Tests []ExportedTest `json:"tests"`
}

// ExportedTest is a test reaching an Exported survivor.
type ExportedTest struct {
	// Name is the row as report.json names it (TestX, TestX/case,
	// pkg.TestX for another package's test).
	Name string `json:"name"`
	Pkg  string `json:"pkg,omitempty"`
	// Source is the test function's source (the parent's for a subtest);
	// empty when no _test.go among the sources declares it.
	Source string `json:"source,omitempty"`
}

// ExportSurvivors lists the Survivors of reports, SUSPECT_EQUIVALENT
// included whether or not the reports count it (an equivalence verdict is
// what such a mutant wants), with the sources of their function and of
// the tests reaching them read from sources (nil: only the paths in
// mutants.json are tried, and no test source is found).
func ExportSurvivors(mutants []mutator.Mutant, reports []*runner.Report, sources *Sources) []Exported {
	reaching := map[string][]runner.Test{}
	for _, r := range reports {
		for _, t := range r.Tests {
			for _, id := range t.Sites {
				reaching[id] = append(reaching[id], t)
			}
		}
	}
	src := &sourceIndex{sources: sources, files: map[string]*parsedFile{}}
	kept := survivors(mutants, reports, func(r *runner.Report, s runner.Status) bool {
		return r.Survived(s) || s == runner.SuspectEquivalent
	})
	out := make([]Exported, 0, len(kept))
	for _, m := range kept {
		e := Exported{
			ID: m.ID, Pkg: m.Pkg, File: m.File, Line: m.Line, Col: m.Col, Func: m.Func,
			Operator: m.Operator, Description: m.Description, Class: m.Class, Status: m.Status,
			Diff: m.Diff, FuncSource: src.funcSource(m.File, m.Func), Tests: []ExportedTest{},
		}
		for _, t := range reaching[m.ID] {
			fn, _, _ := strings.Cut(strings.TrimPrefix(t.Name, t.Pkg+"."), "/")
			want := t.Pkg
			if want == "" {
				want = filepath.Dir(m.File)
			}
			e.Tests = append(e.Tests, ExportedTest{Name: t.Name, Pkg: t.Pkg, Source: src.testSource(fn, want)})
		}
		out = append(out, e)
	}
	return out
}

// sourceIndex parses the source files once and finds functions in them.
type sourceIndex struct {
	sources *Sources
	files   map[string]*parsedFile
	// tests maps a test function's name to the _test.go files declaring
	// it, built on first use.
	tests map[string][]*parsedFile
}

type parsedFile struct {
	path  string
	src   string
	fset  *token.FileSet
	funcs map[string]*ast.FuncDecl // by Mutant.Func spelling
}

func (x *sourceIndex) parse(path, src string) *parsedFile {
	if p, ok := x.files[path]; ok {
		return p
	}
	p := &parsedFile{path: path, src: src, fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}}
	if f, err := parser.ParseFile(p.fset, path, src, parser.ParseComments|parser.SkipObjectResolution); err == nil {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				p.funcs[funcName(fn)] = fn
			}
		}
	}
	x.files[path] = p
	return p
}

// funcSource returns the source of function name in file, or "".
func (x *sourceIndex) funcSource(file, name string) string {
	src := x.sources.Read(file)
	if src == "" {
		return ""
	}
	return x.parse(file, src).text(name)
}

// testSource returns the source of the test function name, from the
// _test.go file whose directory shares the longest trailing path with
// want (the mutant's directory, or an extra test's import path).
func (x *sourceIndex) testSource(name, want string) string {
	if x.tests == nil {
		x.tests = map[string][]*parsedFile{}
		if x.sources != nil {
			for _, paths := range x.sources.byBase {
				for _, path := range paths {
					if !strings.HasSuffix(path, "_test.go") {
						continue
					}
					p := x.parse(path, x.sources.Read(path))
					for fn := range p.funcs {
						x.tests[fn] = append(x.tests[fn], p)
					}
				}
			}
		}
	}
	cands := x.tests[name]
	if len(cands) == 0 {
		return ""
	}
	best := slices.MaxFunc(cands, func(a, b *parsedFile) int {
		if d := sharedSuffix(filepath.Dir(a.path), want) - sharedSuffix(filepath.Dir(b.path), want); d != 0 {
			return d
		}
		return strings.Compare(b.path, a.path) // the first path on a tie
	})
	return best.text(name)
}

// text is the source of function name, doc comment included, or "".
func (p *parsedFile) text(name string) string {
	fn, ok := p.funcs[name]
	if !ok {
		return ""
	}
	start := fn.Pos()
	if fn.Doc != nil {
		start = fn.Doc.Pos()
	}
	tf := p.fset.File(fn.Pos())
	return p.src[tf.Offset(start):tf.Offset(fn.End())]
}

// funcName spells a declaration as Mutant.Func does: "Name" or
// "(*T).Name".
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}
