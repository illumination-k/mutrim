package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
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
	stdout.Reset()
	args = append(args[:len(args)-2], "-previous", first)
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
	for i, r := range second.Results {
		if prev := report.Results[i]; r.MutantID != prev.MutantID || r.Status != prev.Status || r.DurationMS != prev.DurationMS {
			t.Errorf("result not copied forward from -previous: %+v vs %+v", r, prev)
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
	args := []string{"gen", "-importpath", importPath, "-schemata", dir, "-o", mutantsPath, filepath.Join(fixture, "killable.go")}
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
	missing := filepath.Join(t.TempDir(), "missing.json")
	cases := map[string][]string{
		"no command":               {},
		"gen bad package":          {"gen", "./does/not/exist"},
		"gen bad flag":             {"gen", "-bogus"},
		"gen importpath no files":  {"gen", "-importpath", "example.com/x"},
		"gen importpath bad file":  {"gen", "-importpath", "example.com/x", missing},
		"overlay without id":       {"overlay", fixture},
		"overlay unknown id":       {"overlay", "-id", "0000000000000000", fixture},
		"run without flags":        {"run"},
		"run missing mutants file": {"run", "-test-bin", "x.test", "-mutants", missing},
		"run missing previous":     {"run", "-test-bin", "x.test", "-mutants", missing, "-previous", missing},
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
}
