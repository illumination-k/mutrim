package mutator

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"strconv"
	"strings"
)

// DefaultAridCalls are the callees whose calls the built-in arid rules
// treat as arid, spelled as the globs Filter.AridCalls takes. They are
// the call classes Petrović et al. ("State of mutation testing at
// Google", ICSE-SEIP 2018; TSE 2021) found no test asserts on: logging,
// stdout writes, test helpers, monitoring counters and sleeps.
var DefaultAridCalls = []string{
	"log.*",
	"(*log.Logger).*",
	"slog.*",
	"(*slog.Logger).*",
	"(*zap.*).*",
	"(*zerolog.*).*",
	"fmt.Print*",
	"(*testing.*).*",
	"(*prometheus.*).Inc",
	"(*prometheus.*).Add",
	"(*prometheus.*).Observe",
	"(*metrics.*).Inc",
	"(*metrics.*).Add",
	"(*metrics.*).Observe",
	"time.Sleep",
}

// timeoutCalls take a duration or a deadline whose exact value no test
// observes; the built-in rules make those arguments arid, not the call.
var timeoutCalls = []string{
	"context.WithTimeout",
	"context.WithDeadline",
	"time.After",
	"time.AfterFunc",
	"time.NewTimer",
	"time.NewTicker",
	"time.Tick",
	"(*time.Timer).Reset",
	"(*time.Ticker).Reset",
}

// aridFuncs are the declarations whose whole body the built-in rules make
// arid: formatting methods and package initialisation.
var aridFuncs = map[string]bool{"String": true, "Error": true, "GoString": true}

// aridity decides which nodes of one function are arid (Petrović et al.):
// a node is arid when a rule says so, and a compound node when every one
// of its children is. A mutant inside an arid node is ignored, since no
// test can be expected to kill it.
type aridity struct {
	filter Filter
	info   *types.Info
	nodes  map[ast.Node]bool
}

// aridNodes returns the arid nodes of fn, the roots of arid subtrees
// included; nil when the filter applies no arid rule.
func (f Filter) aridNodes(info *types.Info, fn *ast.FuncDecl) map[ast.Node]bool {
	if !f.Arid && len(f.AridCalls) == 0 {
		return nil
	}
	a := &aridity{filter: f, info: info, nodes: map[ast.Node]bool{}}
	if f.Arid && aridFunc(fn) {
		a.nodes[fn.Body] = true
		return a.nodes
	}
	a.visit(fn.Body)
	return a.nodes
}

// aridFunc reports whether fn is init or a String, Error or GoString
// method.
func aridFunc(fn *ast.FuncDecl) bool {
	if fn.Recv == nil {
		return fn.Name.Name == "init"
	}
	return aridFuncs[fn.Name.Name] && fn.Type.Params.NumFields() == 0
}

// visit marks n when it is arid and reports whether it is. Every child is
// visited, so the arid subtrees of a node that is not arid are found too.
func (a *aridity) visit(n ast.Node) bool {
	if a.nodes[n] || a.rule(n) {
		a.nodes[n] = true
		return true
	}
	leaf, all := true, true
	ast.Inspect(n, func(c ast.Node) bool {
		if c == n {
			return true
		}
		if c == nil {
			return false
		}
		leaf = false
		if !a.visit(c) {
			all = false
		}
		return false
	})
	// A leaf, or a node with no children at all, is arid only by a rule.
	if !leaf && all {
		a.nodes[n] = true
	}
	return a.nodes[n]
}

// rule reports whether an expert rule makes n arid. The timeout rule marks
// the arguments of n instead, before they are visited.
func (a *aridity) rule(n ast.Node) bool {
	call, isCall := n.(*ast.CallExpr)
	if isCall && matchesGlob(a.filter.AridCalls, calleeNames(a.info, call)) {
		return true
	}
	if !a.filter.Arid {
		return false
	}
	switch n := n.(type) {
	case *ast.CallExpr:
		names := calleeNames(a.info, n)
		if matchesGlob(timeoutCalls, names) {
			for _, arg := range n.Args {
				if isTimeType(a.info.TypeOf(arg)) {
					a.nodes[arg] = true
				}
			}
		}
		return matchesGlob(DefaultAridCalls, names) || a.stdWrite(n, names) || a.unreachable(n)
	case *ast.AssignStmt:
		return blankSink(n)
	case *ast.IfStmt:
		return a.cacheLookup(n)
	}
	return false
}

// stdWrite reports whether call is fmt.Fprint* to os.Stdout or os.Stderr.
func (a *aridity) stdWrite(call *ast.CallExpr, names []string) bool {
	if len(call.Args) == 0 || !matchesGlob([]string{"fmt.Fprint*"}, names) {
		return false
	}
	sel, ok := ast.Unparen(call.Args[0]).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	v, ok := a.info.Uses[sel.Sel].(*types.Var)
	return ok && v.Pkg() != nil && v.Pkg().Path() == "os" && (v.Name() == "Stdout" || v.Name() == "Stderr")
}

// unreachable reports whether call is panic("...unreachable...").
func (a *aridity) unreachable(call *ast.CallExpr) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || len(call.Args) != 1 {
		return false
	}
	if b, isBuiltin := a.info.Uses[id].(*types.Builtin); !isBuiltin || b.Name() != "panic" {
		return false
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && strings.Contains(strings.ToLower(s), "unreachable")
}

// blankSink reports whether s only discards its values: `_ = x`.
func blankSink(s *ast.AssignStmt) bool {
	if s.Tok != token.ASSIGN {
		return false
	}
	for _, lhs := range s.Lhs {
		if id, ok := lhs.(*ast.Ident); !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

// cacheLookup reports whether s is the memoisation pattern
// `if v, ok := m[k]; ok { return ... }`: skipping it only recomputes what
// the map holds.
func (a *aridity) cacheLookup(s *ast.IfStmt) bool {
	init, ok := s.Init.(*ast.AssignStmt)
	if !ok || s.Else != nil || len(init.Lhs) != 2 || len(init.Rhs) != 1 || len(s.Body.List) != 1 {
		return false
	}
	index, isIndex := ast.Unparen(init.Rhs[0]).(*ast.IndexExpr)
	if !isIndex || !isMap(a.info.TypeOf(index.X)) {
		return false
	}
	okVar, isOkIdent := init.Lhs[1].(*ast.Ident)
	cond, isCondIdent := ast.Unparen(s.Cond).(*ast.Ident)
	if !isOkIdent || !isCondIdent || cond.Name != okVar.Name {
		return false
	}
	_, isReturn := s.Body.List[0].(*ast.ReturnStmt)
	return isReturn
}

func isMap(t types.Type) bool {
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types.Map)
	return ok
}

// isTimeType reports whether t is time.Duration or time.Time.
func isTimeType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "time" {
		return false
	}
	return named.Obj().Name() == "Duration" || named.Obj().Name() == "Time"
}

// compileAridCalls validates the callee globs of an Arid spec.
func compileAridCalls(spec string) ([]string, error) {
	globs := patternList(spec)
	for _, glob := range globs {
		if _, err := path.Match(glob, ""); err != nil {
			return nil, fmt.Errorf("mutator: %s: bad glob %q: %w", reasonArid, glob, err)
		}
	}
	return globs, nil
}
