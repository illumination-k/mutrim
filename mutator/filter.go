package mutator

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Reasons a Filter gives for ignoring a mutant; they name the rule that
// rejected it, spelled as the `mutrim gen` flag that sets it.
const (
	reasonMatch        = "match"
	reasonFiles        = "files"
	reasonExcludeFiles = "exclude-files"
	reasonExcludeRE    = "exclude-re"
	reasonArid         = "arid"
)

// Filter narrows which sites are mutated. A site the filter rejects still
// yields a Mutant, with Ignored naming the rule that rejected it, so the
// counts of a filtered run stay comparable with an unfiltered one and the
// suppression is visible.
type Filter struct {
	// Match keeps only the functions whose name matches, in the
	// "(*T).Name" form of Mutant.Func.
	Match *regexp.Regexp
	// Files keeps only the files matching one of these path.Match globs.
	Files []string
	// ExcludeFiles drops the files matching any of these regexps.
	ExcludeFiles []*regexp.Regexp
	// ExcludeRE drops the mutants whose "func operator: description"
	// matches, so a mutant can be selected by its rewrite and not only by
	// its location.
	ExcludeRE *regexp.Regexp
	// Arid applies the built-in arid rules: a node is arid when a rule
	// says no test can observe it (a call of DefaultAridCalls, fmt.Fprint*
	// to os.Stdout or os.Stderr, the duration of a timeout, a `_ = x`
	// sink, a map-cache lookup, panic("unreachable"), the body of init or
	// of a String, Error or GoString method), and a compound node when
	// all of its children are. Every mutant inside an arid node is
	// ignored (Petrović et al., ICSE-SEIP 2018).
	Arid bool
	// AridCalls extends the rules with callees, as path.Match globs: a
	// matching call and its arguments are arid. A callee is spelled as
	// go/types names it, with and without the package path: "log.Printf"
	// and "(*slog.Logger).Info", or "(*log/slog.Logger).Info".
	AridCalls []string
}

// FilterSpec is a Filter as the command line spells it: regexps, and
// comma-separated lists of patterns. A pattern may not contain a comma.
type FilterSpec struct {
	Match        string
	Files        string
	ExcludeFiles string
	ExcludeRE    string
	// Arid lists callee globs that extend the built-in arid rules.
	Arid string
	// NoArid turns the built-in arid rules off; Arid still applies.
	NoArid bool
}

// Compile parses the spec. An empty field imposes no rule, and the
// built-in arid rules apply unless NoArid: the zero FilterSpec is the
// command line's defaults, not the zero Filter.
func (s FilterSpec) Compile() (Filter, error) {
	var f Filter
	var err error
	if f.Match, err = compileRE(reasonMatch, s.Match); err != nil {
		return Filter{}, err
	}
	if f.ExcludeRE, err = compileRE(reasonExcludeRE, s.ExcludeRE); err != nil {
		return Filter{}, err
	}
	if f.AridCalls, err = compileAridCalls(s.Arid); err != nil {
		return Filter{}, err
	}
	f.Arid = !s.NoArid
	for _, glob := range patternList(s.Files) {
		if _, err := path.Match(glob, ""); err != nil {
			return Filter{}, fmt.Errorf("mutator: %s: bad glob %q: %w", reasonFiles, glob, err)
		}
		f.Files = append(f.Files, glob)
	}
	for _, pattern := range patternList(s.ExcludeFiles) {
		re, err := compileRE(reasonExcludeFiles, pattern)
		if err != nil {
			return Filter{}, err
		}
		f.ExcludeFiles = append(f.ExcludeFiles, re)
	}
	return f, nil
}

func compileRE(rule, pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("mutator: %s: %w", rule, err)
	}
	return re, nil
}

// patternList splits a comma-separated list, dropping empty entries.
func patternList(spec string) []string {
	var out []string
	for entry := range strings.SplitSeq(spec, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// ignore returns the rule rejecting m, or "" when the filter keeps it.
func (f Filter) ignore(m *Mutant) string {
	// Aridity was decided while walking, where the enclosing nodes are
	// known; ignore only reports it.
	if m.site.arid {
		return reasonArid
	}
	if f.Match != nil && !f.Match.MatchString(m.Func) {
		return reasonMatch
	}
	if f.ExcludeRE != nil && f.ExcludeRE.MatchString(m.text()) {
		return reasonExcludeRE
	}
	if len(f.Files) == 0 && len(f.ExcludeFiles) == 0 {
		return ""
	}
	paths := filePaths(m.File)
	if len(f.Files) > 0 && !matchesGlob(f.Files, paths) {
		return reasonFiles
	}
	for _, re := range f.ExcludeFiles {
		if matchesAny(re.MatchString, paths) {
			return reasonExcludeFiles
		}
	}
	return ""
}

// text is the one-line rendering of the mutant that ExcludeRE is matched
// against, e.g. `(*Tree).Insert relational: < -> <=`.
func (m Mutant) text() string {
	return m.Func + " " + m.Operator + ": " + m.Description
}

// calleeNames lists the spellings of the function call invokes that an
// arid call glob is matched against: the name go/types gives it
// ("log/slog.Info", "(*log/slog.Logger).Info") and the same with the
// package path shortened to the package name ("slog.Info"), so a pattern
// written the way the source reads works too. A callee that is not a
// declared function (a builtin, a conversion, a function value) has no
// name and is never matched.
func calleeNames(info *types.Info, call *ast.CallExpr) []string {
	var id *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	case *ast.IndexExpr: // an explicitly instantiated generic function
		return calleeNames(info, &ast.CallExpr{Fun: fun.X})
	case *ast.IndexListExpr:
		return calleeNames(info, &ast.CallExpr{Fun: fun.X})
	default:
		return nil
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil
	}
	full := fn.FullName()
	short := strings.ReplaceAll(full, fn.Pkg().Path()+".", fn.Pkg().Name()+".")
	if short == full {
		return []string{full}
	}
	return []string{full, short}
}

func matchesGlob(globs, paths []string) bool {
	for _, glob := range globs {
		// Compile validated the globs, so ErrBadPattern cannot occur.
		if matchesAny(func(p string) bool { ok, _ := path.Match(glob, p); return ok }, paths) {
			return true
		}
	}
	return false
}

func matchesAny(match func(string) bool, paths []string) bool {
	for _, p := range paths {
		if match(p) {
			return true
		}
	}
	return false
}

// workDir is read once; a file filter is matched against paths relative
// to it as well as against the paths the loader reports.
var workDir = sync.OnceValue(func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
})

// filePaths lists the spellings of file that a file filter is matched
// against, all with forward slashes: the path as reported (absolute under
// go/packages, relative to the execution root under Bazel), its path
// relative to the working directory, and its base name. A pattern written
// the way the source tree reads therefore works in both modes.
func filePaths(file string) []string {
	slash := filepath.ToSlash(file)
	paths := []string{slash, path.Base(slash)}
	if wd := workDir(); wd != "" {
		if rel, err := filepath.Rel(wd, file); err == nil && !strings.HasPrefix(rel, "..") {
			paths = append(paths, filepath.ToSlash(rel))
		}
	}
	return paths
}
