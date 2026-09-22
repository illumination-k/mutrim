package mutator

import (
	"go/parser"
	"go/token"
	"testing"
)

// The directive parser is exercised end to end by the disable golden; this
// pins the spellings it does not contain: tab separators, an explicit
// "all", two open blocks closed by one enable, and a disable-func comment
// that documents nothing.
func TestParseDisables(t *testing.T) {
	const src = `package p

//mutrim:disable-func stray, not a doc comment

func F(a, b int) bool {
	//mutrim:disable	all	tab separated
	//mutrim:disable relational
	x := a < b
	//mutrim:enable
	//mutrim:disable-next-line	constant
	return x || a == 1
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	dis := parseDisables(fset, f)

	for _, tc := range []struct {
		line   int
		op     string
		want   bool
		reason string
	}{
		{line: 3, op: "relational"},                                      // a stray disable-func has no effect
		{line: 6, op: "logical", want: true, reason: "tab separated"},    // the block starts on its own line
		{line: 8, op: "logical", want: true, reason: "tab separated"},    // "all" covers every operator
		{line: 8, op: "relational", want: true, reason: "tab separated"}, // the first match wins
		{line: 10, op: "relational"},                                     // one enable closes both blocks
		{line: 11, op: "constant", want: true},                           // disable-next-line, no reason
		{line: 11, op: "logical"},                                        // and only for the named operator
	} {
		reason, got := dis.find(tc.line, tc.op)
		if got != tc.want || reason != tc.reason {
			t.Errorf("find(%d, %q) = %q, %v; want %q, %v", tc.line, tc.op, reason, got, tc.reason, tc.want)
		}
	}
}
