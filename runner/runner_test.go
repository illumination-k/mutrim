package runner_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtureDir = "../mutator/testdata/schemata"

// buildFixture lowers the schemata fixture and compiles its test binary.
func buildFixture(t *testing.T) (bin string, mutants []mutator.Mutant) {
	t.Helper()
	pkgs, err := mutator.Load(".", fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs[0]
	mutants = mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	sch, err := mutator.Lower(pkg, mutants)
	if err != nil {
		t.Fatal(err)
	}
	for i := range mutants {
		mutants[i].Viable = mutants[i].Viable && sch.Embedded[mutants[i].ID]
	}

	dir := t.TempDir()
	overlay := mutator.Overlay{Replace: map[string]string{}}
	for orig, src := range sch.Files {
		mutated := filepath.Join(dir, filepath.Base(orig))
		if werr := os.WriteFile(mutated, src, 0o600); werr != nil {
			t.Fatal(werr)
		}
		overlay.Replace[orig] = mutated
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "schemata.test")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", overlayPath, "-o", bin, fixtureDir) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
	return bin, mutants
}

func TestRunReportGolden(t *testing.T) {
	bin, mutants := buildFixture(t)
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin,
		Mutants: mutants,
		Dir:     fixtureDir,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineMS < 0 || report.TimeoutMS != 1000 {
		t.Errorf("unexpected timing metadata: %+v", report)
	}
	for _, r := range report.Results {
		switch r.Status {
		case runner.Lived:
			// The fixture has five top-level tests; a surviving mutant sees them all.
			if r.TestsRun != 5 || len(r.KilledBy) != 0 {
				t.Errorf("%s LIVED with tests_run=%d killed_by=%v", r.MutantID, r.TestsRun, r.KilledBy)
			}
		case runner.Killed:
			// failfast stops at the first failing test, so exactly one is recorded.
			if r.TestsRun == 0 || r.TestsRun > 5 || len(r.KilledBy) != 1 {
				t.Errorf("%s KILLED with tests_run=%d killed_by=%v", r.MutantID, r.TestsRun, r.KilledBy)
			}
		case runner.NotViable:
			if r.TestsRun != 0 || r.DurationMS != 0 {
				t.Errorf("%s NOT_VIABLE was executed: %+v", r.MutantID, r)
			}
		}
	}

	byID := map[string]mutator.Mutant{}
	for _, m := range mutants {
		byID[m.ID] = m
	}
	var b strings.Builder
	for _, r := range report.Results {
		m := byID[r.MutantID]
		fmt.Fprintf(&b, "%s:%d %s %q %s", filepath.Base(m.File), m.Line, m.Func, m.Description, r.Status)
		if r.Status == runner.Killed && len(r.KilledBy) == 0 {
			t.Errorf("%s %s: killed but no failing test recorded", m.Func, m.Description)
		}
		if len(r.KilledBy) > 0 {
			fmt.Fprintf(&b, " by %s", strings.Join(r.KilledBy, ","))
		}
		b.WriteString("\n")
	}
	got := b.String()
	golden := filepath.Join("testdata", "schemata.golden")
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
		t.Errorf("report differs from %s (run with -update to accept)\n--- got ---\n%s", golden, got)
	}

	tot := report.Totals
	if tot.Mutants != len(mutants) || tot.Killed+tot.Lived+tot.Timeout+tot.NotViable != tot.Mutants {
		t.Errorf("totals do not add up: %+v", tot)
	}
	if tot.Timeout != 1 || tot.Lived != 2 || tot.NotViable != 5 {
		t.Errorf("unexpected totals: %+v", tot)
	}
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.Lived); tot.Score != want {
		t.Errorf("score = %v, want %v", tot.Score, want)
	}
}

// Sharding partitions the mutants; incremental runs copy previous results
// forward instead of executing them; the tests allowlist narrows the run.
func TestRunShardsPreviousAndTests(t *testing.T) {
	bin, mutants := buildFixture(t)
	first, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	var shardIDs []string
	for shard := range 3 {
		r, rerr := runner.Run(t.Context(), runner.Options{
			TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second,
			Shard: shard, Shards: 3,
			Previous: first, // every mutant is cached, so the shard runs nothing
		})
		if rerr != nil {
			t.Fatal(rerr)
		}
		for _, res := range r.Results {
			shardIDs = append(shardIDs, res.MutantID)
		}
	}
	sort.Strings(shardIDs)
	allIDs := make([]string, 0, len(mutants))
	for _, m := range mutants {
		allIDs = append(allIDs, m.ID)
	}
	sort.Strings(allIDs)
	if strings.Join(shardIDs, ",") != strings.Join(allIDs, ",") {
		t.Errorf("shards do not partition the mutants:\n%v\n%v", shardIDs, allIDs)
	}

	// Copy-forward: seed the previous report with a fake status and see it
	// reappear untouched, while NOT_VIABLE is always recomputed.
	var timedOut, notViable string
	for _, res := range first.Results {
		switch res.Status {
		case runner.Timeout:
			timedOut = res.MutantID
		case runner.NotViable:
			notViable = res.MutantID
		}
	}
	prev := &runner.Report{Results: []runner.Result{
		{MutantID: timedOut, Status: runner.Lived, DurationMS: 42},
		{MutantID: notViable, Status: runner.Killed},
	}}
	start := time.Now()
	second, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, Previous: prev,
		Tests: []string{"TestStatements"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 900*time.Millisecond*time.Duration(len(mutants)) {
		t.Error("cached result was executed again")
	}
	for _, res := range second.Results {
		switch res.MutantID {
		case timedOut:
			if res.Status != runner.Lived || res.DurationMS != 42 {
				t.Errorf("previous result not copied forward: %+v", res)
			}
		case notViable:
			if res.Status != runner.NotViable {
				t.Errorf("non-viable mutant took its previous status: %+v", res)
			}
		}
	}
	// With only TestStatements running, the comparison mutants live.
	if second.Totals.Lived <= first.Totals.Lived {
		t.Errorf("tests allowlist did not narrow the run: %+v vs %+v", second.Totals, first.Totals)
	}
}

// The derived timeout is 3× the baseline with MinTimeout as the floor, and
// a GOMUTANT_ID inherited from the environment must not leak into the
// baseline run.
func TestRunDerivesTimeoutAndStripsEnv(t *testing.T) {
	bin, mutants := buildFixture(t)
	var killed string
	for _, m := range mutants {
		if m.Func == "Less" && m.Operator == "relational" {
			killed = m.ID
		}
	}
	t.Setenv("GOMUTANT_ID", killed)
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir,
		Tests: []string{"TestComparisons"}, // excludes the looping mutant, so no TIMEOUT wait
	})
	if err != nil {
		t.Fatalf("baseline must ignore the inherited GOMUTANT_ID: %v", err)
	}
	if want := max(3*report.BaselineMS, runner.MinTimeout.Milliseconds()); report.TimeoutMS != want {
		t.Errorf("timeout_ms = %d, want %d (baseline %dms)", report.TimeoutMS, want, report.BaselineMS)
	}
	for _, r := range report.Results {
		if r.Status == runner.Lived && r.TestsRun != 1 {
			t.Errorf("%s: -test.run narrowing ran %d tests, want 1", r.MutantID, r.TestsRun)
		}
		if r.MutantID == killed && r.Status != runner.Killed {
			t.Errorf("%s: want KILLED by TestComparisons, got %s", killed, r.Status)
		}
	}
}

func TestRunErrors(t *testing.T) {
	bin, mutants := buildFixture(t)
	if _, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir, Args: []string{"-test.run", "NoSuchTest"}}); err != nil {
		t.Errorf("a run with no tests must still pass the baseline: %v", err)
	}
	cases := map[string]runner.Options{
		"missing binary":   {TestBin: "/nonexistent/test.bin", Mutants: mutants},
		"no binary":        {Mutants: mutants},
		"bad mutant id":    {TestBin: bin, Mutants: []mutator.Mutant{{ID: "not-hex", Viable: true}}},
		"failing baseline": {TestBin: bin, Mutants: mutants, Dir: fixtureDir, Args: []string{"-test.run", "TestSkipped", "-test.failfast=false", "-test.count=1", "-test.timeout=1ns"}},
	}
	for name, opts := range cases {
		if _, err := runner.Run(t.Context(), opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestReadReportErrors(t *testing.T) {
	if _, err := runner.ReadReport(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file: expected an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ReadReport(bad); err == nil {
		t.Error("malformed JSON: expected an error")
	}
}
