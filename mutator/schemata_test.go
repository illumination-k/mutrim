package mutator_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

// notEmbedded lists the fixture sites the lowering must decline, as
// "func description". Everything else viable must be embedded.
var notEmbedded = map[string]bool{
	`SkipConst * -> /`:                  true, // constant expression
	`SkipNamedBool < -> <=`:             true, // result is a defined bool type
	`SkipNamedBoolOperands && -> ||`:    true, // operands are a defined bool type
	`SkipRecover || -> &&`:              true, // recover() in the closure operand
	`SkipMapPost ++ -> --`:              true, // map element in a for post statement
	`Shift << -> >>`:                    true, // constant left operand of a shift
	`Sign -x -> x`:                      true, // constant expression
	`FirstEven -x -> x`:                 true,
	`SkipInitCall call -> removed`:      true, // call in a for init or post statement
	`SkipNamedBoolCond cond -> !(cond)`: true, // condition of a defined bool type
	`SkipNamedBoolCond cond -> true`:    true,
	`SkipNamedBoolCond cond -> false`:   true,
	`GreaterEq 0 -> 1`:                  true, // literal of a defined type
	`Scale 2 -> 3`:                      true,
	`SkipNamedConst 1 -> 2`:             true,
	`SkipShiftConst << -> >>`:           true, // constant left operand of a shift
	`SkipShiftConst 1 -> 2`:             true, // operands of a constant shift
	`SkipShiftConst 2 -> 3`:             true,
	`SkipNamedBool < -> >=`:             true, // result is a defined bool type
	`Classify case body -> empty`:       true, // the case body makes the switch terminating
	`Drain default body -> empty`:       true,
	`Smallest Min -> Max`:               true, // a generic function has no function value
}

// TestSchemataIdentity lowers the schemata fixture, pins the generated
// source with a golden file, and runs the fixture's own tests against it
// with GOMUTANT_ID unset: the schemata source must behave like the original.
func TestSchemataIdentity(t *testing.T) {
	pkg := load(t, "schemata")
	mutants := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	sch, err := mutator.Lower(pkg, mutants, mutator.Blocks(pkg))
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range mutants {
		key := m.Func + " " + m.Description
		if want := m.Viable && m.Ignored == "" && !notEmbedded[key]; sch.Embedded[m.ID] != want {
			t.Errorf("%s: embedded=%v, want %v", key, sch.Embedded[m.ID], want)
		}
	}
	if len(sch.Files) != 1 {
		t.Fatalf("want one rewritten file, got %v", sch.Files)
	}

	var file string
	var src []byte
	for f, s := range sch.Files {
		file, src = f, s
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), file, src, parser.ParseComments); perr != nil {
		t.Fatalf("schemata source does not parse: %v\n%s", perr, src)
	}
	golden := filepath.Join("testdata", "schemata.golden")
	if *update {
		if werr := os.WriteFile(golden, src, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	want, err := os.ReadFile(filepath.Clean(golden))
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != string(want) {
		t.Errorf("schemata source differs from %s (run with -update to accept)\n--- got ---\n%s", golden, src)
	}

	if out, err := goTestOverlay(t, "schemata", overlayFor(t, sch), ""); err != nil {
		t.Fatalf("schemata source must pass the fixture tests: %v\n%s", err, out)
	}
}

// overlayFor writes the lowered sources to a temp directory and returns the
// go build -overlay file naming them.
func overlayFor(t *testing.T, sch *mutator.Schemata) string {
	t.Helper()
	dir := t.TempDir()
	overlay := mutator.Overlay{Replace: map[string]string{}}
	for orig, src := range sch.Files {
		mutated := filepath.Join(dir, filepath.Base(orig))
		if err := os.WriteFile(mutated, src, 0o600); err != nil {
			t.Fatal(err)
		}
		overlay.Replace[orig] = mutated
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// goTestOverlay runs the fixture's own tests against the lowered sources
// with GOMUTANT_ID set to id; an empty id is the identity run.
func goTestOverlay(t *testing.T, fixture, overlayPath, id string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-overlay", overlayPath, "./testdata/"+fixture) //nolint:gosec // test-controlled args
	cmd.Env = append(os.Environ(), "GOMUTANT_ID="+id)
	return cmd.CombinedOutput()
}

// TestSchemataOptInIdentity lowers the opt-in call operator, which the
// schemata fixture does not use: the source must compile, behave like the
// original with GOMUTANT_ID unset, and fail the fixture's tests when one of
// its mutants is selected.
func TestSchemataOptInIdentity(t *testing.T) {
	pkg := load(t, "calls")
	ops, err := mutator.Operators("default,call")
	if err != nil {
		t.Fatal(err)
	}
	mutants := mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true})
	sch, err := mutator.Lower(pkg, mutants, nil)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, m := range mutants {
		if m.Operator == "call" && m.Func == "Count" && sch.Embedded[m.ID] {
			selected = m.ID
		}
	}
	if selected == "" {
		t.Fatal("no call mutant of Count embedded")
	}

	overlayPath := overlayFor(t, sch)
	if out, err := goTestOverlay(t, "calls", overlayPath, ""); err != nil {
		t.Fatalf("schemata source must pass the fixture tests: %v\n%s", err, out)
	}
	if out, err := goTestOverlay(t, "calls", overlayPath, selected); err == nil {
		t.Errorf("mutant %s should have been killed\n%s", selected, out)
	}
}

// TestSchemataConcurrency lowers the opt-in concurrency operator and runs
// the fixture's tests under the race detector: the identity must pass, and
// an atomic add made plain must be reported as a data race.
func TestSchemataConcurrency(t *testing.T) {
	pkg := load(t, "concurrency")
	ops, err := mutator.Operators("concurrency")
	if err != nil {
		t.Fatal(err)
	}
	mutants := mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true})
	sch, err := mutator.Lower(pkg, mutants, nil)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, m := range mutants {
		if !sch.Embedded[m.ID] {
			t.Errorf("%s %s: not embedded", m.Func, m.Description)
		}
		if m.Func == "Sum" && m.Description == "atomic.AddInt64 -> +=" {
			selected = m.ID
		}
	}
	if selected == "" {
		t.Fatal("no atomic mutant of Sum")
	}

	overlayPath := overlayFor(t, sch)
	race := func(id string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "go", "test", "-race", "-count=1", "-run", "^TestSum$", "-overlay", overlayPath, "./testdata/concurrency") //nolint:gosec // test-controlled args
		cmd.Env = append(os.Environ(), "GOMUTANT_ID="+id)
		return cmd.CombinedOutput()
	}
	if out, err := race(""); err != nil {
		t.Fatalf("schemata source must pass the fixture tests: %v\n%s", err, out)
	}
	if out, err := race(selected); err == nil || !strings.Contains(string(out), "DATA RACE") {
		t.Errorf("mutant %s should have been killed by the race detector: %v\n%s", selected, err, out)
	}
}

// TestSchemataCompiles lowers the fixture of every operator family, with
// every block reached, and compiles it: the schemata source of a package must always build, since one
// broken file costs every mutant of the package. The fixtures without tests
// are only compiled, which is what `go test` on them does.
func TestSchemataCompiles(t *testing.T) {
	for _, fixture := range goldenFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			ops, err := mutator.Operators(fixture.operators)
			if err != nil {
				t.Fatal(err)
			}
			pkg := load(t, fixture.name)
			sch, err := mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true}), mutator.Blocks(pkg))
			if err != nil {
				t.Fatal(err)
			}
			if len(sch.Embedded) == 0 {
				t.Fatal("no mutant embedded")
			}
			if out, err := goTestOverlay(t, fixture.name, overlayFor(t, sch), ""); err != nil {
				t.Fatalf("schemata source must build: %v\n%s", err, out)
			}
		})
	}
}

// The runtime import name must not collide with identifiers of the file,
// whether bound in the package scope or inside a function.
func TestSchemataRuntimeNameAvoidsCollision(t *testing.T) {
	pkg := load(t, "mutname")
	sch, err := mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true}), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sch.Files {
		s := string(src)
		if !strings.Contains(s, `mut2 "`+mutator.RuntimePath+`"`) || !strings.Contains(s, "mut2.Cmp(") {
			t.Errorf("expected the runtime to be imported as mut2:\n%s", s)
		}
	}
}

// Comment groups are dropped from schemata sources unless they hold a
// //go: directive, which keeps its meaning because declarations stay
// where they were.
func TestSchemataKeepsDirectives(t *testing.T) {
	pkg := load(t, "mutname")
	sch, err := mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true}), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sch.Files {
		s := string(src)
		if !strings.Contains(s, "//go:noinline\nfunc Above(") {
			t.Errorf("the //go:noinline directive must stay on Above:\n%s", s)
		}
		if strings.Contains(s, "default runtime import name") {
			t.Errorf("comment groups without a directive must be dropped:\n%s", s)
		}
	}
}

// Files without an embedded mutant are not rewritten; the runtime itself
// has nothing to embed; a package that already imports the runtime gets
// it a second time under a fresh name.
func TestLowerSkipsAndReimports(t *testing.T) {
	pkg := load(t, "excluded")
	sch, err := mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sch.Files) != 1 {
		t.Fatalf("want only normal.go rewritten, got %d files", len(sch.Files))
	}
	for name := range sch.Files {
		if filepath.Base(name) != "normal.go" {
			t.Errorf("rewrote %s", name)
		}
	}

	pkgs, err := mutator.Load(".", "../mut")
	if err != nil {
		t.Fatal(err)
	}
	runtime := pkgs[0]
	if runtime.PkgPath != mutator.RuntimePath {
		t.Fatalf("loaded %s, want %s", runtime.PkgPath, mutator.RuntimePath)
	}
	mutants := mutator.Generate(runtime, mutator.Options{TypeCheck: true})
	sch, err = mutator.Lower(runtime, mutants, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutants) == 0 || len(sch.Files) != 0 || len(sch.Embedded) != 0 {
		t.Errorf("the runtime must generate mutants but embed none: %d mutants, %+v", len(mutants), sch)
	}

	pkg = load(t, "importsmut")
	sch, err = mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true}), nil)
	if err != nil {
		t.Fatal(err)
	}
	for file, src := range sch.Files {
		if _, perr := parser.ParseFile(token.NewFileSet(), file, src, 0); perr != nil {
			t.Errorf("schemata source does not parse: %v\n%s", perr, src)
		}
		if s := string(src); strings.Count(s, `"`+mutator.RuntimePath+`"`) != 2 || !strings.Contains(s, "mut1.Cmp(") {
			t.Errorf("want the runtime imported a second time as mut1:\n%s", s)
		}
	}
}

// TestSchemataVetPrintf lowers the literal fixture, whose printf formats
// are string sites, and runs vet's printf check on the result: a lowered
// format is no longer constant, which go test and nogo reject (#67).
func TestSchemataVetPrintf(t *testing.T) {
	pkg := load(t, "literal")
	sch, err := mutator.Lower(pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true}), nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "vet", "-printf", "-overlay", overlayFor(t, sch), "./testdata/literal") //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet -printf on the schemata source: %v\n%s", err, out)
	}
}

// TestSchemataErrPath lowers the errpath operator: the source must pass
// vet's printf check (a degraded format stays constant), behave like the
// original with GOMUTANT_ID unset, and have every errpath mutant killed by
// the fixture's tests but the one they cannot tell apart.
func TestSchemataErrPath(t *testing.T) {
	pkg := load(t, "errpath")
	ops, err := mutator.Operators("errpath")
	if err != nil {
		t.Fatal(err)
	}
	mutants := mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: true})
	sch, err := mutator.Lower(pkg, mutants, nil)
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := overlayFor(t, sch)
	vet := exec.CommandContext(t.Context(), "go", "vet", "-printf", "-overlay", overlayPath, "./testdata/errpath") //nolint:gosec // test-controlled args
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet -printf on the schemata source: %v\n%s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "errpath.test")
	build := exec.CommandContext(t.Context(), "go", "test", "-c", "-o", bin, "-overlay", overlayPath, "./testdata/errpath") //nolint:gosec // test-controlled args
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(id string) error {
		cmd := exec.CommandContext(t.Context(), bin) //nolint:gosec // test-controlled args
		cmd.Env = append(os.Environ(), "GOMUTANT_ID="+id)
		return cmd.Run()
	}
	if err := run(""); err != nil {
		t.Fatalf("schemata source must pass the fixture tests: %v", err)
	}
	// errors.As forced to true leaves the target nil, which NumError
	// returns as it would without a match.
	lives := map[string]bool{"NumError errors.As -> true": true}
	for _, m := range mutants {
		key := m.Func + " " + m.Description
		if !sch.Embedded[m.ID] {
			t.Errorf("%s: not embedded", key)
			continue
		}
		if killed := run(m.ID) != nil; killed == lives[key] {
			t.Errorf("%s: killed=%v", key, killed)
		}
	}
}
