package mutator

import (
	"go/ast"
	"go/token"
	"strings"
)

// directivePrefix starts every inline mutrim directive. Directives are read
// from the raw comment lines, since CommentGroup.Text drops
// directive-shaped lines.
//
//	//mutrim:disable [op,...] [reason]            until //mutrim:enable
//	//mutrim:disable-next-line [op,...] [reason]  the line below
//	//mutrim:disable-func [op,...] [reason]       a function's doc comment
//	//mutrim:enable                               closes every open disable
//
// The operator list is optional and defaults to every operator, which
// "all" spells explicitly. A first word that does not name operators
// starts the reason instead, so a reason never needs quoting. Verbs this
// package does not know (//mutrim:keep, typos) are left alone: the
// directive namespace is shared with the other packages.
const directivePrefix = "//mutrim:"

// disable is one directive's effect: the inclusive line range it covers,
// the operators it suppresses (nil means every operator) and its reason.
type disable struct {
	from, to int
	ops      map[string]bool
	reason   string
}

// disables holds every disable directive of one file.
type disables []disable

// find reports whether a site of operator op on line is suppressed, and by
// which reason.
func (d disables) find(line int, op string) (reason string, ignored bool) {
	for _, r := range d {
		if line >= r.from && line <= r.to && (r.ops == nil || r.ops[op]) {
			return r.reason, true
		}
	}
	return "", false
}

// parseDisables reads the disable directives of f.
func parseDisables(fset *token.FileSet, f *ast.File) disables {
	known := map[string]bool{}
	for _, op := range DefaultOperators {
		known[op.Name()] = true
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	lastLine := fset.File(f.Package).LineCount()

	var out disables
	// disable-func covers the declaration it documents, so it is read from
	// the declarations; anywhere else the comment has no effect.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		for _, c := range fn.Doc.List {
			if verb, rest, ok := directive(c.Text); ok && verb == "disable-func" {
				d := parseDisable(rest, known)
				d.from, d.to = line(fn.Pos()), line(fn.End())
				out = append(out, d)
			}
		}
	}

	var open []int // disables still waiting for //mutrim:enable
	for _, g := range f.Comments {
		for _, c := range g.List {
			verb, rest, ok := directive(c.Text)
			if !ok {
				continue
			}
			switch verb {
			case "disable":
				d := parseDisable(rest, known)
				d.from, d.to = line(c.Pos()), lastLine
				open = append(open, len(out))
				out = append(out, d)
			case "disable-next-line":
				d := parseDisable(rest, known)
				d.from = line(c.End()) + 1
				d.to = d.from
				out = append(out, d)
			case "enable":
				for _, i := range open {
					out[i].to = line(c.Pos())
				}
				open = nil
			}
		}
	}
	return out
}

// directive splits a comment into the verb after //mutrim: and the rest.
func directive(text string) (verb, rest string, ok bool) {
	rest, ok = strings.CutPrefix(text, directivePrefix)
	if !ok {
		return "", "", false
	}
	verb, rest = cutWord(rest)
	return verb, rest, true
}

// cutWord splits s at its first space or tab, trimming the remainder.
func cutWord(s string) (word, rest string) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// parseDisable reads the optional operator list and the reason.
func parseDisable(rest string, known map[string]bool) disable {
	first, tail := cutWord(rest)
	ops, ok := operatorSet(first, known)
	if !ok {
		return disable{reason: rest}
	}
	return disable{ops: ops, reason: tail}
}

// operatorSet parses a comma-separated operator list. An empty word and
// "all" mean every operator, reported as a nil set. ok is false when the
// word names anything else, which makes it the start of the reason.
func operatorSet(word string, known map[string]bool) (ops map[string]bool, ok bool) {
	if word == "" || word == "all" {
		return nil, true
	}
	set := map[string]bool{}
	for name := range strings.SplitSeq(word, ",") {
		if !known[name] {
			return nil, false
		}
		set[name] = true
	}
	return set, true
}
