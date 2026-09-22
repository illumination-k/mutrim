package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	var logs bytes.Buffer
	// The golden pins KILLED against TIMEOUT, and the looping mutant waits
	// out the whole timeout: 3s keeps a loaded machine from turning a kill
	// into a timeout without making the test much slower.
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin,
		Mutants: mutants,
		Dir:     fixtureDir,
		Timeout: 3 * time.Second,
		Log:     &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Pkg != "github.com/illumination-k/mutrim/mutator/testdata/schemata" || report.BaselineMS < 0 || report.TimeoutMS != 3000 {
		t.Errorf("unexpected report metadata: %+v", report)
	}
	if !strings.Contains(logs.String(), "timeout 3s, 10 tests\n") {
		t.Errorf("the baseline must run every test:\n%s", logs.String())
	}
	// Every top-level test ran on its own and reached some sites.
	reached := map[string]map[string]bool{}
	for _, tt := range report.Tests {
		reached[tt.Name] = map[string]bool{}
		for _, id := range tt.Sites {
			reached[tt.Name][id] = true
		}
		if len(tt.Sites) == 0 || tt.DurationMS < 0 {
			t.Errorf("test row %+v", tt)
		}
	}
	if len(reached) != 10 {
		t.Errorf("tests = %d rows, want 10", len(reached))
	}
	for _, r := range report.Results {
		switch r.Status {
		case runner.Killed:
			// Only the tests reaching the site run, and every one that fails is recorded.
			if r.TestsRun == 0 || len(r.KilledBy) == 0 || len(r.KilledBy) > r.TestsRun {
				t.Errorf("%s KILLED with tests_run=%d killed_by=%v", r.MutantID, r.TestsRun, r.KilledBy)
			}
			for _, name := range r.KilledBy {
				if !reached[name][r.MutantID] {
					t.Errorf("%s killed by %s, which does not reach it", r.MutantID, name)
				}
			}
		case runner.Lived:
			t.Errorf("%s LIVED: every reached mutant of the fixture is killed", r.MutantID)
		case runner.NoCoverage, runner.NotViable:
			if r.TestsRun != 0 || r.DurationMS != 0 || len(r.KilledBy) != 0 {
				t.Errorf("%s %s was executed: %+v", r.MutantID, r.Status, r)
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
	if tot.Mutants != len(mutants) || tot.Killed+tot.Lived+tot.Timeout+tot.NoCoverage+tot.NotViable != tot.Mutants {
		t.Errorf("totals do not add up: %+v", tot)
	}
	if tot.Timeout != 1 || tot.Lived != 0 || tot.NoCoverage != 2 || tot.NotViable != 12 {
		t.Errorf("unexpected totals: %+v", tot)
	}
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.NoCoverage); tot.Score != want {
		t.Errorf("score = %v, want %v", tot.Score, want)
	}

	spots := runner.WeakSpots(mutants, report)
	if len(spots) != 1 || spots[0].Func != "Untested" || spots[0].NoCoverage != 2 || spots[0].Killed != 0 || spots[0].Line != 134 {
		t.Errorf("weak spots = %+v, want Untested with two unreached mutants", spots)
	}
	if none := runner.WeakSpots(nil, report); none == nil || len(none) != 0 {
		t.Errorf("weak spots of nothing = %#v, want an empty list (JSON [])", none)
	}
}

// Sharding partitions the mutants; incremental runs copy previous results
// forward instead of executing them; the tests allowlist narrows the run
// and leaves the other mutants unreached.
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
	// reappear untouched, while NOT_VIABLE is always recomputed and a
	// previously unreached mutant that a test now reaches is executed.
	var timedOut, notViable, reached string
	for _, res := range first.Results {
		switch {
		case res.Status == runner.Timeout:
			timedOut = res.MutantID
		case res.Status == runner.NotViable:
			notViable = res.MutantID
		case res.Status == runner.Killed && slices.Contains(res.KilledBy, "TestStatements"):
			reached = res.MutantID
		}
	}
	prev := &runner.Report{Results: []runner.Result{
		{MutantID: timedOut, Status: runner.Lived, DurationMS: 42},
		{MutantID: notViable, Status: runner.Killed},
		{MutantID: reached, Status: runner.NoCoverage},
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
		case reached:
			if res.Status != runner.Killed {
				t.Errorf("previously unreached mutant was not executed: %+v", res)
			}
		}
	}
	// With only TestStatements running, nothing reaches the comparison mutants.
	if second.Totals.NoCoverage <= first.Totals.NoCoverage || len(second.Tests) != 1 {
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
		if r.Status == runner.Killed && r.TestsRun != 1 {
			t.Errorf("%s: -test.run narrowing ran %d tests, want 1", r.MutantID, r.TestsRun)
		}
		if r.MutantID == killed && (r.Status != runner.Killed || !slices.Equal(r.KilledBy, []string{"TestComparisons"})) {
			t.Errorf("%s: want KILLED by TestComparisons, got %s by %v", killed, r.Status, r.KilledBy)
		}
	}
}

func TestRunErrors(t *testing.T) {
	bin, mutants := buildFixture(t)
	empty, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Dir: fixtureDir, Args: []string{"-test.run", "NoSuchTest"}})
	if err != nil {
		t.Errorf("a run with no tests and no mutants must still pass the baseline: %v", err)
	} else if empty.Pkg != "" || len(empty.Results) != 0 {
		t.Errorf("report of nothing = %+v", empty)
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

// writeJSON writes v as indented JSON, the way mutrim run does.
func writeJSON(t *testing.T, path string, v any) error {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// A canceled context stops the run with its error instead of a verdict,
// and a binary that cannot list its tests is an error too.
func TestRunAbortsOnContextAndListFailure(t *testing.T) {
	bin, mutants := buildFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: got %v, want context.Canceled", err)
	}

	// The script passes as a test run but fails -test.list.
	script := filepath.Join(t.TempDir(), "nolist.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$1\" in -test.list) exit 1;; esac\nexit 0\n"), 0o700); err != nil { //nolint:gosec // it must be executable
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), runner.Options{TestBin: script}); err == nil || !strings.Contains(err.Error(), "list tests") {
		t.Errorf("failing -test.list: got %v, want a list error", err)
	}
}
