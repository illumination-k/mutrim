package runner

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Diff is the set of lines a unified diff adds, per file. It scopes a run
// to what a pull request touched: a mutant it does not cover is reported
// Skipped and counts towards no score.
//
// Only the new side is recorded. A removed line holds no mutant, and a
// context line is code the diff left alone, so a mutant is kept only when
// its span overlaps an added line. Hunks of a _test.go file are dropped,
// since a test file holds no mutant either.
//
// The diff must be taken against the sources the test binary was built
// from; mutrim does not check that, and a diff of a different tree simply
// selects the wrong lines.
type Diff struct {
	// files maps a new-side path to its added lines, as ranges in
	// ascending order.
	files map[string][]lineRange
}

// lineRange is a closed range of new-side line numbers.
type lineRange struct{ start, end int }

// hunkHeader matches "@@ -old,count +new,count @@", whose new-side start
// and count drive the line numbering of the hunk body.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ReadDiff parses the unified diff stored in path.
func ReadDiff(path string) (*Diff, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	d, err := ParseDiff(f)
	if err != nil {
		return nil, fmt.Errorf("runner: parse %s: %w", path, err)
	}
	return d, nil
}

// ParseDiff reads a unified diff, as `git diff` or `diff -u` writes it.
// Anything it does not understand (commit headers, mode changes, binary
// files) is skipped, so a whole `git format-patch` mail can be passed.
func ParseDiff(r io.Reader) (*Diff, error) {
	d := &Diff{files: map[string][]lineRange{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20) // one long line of a generated file must not end the parse
	var file string
	var line, left int
	for sc.Scan() {
		text := sc.Text()
		// Inside a hunk the counted body wins: an added line of a diff of a
		// diff starts with "+++" and is not a file header.
		if left > 0 {
			switch {
			case strings.HasPrefix(text, "+"):
				d.add(file, line)
				line++
				left--
			case strings.HasPrefix(text, "-"):
				// removed from the old side; the new side does not advance
			case strings.HasPrefix(text, `\`):
				// "\ No newline at end of file"
			default: // context, which some tools write as an empty line
				line++
				left--
			}
			continue
		}
		switch {
		case strings.HasPrefix(text, "+++ "):
			file = newPath(text[len("+++ "):])
		case strings.HasPrefix(text, "@@"):
			m := hunkHeader.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			line, _ = strconv.Atoi(m[1]) // the regexp matched \d+
			left = 1
			if m[2] != "" {
				left, _ = strconv.Atoi(m[2])
			}
		}
	}
	return d, sc.Err()
}

// newPath is the file a "+++" header names: its path without the
// tab-separated timestamp and the leading a/ or b/ of `git diff`. A
// deletion (/dev/null) and a test file yield "", which drops the hunks
// that follow.
func newPath(s string) string {
	if unquoted, err := strconv.Unquote(s); strings.HasPrefix(s, `"`) && err == nil {
		s = unquoted
	}
	s, _, _ = strings.Cut(s, "\t")
	s = filepath.ToSlash(strings.TrimSpace(s))
	if s == "/dev/null" || !strings.HasSuffix(s, ".go") || strings.HasSuffix(s, "_test.go") {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return path.Clean(s)
}

// add records line as added in file, extending the last range when the
// line continues it. Hunks arrive in ascending order, so the ranges stay
// sorted.
func (d *Diff) add(file string, line int) {
	if file == "" {
		return
	}
	rs := d.files[file]
	if n := len(rs); n > 0 && rs[n-1].end == line-1 {
		rs[n-1].end = line
		return
	}
	d.files[file] = append(rs, lineRange{start: line, end: line})
}

// Touches reports whether the mutant span starting at line and ending at
// endLine (zero when unknown) overlaps a line the diff adds to file. A
// nil Diff scopes nothing and touches everything.
func (d *Diff) Touches(file string, line, endLine int) bool {
	if d == nil {
		return true
	}
	end := max(line, endLine)
	for _, r := range d.ranges(file) {
		if r.start <= end && line <= r.end {
			return true
		}
	}
	return false
}

// ranges are the added lines of file. A mutant's path is absolute under
// `go list` and execroot-relative under Bazel, while a diff is relative
// to the repository root, so the two are matched by path suffix; the
// longest matching path of the diff wins, which keeps the choice
// deterministic when several are suffixes of the mutant's.
func (d *Diff) ranges(file string) []lineRange {
	key := path.Clean(filepath.ToSlash(file))
	if rs, ok := d.files[key]; ok {
		return rs
	}
	var best string
	for name := range d.files {
		if len(name) > len(best) && (strings.HasSuffix(key, "/"+name) || strings.HasSuffix(name, "/"+key)) {
			best = name
		}
	}
	return d.files[best]
}
