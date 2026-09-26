package main

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/ingest"
	"github.com/illumination-k/mutrim/runner"
)

// A Stryker report of three tests: b kills what a kills and more, c
// kills nothing but reaches a mutant no other test reaches; one mutant
// survives.
const strykerFixture = `{
  "files": {"src/f.ts": {"mutants": [
    {"id": "0", "mutatorName": "EqualityOperator", "replacement": "x <= 0", "status": "Killed",
     "coveredBy": ["0", "1"], "killedBy": ["0", "1"], "location": {"start": {"line": 1, "column": 5}, "end": {"line": 1, "column": 10}}},
    {"id": "1", "mutatorName": "BooleanLiteral", "replacement": "true", "status": "Killed",
     "coveredBy": ["1"], "killedBy": ["1"], "location": {"start": {"line": 2, "column": 5}, "end": {"line": 2, "column": 10}}},
    {"id": "2", "mutatorName": "StringLiteral", "replacement": "\"\"", "status": "Survived",
     "coveredBy": ["2"], "location": {"start": {"line": 3, "column": 5}, "end": {"line": 3, "column": 7}}}
  ]}},
  "testFiles": {"src/f.test.ts": {"tests": [{"id": "0", "name": "f a"}, {"id": "1", "name": "f b"}, {"id": "2", "name": "f c"}]}}
}`

const junitFixture = `<testsuites><testsuite name="src/f.test.ts">
  <testcase classname="src/f.test.ts" name="f &gt; a" time="0.002"/>
  <testcase classname="src/f.test.ts" name="f &gt; b" time="0.004"/>
</testsuite></testsuites>`

// importTo runs `mutrim import` on input, written to dir, into dir.
func importTo(t *testing.T, dir, tool, input string, flags ...string) string {
	t.Helper()
	in := filepath.Join(dir, tool+".in")
	if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, tool+".json")
	var stdout, stderr bytes.Buffer
	args := append(append([]string{"import", tool, "-o", out}, flags...), in)
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("import %s: %v\n%s", tool, err, stderr.String())
	}
	return out
}

// Observations files of one suite merge by test name into the minimize
// matrix: the kills of the Stryker report, the durations of the JUnit
// report and the blocks of one test's coverage.
func TestMinimizeObservations(t *testing.T) {
	dir := t.TempDir()
	stryker := importTo(t, dir, "stryker", strykerFixture)
	junit := importTo(t, dir, "junit", junitFixture, "-runner", "vitest")
	istanbul := importTo(t, dir, "istanbul",
		`{"/p/src/g.ts": {"path": "/p/src/g.ts", "statementMap": {"0": {"start": {"line": 1, "column": 0}, "end": {"line": 1, "column": 9}}}, "s": {"0": 1}}}`,
		"-root", "/p", "-test", "src/f.test.ts#f a")

	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"minimize", "-keep", "^$", stryker, junit, istanbul}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("minimize output is not JSON: %v\n%s", err, stdout.String())
	}
	// a is kept by the block only it reaches, c by the site only it
	// reaches, b by its kills: nothing is redundant.
	selected := map[string]bool{}
	for _, s := range result.Selected {
		selected[s.Name] = s.Essential
	}
	for _, name := range []string{"src/f.test.ts#f a", "src/f.test.ts#f b", "src/f.test.ts#f c"} {
		if !selected[name] {
			t.Errorf("%s not selected as essential: %+v", name, result.Selected)
		}
	}
	if len(result.Redundant) != 0 {
		t.Errorf("redundant = %+v", result.Redundant)
	}
	if tot := result.Totals; tot.Killed != 2 || tot.Dominators != 1 || tot.Survived != 1 {
		t.Errorf("totals = %+v, want 2 killed, 1 dominator (b's), 1 survivor", tot)
	}

	// Without the coverage, a's kill is subsumed by b's: a is redundant.
	stdout.Reset()
	if err := run(t.Context(), []string{"minimize", stryker, junit}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	result = minimizeOutput{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Redundant) != 1 || result.Redundant[0].Name != "src/f.test.ts#f a" || !slices.Equal(result.Redundant[0].SubsumedBy, []string{"src/f.test.ts#f b"}) {
		t.Errorf("redundant = %+v, want f a subsumed by f b", result.Redundant)
	}
}

func TestImportLLVMCovAndCargoMutants(t *testing.T) {
	dir := t.TempDir()
	// clamp's line is reached, once for its two regions; the test's own
	// body, in its tests module, is not a block, nor is a test_support
	// helper.
	export := `{"type": "llvm.coverage.json.export", "data": [{"functions": [
		{"name": "_RNvCsjJghpr7hpzZ_6rsdemo5clamp", "filenames": ["/p/src/lib.rs"], "regions": [[1, 1, 1, 46, 1, 0, 0, 0], [1, 8, 1, 14, 1, 0, 0, 0]]},
		{"name": "_RNvNtCsjJghpr7hpzZ_6rsdemo5testss_3low", "filenames": ["/p/src/lib.rs"], "regions": [[26, 5, 26, 13, 1, 0, 0, 0]]},
		{"name": "_RNvNtCsjJghpr7hpzZ_6rsdemo12test_support5parse", "filenames": ["/p/src/test_support.rs"], "regions": [[3, 1, 3, 20, 1, 0, 0, 0]]}
	]}]}`
	cov := importTo(t, dir, "llvm-cov", export, "-root", "/p", "-test", "rsdemo::tests::low")
	o := readObservations(t, cov)
	if len(o.Tests) != 1 || !slices.Equal(o.Tests[0].Blocks, []string{"src/lib.rs:1"}) {
		t.Errorf("llvm-cov observations = %+v", o)
	}
	// -regions keeps the regions; -files leaves out the other files.
	o = readObservations(t, importTo(t, dir, "llvm-cov", export, "-root", "/p", "-test", "x", "-regions", "-exclude-fn", "", "-files", "^src/test_"))
	if len(o.Tests) != 1 || !slices.Equal(o.Tests[0].Blocks, []string{"src/test_support.rs:3:1-3:20"}) {
		t.Errorf("llvm-cov -regions -files observations = %+v", o)
	}

	mutants := filepath.Join(dir, "mutants.out")
	if err := os.MkdirAll(filepath.Join(mutants, "log"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"outcomes.json": `{"outcomes": [
			{"scenario": {"Mutant": {"name": "src/lib.rs:2:10: replace < with == in clamp", "file": "src/lib.rs", "function": {"function_name": "clamp"}, "span": {"start": {"line": 2}}}}, "summary": "CaughtMutant", "log_path": "log/m.log"},
			{"scenario": {"Mutant": {"name": "src/lib.rs:2:10: replace < with <= in clamp", "file": "src/lib.rs", "function": {"function_name": "clamp"}, "span": {"start": {"line": 2}}}}, "summary": "MissedMutant", "log_path": "log/n.log"}
		]}`,
		"log/m.log": "     Running `target/debug/deps/rsdemo-0123456789abcdef`\ntest tests::low ... FAILED\n",
	} {
		if err := os.WriteFile(filepath.Join(mutants, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "cargo.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"import", "cargo-mutants", "-o", out, mutants}, &stdout, &stderr); err != nil {
		t.Fatalf("import cargo-mutants: %v\n%s", err, stderr.String())
	}
	o = readObservations(t, out)
	if len(o.Tests) != 1 || o.Tests[0].Name != "rsdemo::tests::low" || len(o.Tests[0].Kills) != 1 {
		t.Errorf("cargo-mutants observations = %+v", o)
	}

	// The missed mutant makes clamp a weak spot.
	stdout.Reset()
	if err := run(t.Context(), []string{"minimize", out, cov}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if want := []runner.Spot{{Func: "clamp", File: "src/lib.rs", Line: 2, Killed: 1, Lived: 1}}; !slices.Equal(result.WeakSpots, want) {
		t.Errorf("weak spots = %+v, want %+v", result.WeakSpots, want)
	}
}

func readObservations(t *testing.T, path string) *ingest.Observations {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	o, err := ingest.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestImportErrors(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "missing tool"},
		{[]string{"jest", in}, "unknown tool"},
		{[]string{"stryker"}, "exactly one input"},
		{[]string{"istanbul", in}, "-test is required"},
		{[]string{"junit", in}, "-runner is required"},
		{[]string{"llvm-cov", "-test", "x", "-exclude-fn", "(", in}, "-exclude-fn"},
		{[]string{"istanbul", "-test", "x", "-exclude-files", "(", in}, "-exclude-files"},
		{[]string{"llvm-cov", "-test", "x", "-files", "(", in}, "-files"},
		{[]string{"stryker", filepath.Join(dir, "missing.json")}, "no such file"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(t.Context(), append([]string{"import"}, tc.args...), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("import %q: error %v, want %q", tc.args, err, tc.want)
		}
	}
}
