package mutator_test

import (
	"encoding/json"
	"flag"
	"fmt"
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
func render(t *testing.T, pkg *packages.Package, ms []mutator.Mutant) string {
	t.Helper()
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "%s:%d:%d %s %s %q viable=%v | %s\n",
			filepath.Base(m.File), m.Line, m.Col, m.Func, m.Operator, m.Description, m.Viable,
			mutatedLine(t, pkg, m))
	}
	return b.String()
}

func mutatedLine(t *testing.T, pkg *packages.Package, m mutator.Mutant) string {
	t.Helper()
	_, src, err := mutator.Source(pkg, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	if m.Line > len(lines) {
		t.Fatalf("%s: line %d beyond mutated source (%d lines)", m.ID, m.Line, len(lines))
	}
	return strings.TrimSpace(lines[m.Line-1])
}

func TestGenerateGolden(t *testing.T) {
	for _, name := range []string{"relational", "arith", "logical", "control", "ret", "excluded"} {
		t.Run(name, func(t *testing.T) {
			pkg := load(t, name)
			mutants := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
			got := render(t, pkg, mutants)
			// Every Source call above applied and undid a mutant; the tree
			// must be back to the original so that a second pass agrees.
			if again := render(t, pkg, mutator.Generate(pkg, mutator.Options{TypeCheck: true})); again != got {
				t.Fatalf("mutants differ after apply/undo round trip:\n--- first ---\n%s--- second ---\n%s", got, again)
			}
			golden := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatal(err)
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

func TestLoadErrors(t *testing.T) {
	if _, err := mutator.Load(".", "./testdata/does-not-exist"); err == nil {
		t.Error("unknown package: expected an error")
	}
	if _, err := mutator.Load(".", "./testdata/broken"); err == nil {
		t.Error("package with type errors: expected an error")
	}
}

func TestSourceUnknownMutant(t *testing.T) {
	if _, _, err := mutator.Source(load(t, "killable"), "0000000000000000"); err == nil {
		t.Error("expected an error for an unknown mutant ID")
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

	file, src, err := mutator.Source(pkg, target.ID)
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
