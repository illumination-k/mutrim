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

func TestRunRejectsFailingBaseline(t *testing.T) {
	bin, mutants := buildFixture(t)
	_, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir, Args: []string{"-test.run", "NoSuchTest", "-test.v"}})
	if err != nil {
		t.Fatalf("a run with no tests must still pass the baseline: %v", err)
	}
	_, err = runner.Run(t.Context(), runner.Options{TestBin: "/nonexistent/test.bin", Mutants: mutants})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}
