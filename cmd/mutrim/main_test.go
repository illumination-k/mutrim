package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/criteria"
	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/report"
	"github.com/illumination-k/mutrim/runner"
)

const (
	fixture         = "../../mutator/testdata/killable"
	schemataFixture = "../../mutator/testdata/schemata"
)

func TestGenThenOverlay(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", fixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen: %v\n%s", err, stderr.String())
	}
	var mutants []mutator.Mutant
	if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
		t.Fatalf("gen output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(mutants) == 0 {
		t.Fatal("gen produced no mutants")
	}

	dir := t.TempDir()
	stdout.Reset()
	if err := run(t.Context(), []string{"overlay", "-id", mutants[0].ID, "-dir", dir, fixture}, &stdout, &stderr); err != nil {
		t.Fatalf("overlay: %v\n%s", err, stderr.String())
	}
	var overlay mutator.Overlay
	if err := json.Unmarshal(stdout.Bytes(), &overlay); err != nil {
		t.Fatalf("overlay output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(overlay.Replace) != 1 {
		t.Fatalf("want one replacement, got %v", overlay.Replace)
	}
	for orig, mutated := range overlay.Replace {
		if filepath.Base(orig) != "killable.go" || filepath.Dir(mutated) != dir {
			t.Errorf("unexpected replacement %s -> %s", orig, mutated)
		}
		if _, err := os.Stat(mutated); err != nil {
			t.Error(err)
		}
	}
}

// The Phase 2 pipeline end to end: gen -schemata, go test -c with the
// overlay, run, and a report on stdout whose totals match the runner's
// own golden.
func TestGenSchemataThenRun(t *testing.T) {
	dir := t.TempDir()
	mutantsPath := filepath.Join(dir, "mutants.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-schemata", dir, "-o", mutantsPath, schemataFixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -schemata: %v\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("gen -o must not write to stdout: %s", stdout.String())
	}
	var mutants []mutator.Mutant
	if err := readJSON(mutantsPath, &mutants); err != nil {
		t.Fatal(err)
	}
	var overlay mutator.Overlay
	if err := readJSON(filepath.Join(dir, "overlay.json"), &overlay); err != nil {
		t.Fatal(err)
	}
	if len(overlay.Replace) != 1 {
		t.Fatalf("want one overlay entry, got %v", overlay.Replace)
	}
	for _, mutated := range overlay.Replace {
		if !strings.HasPrefix(mutated, filepath.Join(dir, "github.com", "illumination-k", "mutrim")) {
			t.Errorf("schemata source not under the package path: %s", mutated)
		}
	}

	bin := filepath.Join(dir, "schemata.test")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", filepath.Join(dir, "overlay.json"), "-o", bin, schemataFixture) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}

	t.Setenv("TEST_TOTAL_SHARDS", "2")
	t.Setenv("TEST_SHARD_INDEX", "1")
	statusFile := filepath.Join(dir, "shard_status")
	t.Setenv("TEST_SHARD_STATUS_FILE", statusFile)
	stdout.Reset()
	args := []string{"run", "-test-bin", bin, "-mutants", mutantsPath, "-dir", schemataFixture, "-timeout", "1s", "--", "-test.short"}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	var report runner.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("run output is not JSON: %v\n%s", err, stdout.String())
	}
	if _, err := os.Stat(statusFile); err != nil {
		t.Errorf("shard status file not touched: %v", err)
	}
	if n := len(report.Results); n == 0 || n >= len(mutants) {
		t.Errorf("shard 1 of 2 ran %d of %d mutants", n, len(mutants))
	}
	if report.Totals.Killed == 0 || !strings.Contains(stderr.String(), "KILLED") {
		t.Errorf("expected killed mutants and a log line per mutant:\n%s", stderr.String())
	}

	// Under Bazel the report goes to the undeclared outputs; with -previous
	// every result is copied forward instead of executed.
	outDir := filepath.Join(dir, "outputs")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", outDir)
	first := filepath.Join(dir, "first.json")
	if err := writeJSON(first, nil, report); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(report.Tests))
	for i, tt := range report.Tests {
		names[i] = tt.Name
	}
	stdout.Reset()
	args = append(args[:len(args)-2], "-previous", first, "-tests", strings.Join(names, ","))
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run -previous: %v\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("report must go to TEST_UNDECLARED_OUTPUTS_DIR, not stdout: %s", stdout.String())
	}
	var second runner.Report
	if err := readJSON(filepath.Join(outDir, "report.json"), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != len(report.Results) {
		t.Fatalf("second run has %d results, first %d", len(second.Results), len(report.Results))
	}
	if len(second.Tests) != len(names) {
		t.Errorf("-tests named %d tests, %d ran", len(names), len(second.Tests))
	}
	for i, r := range second.Results {
		if prev := report.Results[i]; r.MutantID != prev.MutantID || r.Status != prev.Status || r.DurationMS != prev.DurationMS {
			t.Errorf("result not copied forward from -previous: %+v vs %+v", r, prev)
		}
	}

	// -in-diff scopes the run to the lines a diff adds, before -previous is
	// consulted: a diff that touches no source of the package leaves every
	// mutant SKIPPED and the score at zero.
	diffPath := filepath.Join(dir, "pr.diff")
	diff := "--- a/other/file.go\n+++ b/other/file.go\n@@ -1,1 +1,2 @@\n package other\n+var x = 1\n"
	if err := os.WriteFile(diffPath, []byte(diff), 0o600); err != nil {
		t.Fatal(err)
	}
	scoped := filepath.Join(dir, "scoped.json")
	stdout.Reset()
	args = append(args[:len(args):len(args)], "-in-diff", diffPath, "-out", scoped)
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run -in-diff: %v\n%s", err, stderr.String())
	}
	var third runner.Report
	if err := readJSON(scoped, &third); err != nil {
		t.Fatal(err)
	}
	if len(third.Results) != len(report.Results) || third.Totals.Skipped != len(third.Results) || third.Totals.Score != 0 {
		t.Errorf("an unrelated diff must skip every mutant: %+v", third.Totals)
	}

	// bazel-test, the test executable of mutation_test, resolves runfiles
	// paths, reads MUTRIM_IN_DIFF, and writes every output next to
	// report.json.
	runfilesDir := filepath.Join(dir, "runfiles")
	if err := os.MkdirAll(filepath.Join(runfilesDir, "_main"), 0o750); err != nil {
		t.Fatal(err)
	}
	libSrc, err := filepath.Abs(filepath.Join(schemataFixture, "schemata.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{bin, mutantsPath, libSrc} {
		if err := os.Symlink(f, filepath.Join(runfilesDir, "_main", filepath.Base(f))); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RUNFILES_DIR", runfilesDir)
	t.Setenv("MUTRIM_IN_DIFF", diffPath)
	stdout.Reset()
	args = []string{"bazel-test", "-test-bin", "_main/schemata.test", "-mutants", "_main/mutants.json", "_main/schemata.go", "--", "-timeout", "1s"}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("bazel-test: %v\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("bazel-test must write to TEST_UNDECLARED_OUTPUTS_DIR, not stdout: %s", stdout.String())
	}
	var fourth runner.Report
	if err := readJSON(filepath.Join(outDir, "report.json"), &fourth); err != nil {
		t.Fatal(err)
	}
	if fourth.Totals.Skipped != len(fourth.Results) {
		t.Errorf("bazel-test must pass MUTRIM_IN_DIFF to run: %+v", fourth.Totals)
	}
	for _, f := range []string{"minimize.json", "mutation-report.json", "mutation-report.html"} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Error(err)
		}
	}
}

// Bazel mode: gen type-checks the given files from export data instead of
// running go list, and the schemata directory holds the complete package,
// files without a mutant copied as they are.
func TestGenFromFiles(t *testing.T) {
	dir := t.TempDir()
	mutantsPath := filepath.Join(dir, "mutants.json")
	const importPath = "github.com/illumination-k/mutrim/mutator/testdata/killable"
	var stdout, stderr bytes.Buffer
	args := []string{"gen", "-importpath", importPath, "-tags", "mutrimtag,other", "-schemata", dir, "-o", mutantsPath, filepath.Join(fixture, "killable.go")}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("gen -importpath: %v\n%s", err, stderr.String())
	}
	var mutants []mutator.Mutant
	if err := readJSON(mutantsPath, &mutants); err != nil {
		t.Fatal(err)
	}
	if len(mutants) == 0 || mutants[0].Pkg != importPath {
		t.Fatalf("unexpected mutants: %+v", mutants)
	}
	src, err := os.ReadFile(filepath.Clean(filepath.Join(dir, filepath.FromSlash(importPath), "killable.go")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), mutator.RuntimePath) {
		t.Errorf("schemata source does not import the runtime:\n%s", src)
	}

	// The excluded fixture has one mutable file and three that gen leaves
	// alone; all four end up in the schemata directory.
	stdout.Reset()
	excluded := "../../mutator/testdata/excluded"
	if err := run(t.Context(), []string{"gen", "-schemata", dir, "-o", mutantsPath, excluded}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -schemata: %v\n%s", err, stderr.String())
	}
	var overlay mutator.Overlay
	if err := readJSON(filepath.Join(dir, "overlay.json"), &overlay); err != nil {
		t.Fatal(err)
	}
	if len(overlay.Replace) != 4 {
		t.Errorf("want every package file in the overlay, got %v", overlay.Replace)
	}
	for orig, copied := range overlay.Replace {
		want, err := os.ReadFile(filepath.Clean(orig))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Clean(copied))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(orig) != "normal.go" && string(got) != string(want) {
			t.Errorf("%s was rewritten although it has no mutant", orig)
		}
	}
}

// Without a package argument gen mutates the current directory, and a
// stdout that cannot be written is an error.
func TestGenDefaultsToCurrentDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-no-check"}, &stdout, &stderr); err != nil {
		t.Fatalf("gen: %v\n%s", err, stderr.String())
	}
	var mutants []mutator.Mutant
	if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
		t.Fatalf("gen output is not JSON: %v", err)
	}
	if len(mutants) == 0 || mutants[0].Pkg != "github.com/illumination-k/mutrim/cmd/mutrim" {
		t.Errorf("expected mutants of this package, got %+v", mutants)
	}
	if err := run(t.Context(), []string{"gen", "-no-check"}, failingWriter{}, &stderr); err == nil {
		t.Error("unwritable stdout: expected an error")
	}
}

// -operators restricts the mutants to the named operators.
func TestGenOperators(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-operators", "relational,return", fixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -operators: %v\n%s", err, stderr.String())
	}
	var mutants []mutator.Mutant
	if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
		t.Fatal(err)
	}
	if len(mutants) == 0 {
		t.Fatal("no mutants")
	}
	for _, m := range mutants {
		if m.Operator != "relational" && m.Operator != "return" {
			t.Errorf("unexpected operator %s", m.Operator)
		}
	}
}

// The gen filters reject sites without dropping them: a filtered mutant
// stays in mutants.json, marked with the rule that ignored it.
func TestGenFilters(t *testing.T) {
	gen := func(t *testing.T, args ...string) []mutator.Mutant {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), append(append([]string{"gen"}, args...), fixture), &stdout, &stderr); err != nil {
			t.Fatalf("gen %v: %v\n%s", args, err, stderr.String())
		}
		var mutants []mutator.Mutant
		if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
			t.Fatal(err)
		}
		if len(mutants) == 0 {
			t.Fatal("no mutants")
		}
		return mutants
	}

	all := gen(t)
	cases := map[string]struct {
		args   []string
		reason string
		kept   func(m mutator.Mutant) bool
	}{
		"match":         {[]string{"-match", "^Sign$"}, "match", func(m mutator.Mutant) bool { return m.Func == "Sign" }},
		"files":         {[]string{"-files", "killable.go"}, "files", func(mutator.Mutant) bool { return true }},
		"files none":    {[]string{"-files", "other/*.go"}, "files", func(mutator.Mutant) bool { return false }},
		"exclude files": {[]string{"-exclude-files", "killable"}, "exclude-files", func(mutator.Mutant) bool { return false }},
		"exclude re":    {[]string{"-exclude-re", "relational"}, "exclude-re", func(m mutator.Mutant) bool { return m.Operator != "relational" }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mutants := gen(t, tc.args...)
			if len(mutants) != len(all) {
				t.Fatalf("filtering changed the mutant count: %d -> %d", len(all), len(mutants))
			}
			for i, m := range mutants {
				want := ""
				if !tc.kept(m) {
					want = tc.reason
				}
				if m.Ignored != want {
					t.Errorf("%s %q: ignored=%q, want %q", m.Func, m.Description, m.Ignored, want)
				}
				if m.ID != all[i].ID {
					t.Errorf("%d: id %s -> %s", i, all[i].ID, m.ID)
				}
			}
		})
	}
}

// Arid nodes are ignored by default, without the flag being named, and
// -no-arid turns the built-in rules off.
func TestGenArid(t *testing.T) {
	const fixture = "../../mutator/testdata/arid"
	gen := func(t *testing.T, args ...string) []mutator.Mutant {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), append(append([]string{"gen"}, args...), fixture), &stdout, &stderr); err != nil {
			t.Fatalf("gen %v: %v\n%s", args, err, stderr.String())
		}
		var mutants []mutator.Mutant
		if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
			t.Fatal(err)
		}
		return mutants
	}

	ignored := 0
	for _, m := range gen(t) {
		if m.Ignored == "arid" {
			ignored++
		}
	}
	if ignored == 0 {
		t.Error("the built-in rules ignored no mutant of the arid fixture")
	}
	for _, m := range gen(t, "-no-arid") {
		if m.Ignored != "" && m.Ignored != "printf-format" {
			t.Errorf("%s %q: ignored=%q with -no-arid", m.Func, m.Description, m.Ignored)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	err := run(t.Context(), []string{"bogus"}, &bytes.Buffer{}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error, got %v", err)
	}
}

// Every command reports bad input as an error instead of writing partial
// JSON.
func TestCommandErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportFile := filepath.Join(dir, "report.json")
	if err := writeJSON(reportFile, nil, runner.Report{Pkg: "example.com/a"}); err != nil {
		t.Fatal(err)
	}
	mutantsFile := filepath.Join(dir, "mutants.json")
	if err := writeJSON(mutantsFile, nil, []mutator.Mutant{}); err != nil {
		t.Fatal(err)
	}
	const notADir = "/dev/null/x"
	cases := map[string][]string{
		"no command":                 {},
		"gen bad package":            {"gen", "./does/not/exist"},
		"gen bad flag":               {"gen", "-bogus"},
		"gen importpath no files":    {"gen", "-importpath", "example.com/x"},
		"gen importpath bad file":    {"gen", "-importpath", "example.com/x", missing},
		"gen bad match":              {"gen", "-match", "(", fixture},
		"gen bad files glob":         {"gen", "-files", "[-]", fixture},
		"gen bad exclude files":      {"gen", "-exclude-files", "(", fixture},
		"gen bad exclude re":         {"gen", "-exclude-re", "(", fixture},
		"gen bad arid":               {"gen", "-arid", "[-]", fixture},
		"gen unwritable output":      {"gen", "-o", notADir, fixture},
		"gen unwritable schemata":    {"gen", "-schemata", notADir, fixture},
		"overlay bad flag":           {"overlay", "-bogus"},
		"overlay without id":         {"overlay", fixture},
		"overlay bad package":        {"overlay", "-id", "0000000000000000", "./does/not/exist"},
		"overlay unknown id":         {"overlay", "-id", "0000000000000000", fixture},
		"run bad flag":               {"run", "-bogus"},
		"run without flags":          {"run"},
		"run missing mutants file":   {"run", "-test-bin", "x.test", "-mutants", missing},
		"run malformed mutants":      {"run", "-test-bin", "x.test", "-mutants", malformed},
		"run missing previous":       {"run", "-test-bin", "x.test", "-mutants", missing, "-previous", missing},
		"run missing in-diff":        {"run", "-test-bin", "x.test", "-mutants", mutantsFile, "-in-diff", missing},
		"run bad confirm-kills":      {"run", "-confirm-kills", "many", "-test-bin", "x.test", "-mutants", mutantsFile},
		"run bad confirm-baseline":   {"run", "-confirm-baseline", "many", "-test-bin", "x.test", "-mutants", mutantsFile},
		"run bad extra-test":         {"run", "-extra-test", "x.test", "-test-bin", "x.test", "-mutants", mutantsFile},
		"bazel-test bad flag":        {"bazel-test", "-bogus"},
		"bazel-test without flags":   {"bazel-test"},
		"bazel-test without outputs": {"bazel-test", "-test-bin", "x.test", "-mutants", mutantsFile},
		"minimize bad flag":          {"minimize", "-bogus"},
		"minimize no report":         {"minimize"},
		"minimize missing report":    {"minimize", missing},
		"minimize bad keep":          {"minimize", "-keep", "(", missing},
		"minimize missing srcs":      {"minimize", "-srcs", missing, reportFile},
		"minimize missing mutants":   {"minimize", "-mutants", missing, reportFile},
		"minimize unwritable":        {"minimize", "-o", notADir, reportFile},
		"minimize unwritable matrix": {"minimize", "-matrix", notADir, reportFile},
		"report bad flag":            {"report", "-bogus"},
		"report without mutants":     {"report", reportFile},
		"report no report":           {"report", "-mutants", missing},
		"report missing mutants":     {"report", "-mutants", missing, reportFile},
		"report malformed mutants":   {"report", "-mutants", malformed, reportFile},
		"report missing report":      {"report", "-mutants", mutantsFile, missing},
		"report missing srcs":        {"report", "-mutants", mutantsFile, "-srcs", missing, reportFile},
		"report unknown format":      {"report", "-format", "sarif", "-mutants", mutantsFile, reportFile},
		"report unwritable":          {"report", "-o", notADir, "-mutants", mutantsFile, reportFile},
		"report unwritable html":     {"report", "-format", "html", "-o", notADir, "-mutants", mutantsFile, reportFile},
	}
	for name, args := range cases {
		var stdout bytes.Buffer
		if err := run(t.Context(), args, &stdout, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: wrote to stdout on error: %s", name, stdout.String())
		}
	}
}

func TestShardEnvRejectsBadValues(t *testing.T) {
	t.Setenv("TEST_TOTAL_SHARDS", "two")
	if _, _, err := shardEnv(); err == nil {
		t.Error("TEST_TOTAL_SHARDS=two: expected an error")
	}
	t.Setenv("TEST_TOTAL_SHARDS", "2")
	t.Setenv("TEST_SHARD_INDEX", "")
	if _, _, err := shardEnv(); err == nil {
		t.Error("empty TEST_SHARD_INDEX: expected an error")
	}
	t.Setenv("TEST_SHARD_INDEX", "1")
	if index, total, err := shardEnv(); err != nil || index != 1 || total != 2 {
		t.Errorf("shardEnv() = %d, %d, %v", index, total, err)
	}
	t.Setenv("TEST_SHARD_STATUS_FILE", "/dev/null/x")
	if _, _, err := shardEnv(); err == nil {
		t.Error("unwritable TEST_SHARD_STATUS_FILE: expected an error")
	}
}

// The Phase 4 pipeline: run writes the per-test kill matrix, minimize
// composes it and reports the redundant test, the protected ones and the
// function no test reaches.
func TestMinimize(t *testing.T) {
	dir := t.TempDir()
	mutantsPath := filepath.Join(dir, "mutants.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-schemata", dir, "-o", mutantsPath, schemataFixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -schemata: %v\n%s", err, stderr.String())
	}
	bin := filepath.Join(dir, "schemata.test")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", filepath.Join(dir, "overlay.json"), "-o", bin, schemataFixture) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
	reportPath := filepath.Join(dir, "report.json")
	if err := run(t.Context(), []string{"run", "-test-bin", bin, "-mutants", mutantsPath, "-dir", schemataFixture, "-timeout", "1s", "-out", reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}

	matrixPath := filepath.Join(dir, "matrix.json")
	args := []string{"minimize", "-mutants", mutantsPath, "-srcs", schemataFixture, "-matrix", matrixPath, reportPath}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("minimize output is not JSON: %v\n%s", err, stdout.String())
	}
	protected := map[string]bool{}
	for _, s := range result.Selected {
		if s.Protected {
			protected[s.Name] = true
		}
	}
	if len(protected) != 2 || !protected["TestRegression_Sign"] || !protected["TestTagged"] {
		t.Errorf("protected = %v, want TestRegression_Sign (by name) and TestTagged (by tag)", protected)
	}
	if len(result.Selected) != 13 {
		t.Errorf("selected = %+v, want the 13 non-redundant tests", result.Selected)
	}
	if len(result.Redundant) != 1 || result.Redundant[0].Name != "TestLessRedundant" || strings.Join(result.Redundant[0].SubsumedBy, ",") != "TestComparisons" {
		t.Errorf("redundant = %+v, want TestLessRedundant subsumed by TestComparisons", result.Redundant)
	}
	if len(result.WeakSpots) != 1 || result.WeakSpots[0].Func != "Untested" || result.WeakSpots[0].NoCoverage != 4 {
		t.Errorf("weak_spots = %+v, want Untested", result.WeakSpots)
	}

	var matrix criteria.Matrix
	if err := readJSON(matrixPath, &matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Tests) != 14 || len(matrix.Requirements) == 0 {
		t.Errorf("matrix has %d tests and %d requirements", len(matrix.Tests), len(matrix.Requirements))
	}
	kills, sites := 0, 0
	for _, r := range matrix.Requirements {
		switch {
		case strings.HasPrefix(r.Label, "kill:") && r.Weight == 5:
			kills++
		case strings.HasPrefix(r.Label, "site:") && r.Weight == 1:
			sites++
		default:
			t.Errorf("unexpected requirement %+v", r)
		}
	}
	if kills == 0 || sites < kills {
		t.Errorf("matrix has %d kills and %d sites", kills, sites)
	}

	// Without -srcs the tag is unknown, and the shards of a package can be
	// passed together.
	stdout.Reset()
	if err := run(t.Context(), []string{"minimize", "-keep", "^$", reportPath, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize without sources: %v\n%s", err, stderr.String())
	}
	var unprotected minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &unprotected); err != nil {
		t.Fatal(err)
	}
	for _, s := range unprotected.Selected {
		if s.Protected {
			t.Errorf("%s protected without a rule", s.Name)
		}
	}
	if len(unprotected.Redundant) != 3 || len(unprotected.WeakSpots) != 0 {
		t.Errorf("without protection the by-name and tagged tests are redundant too: %+v, %+v", unprotected.Redundant, unprotected.WeakSpots)
	}
}

// `run -subtests` makes the subtests the rows: minimize then calls each
// case of TestLessRedundant redundant on its own, and the tag on TestTagged
// protects its cases. The run is narrowed to the tests involved.
func TestRunSubtestsThenMinimize(t *testing.T) {
	dir := t.TempDir()
	mutantsPath := filepath.Join(dir, "mutants.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-schemata", dir, "-o", mutantsPath, schemataFixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -schemata: %v\n%s", err, stderr.String())
	}
	bin := filepath.Join(dir, "schemata.test")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", filepath.Join(dir, "overlay.json"), "-o", bin, schemataFixture) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
	reportPath := filepath.Join(dir, "report.json")
	args := []string{"run", "-subtests", "-tests", "TestComparisons,TestArithmetic,TestLessRedundant,TestTagged", "-test-bin", bin, "-mutants", mutantsPath, "-dir", schemataFixture, "-timeout", "1s", "-out", reportPath}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run -subtests: %v\n%s", err, stderr.String())
	}
	var ran runner.Report
	if err := readJSON(reportPath, &ran); err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, tt := range ran.Tests {
		rows[tt.Name] = tt.Parent
	}
	want := map[string]string{"TestComparisons": "", "TestArithmetic": "", "TestLessRedundant/less": "TestLessRedundant", "TestLessRedundant/equal": "TestLessRedundant", "TestTagged/2+3": "TestTagged", "TestTagged/0+0": "TestTagged"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	for name, parent := range want {
		if p, ok := rows[name]; !ok || p != parent {
			t.Errorf("row %s: parent %q, %v; want %q", name, p, ok, parent)
		}
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"minimize", "-srcs", schemataFixture, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("minimize output is not JSON: %v\n%s", err, stdout.String())
	}
	protected := map[string]bool{}
	for _, s := range result.Selected {
		protected[s.Name] = s.Protected
	}
	wantProtected := map[string]bool{"TestTagged/2+3": true, "TestTagged/0+0": true, "TestComparisons": false, "TestArithmetic": false}
	if !maps.Equal(protected, wantProtected) {
		t.Errorf("selected = %v, want %v (the subtests of TestTagged protected by their parent's tag)", protected, wantProtected)
	}
	if len(result.Redundant) != 2 {
		t.Fatalf("redundant = %+v, want the two subtests of TestLessRedundant", result.Redundant)
	}
	for i, name := range []string{"TestLessRedundant/equal", "TestLessRedundant/less"} {
		if r := result.Redundant[i]; r.Name != name || strings.Join(r.SubsumedBy, ",") != "TestComparisons" {
			t.Errorf("redundant[%d] = %+v, want %s subsumed by TestComparisons", i, r, name)
		}
	}
}

// `report` renders a real run in each of its formats: the Stryker JSON
// with the source text of the mutated files, that JSON inside the HTML
// viewer, and an annotation per surviving mutant.
func TestReport(t *testing.T) {
	dir := t.TempDir()
	mutantsPath := filepath.Join(dir, "mutants.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"gen", "-schemata", dir, "-o", mutantsPath, schemataFixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen -schemata: %v\n%s", err, stderr.String())
	}
	bin := filepath.Join(dir, "schemata.test")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", filepath.Join(dir, "overlay.json"), "-o", bin, schemataFixture) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
	reportPath := filepath.Join(dir, "report.json")
	if err := run(t.Context(), []string{"run", "-test-bin", bin, "-mutants", mutantsPath, "-dir", schemataFixture, "-timeout", "1s", "-out", reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	var ran runner.Report
	if err := readJSON(reportPath, &ran); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"report", "-mutants", mutantsPath, "-srcs", schemataFixture, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("report: %v\n%s", err, stderr.String())
	}
	var stryker report.Stryker
	if err := json.Unmarshal(stdout.Bytes(), &stryker); err != nil {
		t.Fatalf("report output is not JSON: %v\n%s", err, stdout.String())
	}
	if stryker.SchemaVersion != "2" || len(stryker.Files) != 1 {
		t.Fatalf("stryker report = %+v", stryker)
	}
	mutants := 0
	for name, f := range stryker.Files {
		if !strings.HasSuffix(name, "schemata.go") {
			t.Errorf("unexpected file %q", name)
		}
		if !strings.Contains(f.Source, "package schemata") {
			t.Errorf("%s: source not read from -srcs: %q", name, f.Source)
		}
		mutants += len(f.Mutants)
	}
	if mutants != len(ran.Results) {
		t.Errorf("stryker has %d mutants, the run reported %d", mutants, len(ran.Results))
	}
	if got := len(stryker.TestFiles[""].Tests); got != len(ran.Tests) {
		t.Errorf("stryker has %d tests, the run reported %d", got, len(ran.Tests))
	}

	htmlPath := filepath.Join(dir, "mutation-report.html")
	stdout.Reset()
	if err := run(t.Context(), []string{"report", "-format", "html", "-mutants", mutantsPath, "-srcs", schemataFixture, "-o", htmlPath, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("report -format html: %v\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("-o must not write to stdout: %s", stdout.String())
	}
	html, err := os.ReadFile(htmlPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(html, []byte("<mutation-test-report-app")) || !bytes.Contains(html, []byte(`"schemaVersion":"2"`)) {
		t.Errorf("HTML view does not embed the report:\n%s", html)
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"report", "-format", "github", "-mutants", mutantsPath, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("report -format github: %v\n%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	survivors := ran.Totals.Lived + ran.Totals.NoCoverage
	if survivors == 0 || len(lines) != survivors {
		t.Fatalf("%d annotations for %d survivors:\n%s", len(lines), survivors, stdout.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "::warning file=") || !strings.Contains(line, "::") {
			t.Errorf("not a workflow command: %q", line)
		}
	}
}

// minimize leaves a flaky test out of the matrix altogether: it is neither
// selected nor called redundant, only listed as flaky, and the
// requirements only it reached disappear with it. A suspicious pair is no
// kill either, so it never becomes a requirement.
func TestMinimizeExcludesFlakyTests(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	if err := writeJSON(reportPath, nil, runner.Report{
		Pkg: "example.com/a",
		Tests: []runner.Test{
			{Name: "TestStable", DurationMS: 1, Sites: []string{"1", "2"}},
			{Name: "TestFlaky", DurationMS: 1, Sites: []string{"1", "2", "3"}, Flaky: true},
		},
		Results: []runner.Result{
			{MutantID: "1", Status: runner.Killed, KilledBy: []string{"TestStable"}},
			{MutantID: "2", Status: runner.Lived, SuspiciousBy: []string{"TestStable"}},
			{MutantID: "3", Status: runner.Killed, KilledBy: []string{"TestFlaky"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	matrixPath := filepath.Join(dir, "matrix.json")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"minimize", "-matrix", matrixPath, reportPath}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("minimize output is not JSON: %v\n%s", err, stdout.String())
	}
	if !slices.Equal(result.Flaky, []string{"TestFlaky"}) {
		t.Errorf("flaky_tests = %v, want TestFlaky", result.Flaky)
	}
	if len(result.Selected) != 1 || result.Selected[0].Name != "TestStable" || len(result.Redundant) != 0 {
		t.Errorf("selected = %+v, redundant = %+v; want TestStable alone and the flaky test nowhere", result.Selected, result.Redundant)
	}

	var matrix criteria.Matrix
	if err := readJSON(matrixPath, &matrix); err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(matrix.Requirements))
	for _, r := range matrix.Requirements {
		labels = append(labels, r.Label)
	}
	// Site 3 is only the flaky test's, and the kill of mutant 3 only its
	// own; mutant 2 has a suspicious failure, which is no kill.
	if want := []string{"kill:1", "site:1", "site:2"}; !slices.Equal(labels, want) {
		t.Errorf("requirements = %v, want %v", labels, want)
	}
	if len(matrix.Tests) != 1 || matrix.Tests[0].Name != "TestStable" {
		t.Errorf("matrix rows = %+v, want TestStable alone", matrix.Tests)
	}
}

// run -extra-test runs the tests of a package importing the mutated one
// against its mutants, and minimize takes the reports of both packages as
// one matrix: app's tests are one row each, whether a report names them
// bare (app's own) or qualified (lib's -extra-test).
func TestRunExtraTestThenMinimize(t *testing.T) {
	const (
		libDir = "../../runner/testdata/cross/lib"
		appDir = "../../runner/testdata/cross/app"
		libPkg = "github.com/illumination-k/mutrim/runner/testdata/cross/lib"
		appPkg = "github.com/illumination-k/mutrim/runner/testdata/cross/app"
	)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// build lowers pkgDir under its own directory and compiles the test
	// binaries of the given packages against it.
	build := func(name, pkgDir string, testDirs ...string) (mutantsPath string, bins []string) {
		t.Helper()
		sch := filepath.Join(dir, name)
		mutantsPath = filepath.Join(sch, "mutants.json")
		if err := os.MkdirAll(sch, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := run(t.Context(), []string{"gen", "-schemata", sch, "-o", mutantsPath, pkgDir}, &stdout, &stderr); err != nil {
			t.Fatalf("gen -schemata %s: %v\n%s", pkgDir, err, stderr.String())
		}
		for i, td := range testDirs {
			bin := filepath.Join(sch, strconv.Itoa(i)+".test")
			cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", filepath.Join(sch, "overlay.json"), "-o", bin, td) //nolint:gosec // test-controlled args
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go test -c: %v\n%s", err, out)
			}
			bins = append(bins, bin)
		}
		return mutantsPath, bins
	}
	libMutants, libBins := build("lib", libDir, libDir, appDir)
	appMutants, appBins := build("app", appDir, appDir)

	libReport := filepath.Join(dir, "lib.json")
	args := []string{"run", "-subtests", "-test-bin", libBins[0], "-extra-test", appPkg + "=" + libBins[1] + "," + appDir, "-mutants", libMutants, "-dir", libDir, "-timeout", "1s", "-out", libReport}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run -extra-test: %v\n%s", err, stderr.String())
	}
	appReport := filepath.Join(dir, "app.json")
	args = []string{"run", "-subtests", "-test-bin", appBins[0], "-mutants", appMutants, "-dir", appDir, "-timeout", "1s", "-out", appReport}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	var rep runner.Report
	if err := readJSON(libReport, &rep); err != nil {
		t.Fatal(err)
	}
	var killedByApp bool
	for _, r := range rep.Results {
		for _, k := range r.KilledBy {
			killedByApp = killedByApp || strings.HasPrefix(k, appPkg+".TestPercent/")
		}
	}
	if !killedByApp {
		t.Errorf("no mutant of lib killed by app's tests: %+v", rep.Results)
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"minimize", "-keep", "^TestPercent/above$", libReport, appReport}, &stdout, &stderr); err != nil {
		t.Fatalf("minimize: %v\n%s", err, stderr.String())
	}
	var result minimizeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("minimize output is not JSON: %v\n%s", err, stdout.String())
	}
	rows := map[string]bool{}
	for _, s := range result.Selected {
		rows[s.Name] = s.Protected
	}
	for _, r := range result.Redundant {
		rows[r.Name] = false
	}
	want := map[string]bool{
		libPkg + ".TestClampInside":   false,
		appPkg + ".TestPercent/below": false,
		appPkg + ".TestPercent/above": true, // -keep matches the name within its package
	}
	if !maps.Equal(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}

	// bazel-test takes the extra test from the manifest mutrim_relink
	// writes, with runfiles paths, and scans its test sources for tags.
	runfilesDir := filepath.Join(dir, "runfiles", "_main")
	if err := os.MkdirAll(runfilesDir, 0o750); err != nil {
		t.Fatal(err)
	}
	link := func(src, name string) {
		t.Helper()
		abs, err := filepath.Abs(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(abs, filepath.Join(runfilesDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	link(libBins[0], "lib.test")
	link(libBins[1], "app.test")
	link(libMutants, "mutants.json")
	link(filepath.Join(libDir, "lib.go"), "lib.go")
	link(filepath.Join(appDir, "app_test.go"), "app_test.go")
	manifest := filepath.Join(runfilesDir, "extra.json")
	if err := writeJSON(manifest, nil, relink{Pkg: appPkg, Bin: "_main/app.test", Srcs: []string{"_main/app_test.go"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runfilesDir, "bad.json"), nil, relink{Bin: "_main/app.test"}); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	t.Setenv("RUNFILES_DIR", filepath.Dir(runfilesDir))
	t.Setenv("TEST_UNDECLARED_OUTPUTS_DIR", outDir)
	t.Setenv("MUTRIM_IN_DIFF", "")
	args = []string{"bazel-test", "-test-bin", "_main/lib.test", "-mutants", "_main/mutants.json", "-extra-test", "_main/extra.json", "_main/lib.go", "--", "-timeout", "1s"}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("bazel-test -extra-test: %v\n%s", err, stderr.String())
	}
	var bazelRep runner.Report
	if err := readJSON(filepath.Join(outDir, "report.json"), &bazelRep); err != nil {
		t.Fatal(err)
	}
	var appRows int
	for _, tt := range bazelRep.Tests {
		if tt.Pkg == appPkg {
			appRows++
		}
	}
	if appRows != 1 {
		t.Errorf("bazel-test must run app's test as one row: %+v", bazelRep.Tests)
	}
	for _, m := range []string{"_main/bad.json", "_main/missing.json"} {
		args = []string{"bazel-test", "-test-bin", "_main/lib.test", "-mutants", "_main/mutants.json", "-extra-test", m, "_main/lib.go"}
		if err := run(t.Context(), args, &stdout, &stderr); err == nil {
			t.Errorf("bazel-test -extra-test %s: want an error", m)
		}
	}
}
