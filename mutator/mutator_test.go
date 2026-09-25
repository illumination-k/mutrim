package mutator_test

import (
	"encoding/json/v2"
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/illumination-k/mutrim/mutator"
)

var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T, name string) *packages.Package {
	t.Helper()
	pkgs, err := mutator.Load(".", "./testdata/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return pkgs[0]
}

// render lists mutants in a position-first, path-free form for golden files.
// Each line ends with the source line as it reads after the mutant is applied,
// so the golden files also pin the rewrite itself, not only its location.
func render(t *testing.T, pkg *packages.Package, ops []mutator.Operator, ms []mutator.Mutant) string {
	t.Helper()
	var b strings.Builder
	for _, m := range ms {
		ignored := ""
		if m.Ignored != "" {
			ignored = fmt.Sprintf(" ignored=%s(%q)", m.Ignored, m.Reason)
		}
		if m.Equivalent != "" {
			ignored += " equivalent=" + m.Equivalent
		}
		if m.Class != mutator.ClassDefault {
			ignored += " class=" + m.Class
		}
		// A removed statement leaves an empty line; no trailing blank.
		line := fmt.Sprintf("%s:%d:%d %s %s %q viable=%v%s | %s",
			filepath.Base(m.File), m.Line, m.Col, m.Func, m.Operator, m.Description, m.Viable, ignored,
			mutatedLine(t, pkg, ops, m))
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
}

func mutatedLine(t *testing.T, pkg *packages.Package, ops []mutator.Operator, m mutator.Mutant) string {
	t.Helper()
	_, src, err := mutator.Source(pkg, m.ID, ops)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	if m.Line > len(lines) {
		t.Fatalf("%s: line %d beyond mutated source (%d lines)", m.ID, m.Line, len(lines))
	}
	return strings.TrimSpace(lines[m.Line-1])
}

// goldenFixtures lists the testdata packages pinned by a golden file, with
// the -operators spec to generate them with; an empty spec is the default
// operator set.
var goldenFixtures = []struct{ name, operators string }{
	{name: "relational"},
	{name: "arith"},
	{name: "logical"},
	{name: "bitwise"},
	{name: "constant"},
	{name: "control"},
	{name: "stmt"},
	{name: "ret"},
	{name: "assign"},
	{name: "loop"},
	{name: "branch"},
	{name: "literal"},
	{name: "calls", operators: "default,call"},
	{name: "concurrency", operators: "concurrency"},
	{name: "errpath", operators: "errpath,return,condition"},
	{name: "excluded"},
	{name: "disable"},
	{name: "equivalent"},
}

func TestGenerateGolden(t *testing.T) {
	for _, fixture := range goldenFixtures {
		name := fixture.name
		t.Run(name, func(t *testing.T) {
			ops, err := mutator.Operators(fixture.operators)
			if err != nil {
				t.Fatal(err)
			}
			pkg := load(t, name)
			mutants := mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true})
			got := render(t, pkg, ops, mutants)
			// Every Source call above applied and undid a mutant; the tree
			// must be back to the original so that a second pass agrees.
			if again := render(t, pkg, ops, mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true})); again != got {
				t.Fatalf("mutants differ after apply/undo round trip:\n--- first ---\n%s--- second ---\n%s", got, again)
			}
			golden := filepath.Join("testdata", name+".golden")
			if *update {
				if werr := os.WriteFile(golden, []byte(got), 0o600); werr != nil {
					t.Fatal(werr)
				}
			}
			want, err := os.ReadFile(filepath.Clean(golden))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("mutants differ from %s (run with -update to accept)\n--- got ---\n%s", golden, got)
			}
		})
	}
}

// Load reports an unknown package, a package with type errors, a bad
// working directory and a pattern matching nothing, rather than returning
// an empty package list or a nil error.
func TestLoadErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		dir     string
		pattern string
	}{
		{"unknown package", ".", "./testdata/does-not-exist"},
		{"package with type errors", ".", "./testdata/broken"},
		{"missing working directory", filepath.Join(t.TempDir(), "missing"), "."},
		{"pattern matching no package", ".", "./testdata/does-not-exist/..."},
	} {
		if _, err := mutator.Load(tt.dir, tt.pattern); err == nil {
			t.Errorf("%s: expected an error", tt.name)
		}
	}
}

func TestSourceUnknownMutant(t *testing.T) {
	if _, _, err := mutator.Source(load(t, "killable"), "0000000000000000", nil); err == nil {
		t.Error("expected an error for an unknown mutant ID")
	}
}

// A mutant's span is the code it replaces: the operator token alone for a
// binary expression, whose Node is the whole expression, and the whole
// node for everything else.
func TestMutantSpan(t *testing.T) {
	spans := map[string][2]int{}
	for _, m := range mutator.Generate(load(t, "relational"), mutator.Options{}) {
		if m.Line != m.EndLine || m.EndCol <= m.Col {
			t.Errorf("%s %q spans %d:%d-%d:%d", m.Operator, m.Description, m.Line, m.Col, m.EndLine, m.EndCol)
		}
		spans[m.Description] = [2]int{m.Col, m.EndCol}
	}
	// "return a < b, a <= b, ..." on line 4: the < is one column wide, the
	// <= two, and the return covers the whole statement.
	for desc, want := range map[string][2]int{
		"< -> <=":               {11, 12},
		"<= -> <":               {18, 20},
		"return -> zero values": {2, 53},
	} {
		if got := spans[desc]; got != want {
			t.Errorf("%q spans columns %v, want %v", desc, got, want)
		}
	}
}

// Mutant IDs must not depend on source positions: inserting lines above a
// site keeps its ID.
func TestMutantIDStableAcrossLineShift(t *testing.T) {
	before := ids(t, mutator.Generate(load(t, "control"), mutator.Options{}))

	file, err := filepath.Abs("testdata/control/control.go")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	shifted := strings.Replace(string(src), "package control\n", "package control\n\n// shifted\n\n", 1)
	cfg := &packages.Config{Mode: mutator.LoadMode, Overlay: map[string][]byte{file: []byte(shifted)}}
	pkgs, err := packages.Load(cfg, "./testdata/control")
	if err != nil {
		t.Fatal(err)
	}
	after := ids(t, mutator.Generate(pkgs[0], mutator.Options{}))

	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("mutant count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].id != after[i].id {
			t.Errorf("%s: id %s -> %s", before[i].desc, before[i].id, after[i].id)
		}
		if before[i].line+3 != after[i].line {
			t.Errorf("%s: expected line shift by 3, got %d -> %d", before[i].desc, before[i].line, after[i].line)
		}
	}
}

type idLine struct {
	id   string
	desc string
	line int
}

func ids(t *testing.T, ms []mutator.Mutant) []idLine {
	t.Helper()
	out := make([]idLine, len(ms))
	seen := map[string]bool{}
	for i, m := range ms {
		if seen[m.ID] {
			t.Errorf("duplicate id %s", m.ID)
		}
		seen[m.ID] = true
		out[i] = idLine{m.ID, m.Func + " " + m.Description, m.Line}
	}
	return out
}

// A viable mutant applied through `go test -overlay` must make the
// fixture's test fail, while the untouched package passes.
func TestOverlayKillsMutant(t *testing.T) {
	pkg := load(t, "killable")
	var target mutator.Mutant
	for _, m := range mutator.Generate(pkg, mutator.Options{TypeCheck: true}) {
		if m.Operator == "relational" && m.Viable {
			target = m
		}
	}
	if target.ID == "" {
		t.Fatal("no viable relational mutant in killable fixture")
	}

	file, src, err := mutator.Source(pkg, target.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "x >= 0") {
		t.Fatalf("unexpected mutated source:\n%s", src)
	}

	dir := t.TempDir()
	mutated := filepath.Join(dir, "killable.go")
	if err := os.WriteFile(mutated, src, 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, marshalErr := json.Marshal(mutator.Overlay{Replace: map[string]string{file: mutated}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := goTest(t); err != nil {
		t.Fatalf("original package must pass: %v\n%s", err, out)
	}
	if out, err := goTest(t, "-overlay", overlayPath); err == nil {
		t.Fatalf("mutant %s should have been killed\n%s", target.ID, out)
	}
}

func goTest(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", append(append([]string{"test"}, args...), "./testdata/killable")...) //nolint:gosec // test-controlled args
	return cmd.CombinedOutput()
}

// A custom operator table is lowered like the built-in ones as long as a
// swap stays within its class; a swap across classes still yields a
// mutant through Apply (the type check decides its viability), but no
// schemata.
func TestCustomOperatorSchemata(t *testing.T) {
	src := "package shift\n\nfunc F(a, b int) int { return a << b }\n\nfunc G(a, b complex128) complex128 { return a + b }\n"
	file := filepath.Join(t.TempDir(), "shift.go")
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := mutator.LoadFiles(mutator.FilesConfig{ImportPath: "example.com/shift", Files: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	op := mutator.BinaryOp{OpName: "custom", Table: map[token.Token]token.Token{token.SHL: token.SHR, token.ADD: token.LSS}}
	mutants := mutator.Generate(pkg, mutator.Options{Operators: []mutator.Operator{op}, TypeCheck: true})
	viable := map[string]bool{}
	for _, m := range mutants {
		if m.Operator != "custom" {
			t.Errorf("operator = %q, want custom", m.Operator)
		}
		viable[m.Description] = m.Viable
	}
	if want := map[string]bool{"<< -> >>": true, "+ -> <": false}; !maps.Equal(viable, want) {
		t.Errorf("mutants = %v, want %v", viable, want)
	}
	sch, err := mutator.Lower(pkg, mutants, nil, mutator.RuntimePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutants {
		if want := m.Description == "<< -> >>"; sch.Embedded[m.ID] != want {
			t.Errorf("%s: embedded=%v, want %v", m.Description, sch.Embedded[m.ID], want)
		}
	}
}

// -operators: names add, "default" adds every operator, "-name" removes;
// the order is DefaultOperators', whatever the spelling.
func TestOperators(t *testing.T) {
	names := func(ops []mutator.Operator) string {
		out := make([]string, len(ops))
		for i, op := range ops {
			out[i] = op.Name()
		}
		return strings.Join(out, ",")
	}
	defaults := names(mutator.DefaultOperators)
	cases := map[string]string{
		"":                              defaults,
		"default":                       defaults,
		"default,-constant,-voidcall":   strings.ReplaceAll(strings.ReplaceAll(defaults, "constant,", ""), "voidcall,", ""),
		"return, relational":            "relational,return",
		"constant,-constant,arithmetic": "arithmetic",
		// call and concurrency are not defaults, so only their names select them.
		"default,call,concurrency": names(mutator.AllOperators),
		"call":                     "call",
		"concurrency":              "concurrency",
	}
	for spec, want := range cases {
		ops, err := mutator.Operators(spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
		} else if got := names(ops); got != want {
			t.Errorf("%q = %s, want %s", spec, got, want)
		}
	}
	for _, spec := range []string{"bogus", "-default", "default,-relational,-x", "-constant"} {
		if _, err := mutator.Operators(spec); err == nil {
			t.Errorf("%q: expected an error", spec)
		}
	}
}

// A mutant's diff applies to its original file and gives the code Source
// writes, in both contexts and for every fixture: the diff is the rewrite
// itself. Only the code is compared, since a rewritten node has no
// position and the printer places it and the comments around it as it
// can. An ignored or not viable mutant has none, nor has a rewrite that
// prints as the original.
func TestDiffAppliesAsSource(t *testing.T) {
	for _, fixture := range goldenFixtures {
		ops, err := mutator.Operators(fixture.operators)
		if err != nil {
			t.Fatal(err)
		}
		pkg := load(t, fixture.name)
		for _, dc := range []mutator.DiffContext{mutator.DiffStmt, mutator.DiffFunc} {
			for _, m := range mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true, Diff: dc}) {
				if m.Ignored != "" || !m.Viable {
					if m.Diff != "" {
						t.Errorf("%s %s %q: ignored or not viable, yet a diff", fixture.name, m.Operator, m.Description)
					}
					continue
				}
				orig, err := os.ReadFile(m.File)
				if err != nil {
					t.Fatal(err)
				}
				_, want, err := mutator.Source(pkg, m.ID, ops)
				if err != nil {
					t.Fatal(err)
				}
				if m.Diff == "" { // a rewrite that prints as the original
					if string(want) != string(orig) {
						t.Errorf("%s %s %q: no diff", fixture.name, m.Operator, m.Description)
					}
					continue
				}
				if got := applyDiff(t, string(orig), m.Diff); code(t, got) != code(t, string(want)) {
					t.Errorf("%s %s %s %q: diff\n%s\ndoes not give the Source output", fixture.name, dc, m.Operator, m.Description, m.Diff)
				}
			}
		}
	}
}

// A stmt diff shows the changed statement; a func diff the whole function.
func TestDiffContext(t *testing.T) {
	find := func(dc mutator.DiffContext) mutator.Mutant {
		for _, m := range mutator.Generate(load(t, "control"), mutator.Options{Diff: dc}) {
			if m.Func == "(*Counter).Inc" && m.Operator == "incdec" {
				return m
			}
		}
		t.Fatal("no incdec mutant in (*Counter).Inc")
		return mutator.Mutant{}
	}
	stmt := find(mutator.DiffStmt)
	want := fmt.Sprintf("--- %s\n+++ %s\n@@ -22,1 +22,1 @@\n-\tc.n++\n+\tc.n--\n", stmt.File, stmt.File)
	if stmt.Diff != want {
		t.Errorf("stmt diff =\n%s\nwant\n%s", stmt.Diff, want)
	}
	fn := find(mutator.DiffFunc)
	want = fmt.Sprintf("--- %s\n+++ %s\n@@ -21,3 +21,3 @@\n func (c *Counter) Inc() {\n-\tc.n++\n+\tc.n--\n }\n", fn.File, fn.File)
	if fn.Diff != want {
		t.Errorf("func diff =\n%s\nwant\n%s", fn.Diff, want)
	}
	if none := find(mutator.DiffNone); none.Diff != "" {
		t.Errorf("DiffNone recorded %q", none.Diff)
	}
}

// code lists the tokens of src, without comments and the semicolons a
// line break inserts, so that two layouts of one program compare equal.
func code(t *testing.T, src string) string {
	t.Helper()
	var sc scanner.Scanner
	fset := token.NewFileSet()
	sc.Init(fset.AddFile("", -1, len(src)), []byte(src), func(pos token.Position, msg string) { t.Fatalf("%v: %s", pos, msg) }, 0)
	var b strings.Builder
	for {
		_, tok, lit := sc.Scan()
		if tok == token.EOF {
			return b.String()
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		b.WriteString(tok.String() + " " + lit + "\n")
	}
}

// applyDiff applies the single hunk of a unified diff to src.
func applyDiff(t *testing.T, src, diff string) string {
	t.Helper()
	lines := strings.Split(diff, "\n")
	if len(lines) < 4 {
		t.Fatalf("not a unified diff:\n%s", diff)
	}
	var oldStart, oldLen, newStart, newLen int
	if _, err := fmt.Sscanf(lines[2], "@@ -%d,%d +%d,%d @@", &oldStart, &oldLen, &newStart, &newLen); err != nil {
		t.Fatalf("hunk header %q: %v", lines[2], err)
	}
	var old, repl []string
	for _, l := range lines[3 : len(lines)-1] {
		switch l[0] {
		case ' ':
			old, repl = append(old, l[1:]), append(repl, l[1:])
		case '-':
			old = append(old, l[1:])
		case '+':
			repl = append(repl, l[1:])
		}
	}
	if len(old) != oldLen || len(repl) != newLen {
		t.Fatalf("hunk counts %d,%d do not match its lines %d,%d:\n%s", oldLen, newLen, len(old), len(repl), diff)
	}
	file := strings.Split(src, "\n")
	at := oldStart - 1
	if got := strings.Join(file[at:at+oldLen], "\n"); got != strings.Join(old, "\n") {
		t.Fatalf("diff does not match the source at line %d:\n%s\nsource:\n%s", oldStart, diff, got)
	}
	return strings.Join(append(append(file[:at:at], repl...), file[at+oldLen:]...), "\n")
}
