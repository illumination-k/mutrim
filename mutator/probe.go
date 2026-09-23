package mutator

import (
	"go/ast"
	"maps"
	"slices"

	"golang.org/x/tools/go/ast/astutil"
)

// anchor is where the probe of a mutant goes: at the start of the
// statement list of into, or right before the statement before, in the
// list holding it.
type anchor struct {
	into   ast.Node
	before ast.Stmt
}

// probeAnchors finds the anchor of each mutant in ms, all sites of f, on
// the tree as the walk that found the sites saw it. A mutant without one
// gets no probe (see anchorOf).
func probeAnchors(f *ast.File, ms []Mutant) map[string]anchor {
	want := map[ast.Node][]string{}
	for _, m := range ms {
		want[m.site.Node] = append(want[m.site.Node], m.ID)
	}
	out := map[string]anchor{}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		if ids := want[n]; len(ids) > 0 {
			if a, ok := anchorOf(stack); ok {
				for _, id := range ids {
					out[id] = a
				}
			}
		}
		return true
	})
	return out
}

// anchorOf places the probe of a site on the last node of stack, whose
// ancestors precede it. A site that is a statement list (the body a branch
// mutant empties) is probed at its start, which runs exactly when the
// body is entered. Any other site is probed before the innermost statement
// holding it that sits in a list: the site runs only after that statement
// starts, so the probe may record a test the site itself never runs, but
// never misses one. A labeled statement is not probed, since a goto to its
// label would skip the probe.
func anchorOf(stack []ast.Node) (anchor, bool) {
	switch n := stack[len(stack)-1].(type) {
	case *ast.CaseClause, *ast.CommClause:
		return anchor{into: n}, true
	case *ast.BlockStmt:
		switch stack[len(stack)-2].(type) {
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			// its elements are clauses, which hold no statement before them
		default:
			return anchor{into: n}, true
		}
	}
	for i := len(stack) - 1; i > 0; i-- {
		s, ok := stack[i].(ast.Stmt)
		if !ok || !inList(stack[i-1], s) {
			continue
		}
		if _, labeled := s.(*ast.LabeledStmt); labeled {
			return anchor{}, false
		}
		return anchor{before: s}, true
	}
	return anchor{}, false
}

// inList reports whether s is an element of parent's statement list: a
// block's, other than the clauses of a switch or select body, or a
// clause's body.
func inList(parent ast.Node, s ast.Stmt) bool {
	switch p := parent.(type) {
	case *ast.BlockStmt:
		switch s.(type) {
		case *ast.CaseClause, *ast.CommClause:
			return false
		}
		return true
	case *ast.CaseClause:
		return slices.Contains(p.Body, s)
	case *ast.CommClause:
		return slices.Contains(p.Body, s)
	}
	return false
}

// placeProbes inserts into f, already lowered, the probe of each anchored
// mutant that sch does not embed, and records it in sch.Probed; it reports
// whether it placed any. replaced maps the nodes the lowering replaced to
// their replacements: a statement before which a probe goes may have been
// rebuilt, and the probe goes before what stands in its place. A list
// owner that was replaced gets no probe, since its statements may now sit
// anywhere.
//
// Both kinds of anchor are on the path the unmutated program takes: a
// lowering keeps the original code in the branch GOMUTANT_ID leaves
// unselected, which is the only one a trace run takes.
func placeProbes(f *ast.File, runtime string, anchors map[string]anchor, replaced map[ast.Node]ast.Node, sch *Schemata) bool {
	into := map[ast.Node][]string{}
	before := map[ast.Node][]string{}
	for _, id := range slices.Sorted(maps.Keys(anchors)) {
		if sch.Embedded[id] {
			continue
		}
		switch a := anchors[id]; {
		case a.into != nil:
			if _, gone := replaced[a.into]; !gone {
				into[a.into] = append(into[a.into], id)
			}
		default:
			var n ast.Node = a.before
			if r, ok := replaced[n]; ok {
				n = r
			}
			before[n] = append(before[n], id)
		}
	}
	probes := func(ids []string) []ast.Stmt {
		out := make([]ast.Stmt, len(ids))
		for i, id := range ids {
			out[i] = &ast.ExprStmt{X: runtimeCall(runtime, "Active", id)}
			sch.Probed[id] = true
		}
		return out
	}
	placed := false
	astutil.Apply(f, func(c *astutil.Cursor) bool {
		switch n := c.Node().(type) {
		case *ast.BlockStmt:
			if ids := into[n]; len(ids) > 0 {
				n.List = append(probes(ids), n.List...)
				placed = true
			}
		case *ast.CaseClause:
			if ids := into[n]; len(ids) > 0 {
				n.Body = append(probes(ids), n.Body...)
				placed = true
			}
		case *ast.CommClause:
			if ids := into[n]; len(ids) > 0 {
				n.Body = append(probes(ids), n.Body...)
				placed = true
			}
		}
		delete(into, c.Node())
		// A statement a lowering copied into both branches of its
		// selection is probed in both.
		if ids := before[c.Node()]; len(ids) > 0 && c.Index() >= 0 {
			for _, p := range probes(ids) {
				c.InsertBefore(p)
			}
			placed = true
		}
		return true
	}, nil)
	return placed
}
