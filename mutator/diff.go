package mutator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"slices"
	"strings"
)

// DiffContext selects the code around the change that Mutant.Diff shows.
type DiffContext string

// The diff contexts: none records no diff, stmt shows the enclosing
// statement (at most diffContextLines lines of it on each side of the
// change), func the whole enclosing function.
const (
	DiffNone DiffContext = ""
	DiffStmt DiffContext = "stmt"
	DiffFunc DiffContext = "func"
)

// diffContextLines caps the context of a stmt diff, as `diff -u` does, so
// that an if or a for does not bring its whole body along.
const diffContextLines = 3

// ParseDiffContext reads a -diff-context value: "stmt", "func" or "none".
func ParseDiffContext(s string) (DiffContext, error) {
	switch s {
	case "none":
		return DiffNone, nil
	case string(DiffStmt), string(DiffFunc):
		return DiffContext(s), nil
	}
	return DiffNone, fmt.Errorf("mutator: unknown diff context %q (want stmt, func or none)", s)
}

// differ renders the unified diff of a site. Printing the whole file per
// mutant, as Source does, would dominate Generate, so only the enclosing
// function is printed, and spliced into the file printed once. A function
// that prints differently alone than in its file, as gofmt aligns
// consecutive one-line functions, falls back to the whole file.
type differ struct {
	fset  *token.FileSet
	ctx   DiffContext
	files map[*ast.File][]string
	funcs map[*ast.FuncDecl][]string
}

// diff applies s and returns the unified diff of the printed file against
// the original, or "" when the two print the same or do not print.
func (d *differ) diff(file string, s site) string {
	before, ok := d.files[s.file]
	if !ok {
		before = d.format(s.file)
		d.files[s.file] = before
	}
	fn, ok := d.funcs[s.fn]
	if !ok {
		fn = d.format(&printer.CommentedNode{Node: s.fn, Comments: s.file.Comments})
		d.funcs[s.fn] = fn
	}
	start := d.fset.Position(funcStart(s.fn)).Line - 1
	spliced := fn != nil && start+len(fn) <= len(before) && slices.Equal(fn, before[start:start+len(fn)])

	s.Apply()
	var after []string
	if spliced {
		if alone := d.format(&printer.CommentedNode{Node: s.fn, Comments: s.file.Comments}); alone != nil {
			after = slices.Concat(before[:start], alone, before[start+len(fn):])
		}
	} else {
		after = d.format(s.file)
	}
	s.Undo()
	if before == nil || after == nil {
		return ""
	}

	span := ast.Node(s.fn)
	if d.ctx == DiffStmt && s.stmt != nil {
		span = s.stmt
	}
	lo := d.fset.Position(span.Pos()).Line - 1
	hi := d.fset.Position(span.End()).Line
	return unifiedDiff(file, before, after, lo, hi, d.ctx == DiffStmt)
}

// format prints node as gofmt does, one element per line, or returns nil.
func (d *differ) format(node any) []string {
	var buf bytes.Buffer
	if err := format.Node(&buf, d.fset, node); err != nil {
		return nil
	}
	return strings.Split(buf.String(), "\n")
}

// funcStart is the position format.Node starts printing fn at.
func funcStart(fn *ast.FuncDecl) token.Pos {
	if fn.Doc != nil {
		return fn.Doc.Pos()
	}
	return fn.Pos()
}

// unifiedDiff renders the one hunk between the lines of before and after.
// The hunk spans [lo, hi) of before (zero-based), widened
// to the changed lines; capped, it keeps at most diffContextLines lines
// of context on each side.
func unifiedDiff(file string, before, after []string, lo, hi int, capped bool) string {
	p := 0
	for p < len(before) && p < len(after) && before[p] == after[p] {
		p++
	}
	if p == len(before) && p == len(after) {
		return ""
	}
	s := 0
	for s < len(before)-p && s < len(after)-p && before[len(before)-1-s] == after[len(after)-1-s] {
		s++
	}
	end := len(before) - s // one past the last removed line
	if capped {
		lo, hi = max(lo, p-diffContextLines), min(hi, end+diffContextLines)
	}
	lo, hi = min(lo, p), max(hi, end)
	grow := len(after) - len(before)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", file, file, lo+1, hi-lo, lo+1, hi-lo+grow)
	writeLines(&b, ' ', before[lo:p])
	writeLines(&b, '-', before[p:end])
	writeLines(&b, '+', after[p:end+grow])
	writeLines(&b, ' ', before[end:hi])
	return b.String()
}

func writeLines(b *strings.Builder, prefix byte, lines []string) {
	for _, l := range lines {
		b.WriteByte(prefix)
		b.WriteString(l)
		b.WriteByte('\n')
	}
}
