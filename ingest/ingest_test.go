package ingest_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/ingest"
)

// The testdata are trimmed from real runs over two small suites, one per
// language, testing the same clamp and sign functions: StrykerJS 10 with
// vitest 4, and cargo-mutants 27 (with cargo test and with cargo nextest)
// with cargo llvm-cov. The trimming only drops what the adapters do not
// read, and the paths are rewritten to /project.

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(filepath.Join("testdata", name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// rows indexes the observations by test name.
func rows(o *ingest.Observations) map[string]ingest.Test {
	out := map[string]ingest.Test{}
	for _, t := range o.Tests {
		out[t.Name] = t
	}
	return out
}

func names(o *ingest.Observations) []string {
	out := make([]string, 0, len(o.Tests))
	for _, t := range o.Tests {
		out = append(out, t.Name)
	}
	return out
}

func TestStryker(t *testing.T) {
	o, err := ingest.Stryker(read(t, "stryker.json"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Source != "stryker" {
		t.Errorf("source = %q", o.Source)
	}
	// Rows are "<test file>#<name>", the describe block and the test
	// joined by a space.
	want := []string{
		"src/calc.test.ts#clamp high", "src/calc.test.ts#clamp low", "src/calc.test.ts#clamp mid",
		"src/calc.test.ts#clamp mid again", "src/calc.test.ts#sign pos", "src/other.test.ts#sign neg",
	}
	if got := names(o); !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	r := rows(o)
	low := r["src/calc.test.ts#clamp low"]
	if want := []string{"src/calc.ts:1:66-5:2", "src/calc.ts:2:7-2:13"}; !slices.Equal(low.Sites, want) {
		t.Errorf("clamp low sites = %q, want %q", low.Sites, want)
	}
	if want := []string{
		"src/calc.ts:1:66: BlockStatement {}",
		"src/calc.ts:2:7: ConditionalExpression false",
		"src/calc.ts:2:7: EqualityOperator x >= lo",
	}; !slices.Equal(low.Kills, want) {
		t.Errorf("clamp low kills = %q, want %q", low.Kills, want)
	}
	// With disableBail every killer is listed: both clamp mid tests kill
	// the same mutants.
	if mid, again := r["src/calc.test.ts#clamp mid"], r["src/calc.test.ts#clamp mid again"]; !slices.Equal(mid.Kills, again.Kills) || len(mid.Kills) != 5 {
		t.Errorf("clamp mid kills %q, clamp mid again %q", mid.Kills, again.Kills)
	}
	// Survived and NoCoverage mutants are the survivors.
	if len(o.Survived) != 6 || !slices.Contains(o.Survived, `src/calc.ts:10:10: StringLiteral ""`) {
		t.Errorf("survived = %q", o.Survived)
	}
}

func TestStrykerErrors(t *testing.T) {
	for name, data := range map[string]string{
		"not json":     `{`,
		"no tests":     `{"files": {}}`,
		"unknown test": `{"testFiles": {"a.test.ts": {"tests": [{"id": "0", "name": "a"}]}}, "files": {"a.ts": {"mutants": [{"id": "1", "status": "Killed", "killedBy": ["9"]}]}}}`,
	} {
		if _, err := ingest.Stryker([]byte(data)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCargoMutants(t *testing.T) {
	for _, tool := range []string{"libtest", "nextest"} {
		t.Run(tool, func(t *testing.T) {
			o, err := ingest.CargoMutants(filepath.Join("testdata", "cargo-mutants-"+tool), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			// Rows are "<crate>::<path>": the library's unit tests and the
			// integration test file sign's tests; sign::pos kills nothing
			// but is a row, from the baseline.
			want := []string{"rsdemo::tests::high", "rsdemo::tests::low", "rsdemo::tests::mid", "rsdemo::tests::mid_again", "sign::neg", "sign::pos"}
			if got := names(o); !slices.Equal(got, want) {
				t.Fatalf("rows = %q, want %q", got, want)
			}
			r := rows(o)
			if want := []string{
				"src/lib.rs:2:10: replace < with == in clamp",
				"src/lib.rs:2:10: replace < with > in clamp",
				"src/lib.rs:2:5: replace clamp -> i32 with -1",
				"src/lib.rs:2:5: replace clamp -> i32 with 1",
			}; !slices.Equal(r["rsdemo::tests::low"].Kills, want) {
				t.Errorf("tests::low kills = %q, want %q", r["rsdemo::tests::low"].Kills, want)
			}
			if want := []string{"src/lib.rs:14:17: replace < with == in sign", "src/lib.rs:14:17: replace < with > in sign"}; !slices.Equal(r["sign::neg"].Kills, want) {
				t.Errorf("sign::neg kills = %q, want %q", r["sign::neg"].Kills, want)
			}
			if want := []string{"src/lib.rs:2:10: replace < with <= in clamp", "src/lib.rs:14:17: replace < with <= in sign"}; !slices.Equal(o.Survived, want) {
				t.Errorf("survived = %q, want %q", o.Survived, want)
			}
			// Only nextest reports durations.
			if got := r["rsdemo::tests::mid_again"].DurationMS; (tool == "nextest") != (got > 0) {
				t.Errorf("duration = %d", got)
			}
		})
	}
}

func TestCargoMutantsCaughtWithoutFailingTest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("outcomes.json", `{"outcomes": [
		{"scenario": "Baseline", "summary": "Success", "log_path": "log/baseline.log"},
		{"scenario": {"Mutant": {"name": "src/lib.rs:1:1: crash"}}, "summary": "CaughtMutant", "log_path": "log/crash.log"},
		{"scenario": {"Mutant": {"name": "src/lib.rs:2:1: slow"}}, "summary": "Timeout", "log_path": "log/slow.log"},
		{"scenario": {"Mutant": {"name": "src/lib.rs:3:1: bad"}}, "summary": "Unviable", "log_path": "log/bad.log"}
	]}`)
	write("log/baseline.log", "     Running `target/debug/deps/demo-0123456789abcdef`\ntest a ... ok\n")
	write("log/crash.log", "     Running `target/debug/deps/demo-0123456789abcdef`\nerror: test failed, to rerun pass `--lib`\n")
	var warn strings.Builder
	o, err := ingest.CargoMutants(dir, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Tests) != 1 || o.Tests[0].Name != "demo::a" || len(o.Tests[0].Kills) != 0 || len(o.Survived) != 0 {
		t.Errorf("observations = %+v", o)
	}
	if !strings.Contains(warn.String(), "src/lib.rs:1:1: crash") {
		t.Errorf("warning = %q", warn.String())
	}
	if _, err := ingest.CargoMutants(t.TempDir(), io.Discard); err == nil {
		t.Error("no outcomes.json: no error")
	}
}

func TestLLVMCov(t *testing.T) {
	tests := regexp.MustCompile(`(^|::)tests(::|$)`).MatchString
	testFiles := regexp.MustCompile(`(^|/)tests/`).MatchString
	o, err := ingest.LLVMCov(read(t, "llvm-cov.json"), "rsdemo::tests::high", ingest.Paths{Root: "/project", Exclude: testFiles}, tests)
	if err != nil {
		t.Fatal(err)
	}
	if o.Source != "llvm-cov" || len(o.Tests) != 1 || o.Tests[0].Name != "rsdemo::tests::high" {
		t.Fatalf("observations = %+v", o)
	}
	// clamp's executed regions, one per source range however many
	// binaries compiled it; not the test's own body (tests::high, at line
	// 30), nor clamp's unexecuted `return lo` (3:16).
	want := []string{"src/lib.rs:1:1-1:46", "src/lib.rs:2:8-2:14", "src/lib.rs:4:5-4:6", "src/lib.rs:5:8-5:14", "src/lib.rs:6:16-6:18", "src/lib.rs:9:1-9:2"}
	if got := o.Tests[0].Blocks; !slices.Equal(got, want) {
		t.Errorf("blocks = %q, want %q", got, want)
	}

	// Without the exclusions, the test's body is a block too.
	o, err = ingest.LLVMCov(read(t, "llvm-cov.json"), "rsdemo::tests::high", ingest.Paths{Root: "/project"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Tests[0].Blocks, "src/lib.rs:30:5-30:14") {
		t.Errorf("blocks without exclusions = %q", o.Tests[0].Blocks)
	}
	// Files outside the root are left out.
	o, err = ingest.LLVMCov(read(t, "llvm-cov.json"), "x", ingest.Paths{Root: "/elsewhere"}, nil)
	if err != nil || len(o.Tests[0].Blocks) != 0 {
		t.Errorf("blocks outside the root = %q, %v", o.Tests[0].Blocks, err)
	}

	if _, err := ingest.LLVMCov([]byte(`{"type": "other"}`), "x", ingest.Paths{}, nil); err == nil {
		t.Error("not an export: no error")
	}
}

func TestIstanbul(t *testing.T) {
	o, err := ingest.Istanbul(read(t, "istanbul.json"), "src/calc.test.ts#clamp high", ingest.Paths{Root: "/project"})
	if err != nil {
		t.Fatal(err)
	}
	// clamp's first two statements and `return hi`; the v8 provider ends
	// a statement ending its line in a null column.
	want := []string{"src/calc.ts:2:2-2:0", "src/calc.ts:3:14-3:0", "src/calc.ts:3:2-3:0"}
	if o.Source != "istanbul" || len(o.Tests) != 1 || !slices.Equal(o.Tests[0].Blocks, want) {
		t.Errorf("observations = %+v, want blocks %q", o, want)
	}
	if _, err := ingest.Istanbul([]byte(`[]`), "x", ingest.Paths{}); err == nil {
		t.Error("not a coverage map: no error")
	}
}

func TestJUnit(t *testing.T) {
	o, err := ingest.JUnit(read(t, "junit-vitest.xml"), "vitest")
	if err != nil {
		t.Fatal(err)
	}
	// vitest's "clamp > low" is the Stryker adapter's "clamp low".
	want := []string{
		"src/calc.test.ts#clamp high", "src/calc.test.ts#clamp low", "src/calc.test.ts#clamp mid",
		"src/calc.test.ts#clamp mid again", "src/calc.test.ts#sign pos", "src/other.test.ts#sign neg",
	}
	if got := names(o); !slices.Equal(got, want) {
		t.Errorf("vitest rows = %q, want %q", got, want)
	}
	if got := rows(o)["src/other.test.ts#sign neg"].DurationMS; got != 1 {
		t.Errorf("sign neg duration = %d, want 1", got)
	}

	o, err = ingest.JUnit(read(t, "junit-nextest.xml"), "nextest")
	if err != nil {
		t.Fatal(err)
	}
	// The integration test binary rsdemo::sign is the crate sign.
	want = []string{"rsdemo::tests::high", "rsdemo::tests::low", "rsdemo::tests::mid", "rsdemo::tests::mid_again", "sign::neg", "sign::pos"}
	if got := names(o); !slices.Equal(got, want) {
		t.Errorf("nextest rows = %q, want %q", got, want)
	}
	if got := rows(o)["sign::pos"].DurationMS; got != 6 {
		t.Errorf("sign::pos duration = %d, want 6", got)
	}

	if _, err := ingest.JUnit(nil, "jest"); err == nil {
		t.Error("unknown runner: no error")
	}
}

func TestRead(t *testing.T) {
	if !ingest.IsObservations([]byte(`{"source": "stryker", "tests": []}`)) {
		t.Error("observations not recognized")
	}
	// A report.json has no source.
	if ingest.IsObservations([]byte(`{"pkg": "a", "tests": [], "results": []}`)) || ingest.IsObservations([]byte(`{`)) {
		t.Error("report.json taken for observations")
	}
	o, err := ingest.Read([]byte(`{"source": "junit", "tests": [{"name": "a", "duration_ms": 3}]}`))
	if err != nil || o.Tests[0].DurationMS != 3 {
		t.Errorf("Read = %+v, %v", o, err)
	}
	if _, err := ingest.Read([]byte(`{"tests": []}`)); err == nil {
		t.Error("no source: no error")
	}
}
