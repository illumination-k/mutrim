package mutator

import (
	"fmt"
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
}

// FilterSpec is a Filter as the command line spells it: regexps, and
// comma-separated lists of patterns. A pattern may not contain a comma.
type FilterSpec struct {
	Match        string
	Files        string
	ExcludeFiles string
	ExcludeRE    string
}

// Compile parses the spec. An empty field imposes no rule, so the zero
// FilterSpec compiles to a Filter that ignores nothing.
func (s FilterSpec) Compile() (Filter, error) {
	var f Filter
	var err error
	if f.Match, err = compileRE(reasonMatch, s.Match); err != nil {
		return Filter{}, err
	}
	if f.ExcludeRE, err = compileRE(reasonExcludeRE, s.ExcludeRE); err != nil {
		return Filter{}, err
	}
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
