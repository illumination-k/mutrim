package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
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

const (
	fixtureDir = "../mutator/testdata/schemata"
	// flakyDir holds a fixture whose tests fail on the first process of a
	// phase and pass afterwards; see its package comment.
	flakyDir = "./testdata/flaky"
	// runerrorDir holds a fixture whose library dies from outside its
	// tests: a run of the branch mutant of Die exits the process instead
	// of failing a test.
	runerrorDir = "./testdata/runerror"
	// equivalentDir holds a fixture with an equivalent, a suspect
	// equivalent and a plain surviving mutant.
	equivalentDir = "./testdata/equivalent"
)

// buildFixture lowers the schemata fixture and compiles its test binary.
func buildFixture(t testing.TB) (bin string, mutants []mutator.Mutant) {
	t.Helper()
	return buildPkg(t, fixtureDir)
}

// buildPkg lowers the package in dir and compiles its test binary.
func buildPkg(t testing.TB, dir string) (bin string, mutants []mutator.Mutant) {
	t.Helper()
	bin, mutants, _ = buildPkgOverlay(t, dir)
	return bin, mutants
}

// buildPkgOverlay is buildPkg, also returning the go build -overlay file
// that swaps in the schemata sources, so the test binaries of the
// packages importing dir can be built against them.
func buildPkgOverlay(t testing.TB, dir string) (bin string, mutants []mutator.Mutant, overlayPath string) {
	t.Helper()
	pkgs, err := mutator.Load(".", dir)
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
		if !mutants[i].Excluded() {
			mutants[i].Viable = mutants[i].Viable && sch.Embedded[mutants[i].ID]
		}
	}

	out := t.TempDir()
	overlay := mutator.Overlay{Replace: map[string]string{}}
	for orig, src := range sch.Files {
		mutated := filepath.Join(out, filepath.Base(orig))
		if werr := os.WriteFile(mutated, src, 0o600); werr != nil {
			t.Fatal(werr)
		}
		overlay.Replace[orig] = mutated
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	overlayPath = filepath.Join(out, "overlay.json")
	if err := os.WriteFile(overlayPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(out, "schemata.test")
	compileTest(t, overlayPath, bin, dir)
	return bin, mutants, overlayPath
}

// compileTest builds the test binary of the package in dir with overlay.
func compileTest(t testing.TB, overlay, bin, dir string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay", overlay, "-o", bin, dir) //nolint:gosec // test-controlled args
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
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
	if !strings.Contains(logs.String(), "timeout at most 3s, 14 tests\n") {
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
	if len(reached) != 14 {
		t.Errorf("tests = %d rows, want 14", len(reached))
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
	if tot.Mutants != len(mutants) || tot.Killed+tot.Lived+tot.Timeout+tot.NoCoverage+tot.NotViable+tot.Ignored != tot.Mutants {
		t.Errorf("totals do not add up: %+v", tot)
	}
	if tot.Timeout != 3 || tot.Lived != 0 || tot.NoCoverage != 4 || tot.NotViable != 24 || tot.Ignored != 4 {
		t.Errorf("unexpected totals: %+v", tot)
	}
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.NoCoverage); tot.Score != want {
		t.Errorf("score = %v, want %v", tot.Score, want)
	}

	// Disabled is not a weak spot: its mutants were suppressed, not survivors.
	var untested int // the line WeakSpots must report for Untested
	for _, m := range mutants {
		if m.Func == "Untested" {
			untested = m.Line
		}
	}
	spots := runner.WeakSpots(mutants, report)
	if len(spots) != 1 || spots[0].Func != "Untested" || spots[0].NoCoverage != 4 || spots[0].Killed != 0 || spots[0].Line != untested {
		t.Errorf("weak spots = %+v, want Untested with four unreached mutants at line %d", spots, untested)
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
	// The mutants picked must be ones TestStatements reaches, since the
	// second run below allows only that test.
	var timedOut, notViable, reached string
	for _, res := range first.Results {
		switch {
		case res.Status == runner.Timeout && slices.Contains(res.KilledBy, "TestStatements"):
			timedOut = res.MutantID
		case res.Status == runner.NotViable:
			notViable = res.MutantID
		case res.Status == runner.Killed && slices.Contains(res.KilledBy, "TestStatements"):
			reached = res.MutantID
		}
	}
	// The survivor is observed with the tests of the second run.
	prev := &runner.Report{Results: []runner.Result{
		{MutantID: timedOut, Status: runner.Lived, DurationMS: 42},
		{MutantID: notViable, Status: runner.Killed},
		{MutantID: reached, Status: runner.NoCoverage},
	}}
	for _, tt := range first.Tests {
		if tt.Name == "TestStatements" {
			prev.Tests = append(prev.Tests, tt)
		}
	}
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

// A previous result is copied forward only while the tests it was
// observed with are unchanged: rewriting a test re-executes the mutants it
// killed and the survivors it reaches, and leaves the rest cached.
func TestRunPreviousChecksTests(t *testing.T) {
	bin, mutants := buildFixture(t)
	srcs := []string{filepath.Join(fixtureDir, "schemata_test.go")}
	first, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, TestSrcs: srcs})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range first.Tests {
		if tt.Hash == "" {
			t.Fatalf("%s has no hash", tt.Name)
		}
	}

	const marker = 424242 // a cached result keeps it
	prev := *first
	prev.Results = slices.Clone(first.Results)
	for i := range prev.Results {
		prev.Results[i].DurationMS = marker
	}
	prev.Tests = slices.Clone(first.Tests)
	var rewritten runner.Test
	for i, tt := range prev.Tests {
		if tt.Name == "TestStatements" {
			prev.Tests[i].Hash = "rewritten"
			rewritten = tt
		}
	}
	second, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, TestSrcs: srcs, Previous: &prev})
	if err != nil {
		t.Fatal(err)
	}
	var rerun, cached int
	for _, res := range second.Results {
		if !res.Status.Executed() {
			continue
		}
		stale := slices.Contains(res.KilledBy, rewritten.Name)
		if len(res.KilledBy) == 0 {
			stale = slices.Contains(rewritten.Sites, res.MutantID)
		}
		switch {
		case stale && res.DurationMS == marker:
			t.Errorf("%s was copied forward though %s changed: %+v", res.MutantID, rewritten.Name, res)
		case !stale && res.DurationMS != marker:
			t.Errorf("%s was executed again though its tests are unchanged: %+v", res.MutantID, res)
		case stale:
			rerun++
		default:
			cached++
		}
	}
	if rerun == 0 || cached == 0 {
		t.Errorf("rerun %d, cached %d: want some of each", rerun, cached)
	}
}

// The derived timeout is capped at 3× the baseline with DefaultMinTimeout as the
// floor, each result records the one it ran under, and
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
	if want := max(3*report.BaselineMS, runner.DefaultMinTimeout.Milliseconds()); report.TimeoutMS != want {
		t.Errorf("timeout_ms = %d, want %d (baseline %dms)", report.TimeoutMS, want, report.BaselineMS)
	}
	for _, r := range report.Results {
		if r.Status.Executed() && (r.TimeoutMS < runner.DefaultMinTimeout.Milliseconds() || r.TimeoutMS > report.TimeoutMS) {
			t.Errorf("%s: timeout_ms = %d, want within [%d, %d]", r.MutantID, r.TimeoutMS, runner.DefaultMinTimeout.Milliseconds(), report.TimeoutMS)
		}
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
// and a Tests entry naming a subtest is refused.
func TestRunAbortsOnContextAndSubtestName(t *testing.T) {
	bin, mutants := buildFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, runner.Options{TestBin: bin, Mutants: mutants, Dir: fixtureDir}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: got %v, want context.Canceled", err)
	}
	if _, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Dir: fixtureDir, Tests: []string{"TestLoops/FirstEven"}}); err == nil {
		t.Error("a subtest in Tests: expected an error")
	}
}

// With Subtests every subtest is a row of its own, traced and named in
// killed_by, while a test without subtests stays a row; the rows reaching
// a mutant run in one process per parent.
func TestRunSubtests(t *testing.T) {
	bin, mutants := buildFixture(t)
	var logs bytes.Buffer
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: 3 * time.Second, Log: &logs,
		Subtests: true,
		Tests:    []string{"TestComparisons", "TestLessRedundant", "TestLoops"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), ", 5 tests\n") {
		t.Errorf("the baseline must count the rows:\n%s", logs.String())
	}
	names := make([]string, 0, len(report.Tests))
	parents := make([]string, 0, len(report.Tests))
	for _, tt := range report.Tests {
		names = append(names, tt.Name)
		parents = append(parents, tt.Parent)
		if len(tt.Sites) == 0 {
			t.Errorf("%s reached no site", tt.Name)
		}
	}
	// Rows come in the order the baseline started them: source order.
	wantNames := []string{"TestComparisons", "TestLoops/FirstEven", "TestLoops/CountUntil", "TestLessRedundant/less", "TestLessRedundant/equal"}
	wantParents := []string{"", "TestLoops", "TestLoops", "TestLessRedundant", "TestLessRedundant"}
	if !slices.Equal(names, wantNames) || !slices.Equal(parents, wantParents) {
		t.Errorf("tests = %v with parents %v, want %v with %v", names, parents, wantNames, wantParents)
	}

	byID := map[string]mutator.Mutant{}
	for _, m := range mutants {
		byID[m.ID] = m
	}
	for _, r := range report.Results {
		m := byID[r.MutantID]
		switch {
		case m.Func == "Less" && r.Status == runner.Killed:
			// Both subtests of TestLessRedundant reach Less, so three rows
			// run in two processes (one per parent); which subtest kills
			// depends on the mutant, and TestComparisons kills them all.
			if r.TestsRun != 3 || r.KilledBy[0] != "TestComparisons" || len(r.KilledBy) < 2 {
				t.Errorf("Less %q: tests_run=%d killed_by=%v", m.Description, r.TestsRun, r.KilledBy)
			}
			for _, k := range r.KilledBy[1:] {
				if k != "TestLessRedundant/less" && k != "TestLessRedundant/equal" {
					t.Errorf("Less %q: killed by %s", m.Description, k)
				}
			}
		case m.Func == "FirstEven" && r.Status.Executed():
			if r.TestsRun != 1 || !slices.Equal(r.KilledBy, []string{"TestLoops/FirstEven"}) {
				t.Errorf("FirstEven %q: %s tests_run=%d killed_by=%v, want the one subtest", m.Description, r.Status, r.TestsRun, r.KilledBy)
			}
		case m.Func == "CountUntil" && r.Status.Executed():
			if r.TestsRun != 1 || !slices.Equal(r.KilledBy, []string{"TestLoops/CountUntil"}) {
				t.Errorf("CountUntil %q: %s tests_run=%d killed_by=%v, want the one subtest", m.Description, r.Status, r.TestsRun, r.KilledBy)
			}
		}
	}
	var less, equal bool
	for _, r := range report.Results {
		less = less || slices.Contains(r.KilledBy, "TestLessRedundant/less")
		equal = equal || slices.Contains(r.KilledBy, "TestLessRedundant/equal")
	}
	if !less || !equal {
		t.Errorf("each subtest of TestLessRedundant kills some mutant of Less: less=%v equal=%v", less, equal)
	}
}

// A mutant a gen filter ignored is reported IGNORED without being run,
// even when it is viable and reached by a test.
func TestRunIgnoredMutants(t *testing.T) {
	bin, mutants := buildFixture(t)
	// The fixture disables one function with an inline directive, so those
	// mutants are ignored before the filter adds one of its own.
	byDirective := map[string]bool{}
	var ignored string
	for i, m := range mutants {
		if m.Ignored != "" {
			byDirective[m.ID] = true
			continue
		}
		if ignored == "" && m.Viable && m.Func == "Less" && m.Operator == "relational" {
			mutants[i].Ignored = "match"
			ignored = m.ID
		}
	}
	if ignored == "" {
		t.Fatal("no viable relational mutant of Less in the fixture")
	}
	if len(byDirective) == 0 {
		t.Fatal("no mutant ignored by a directive in the fixture")
	}

	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second,
		Tests: []string{"TestComparisons"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range report.Results {
		if res.MutantID == ignored {
			if res.Status != runner.Ignored {
				t.Errorf("status = %s, want %s", res.Status, runner.Ignored)
			}
			if res.TestsRun != 0 {
				t.Errorf("an ignored mutant ran %d tests", res.TestsRun)
			}
		} else if res.Status == runner.Ignored && !byDirective[res.MutantID] {
			t.Errorf("%s: unexpected %s", res.MutantID, res.Status)
		}
	}
	if want := len(byDirective) + 1; report.Totals.Ignored != want {
		t.Errorf("totals.ignored = %d, want %d", report.Totals.Ignored, want)
	}
}

// A diff scopes the run to the lines it adds: every other mutant is
// SKIPPED without being executed and counts towards no score.
func TestRunInDiff(t *testing.T) {
	bin, mutants := buildFixture(t)
	byID := map[string]mutator.Mutant{}
	var target mutator.Mutant
	for _, m := range mutants {
		byID[m.ID] = m
		if m.Func == "Less" && m.Operator == "relational" {
			target = m
		}
	}
	if target.ID == "" {
		t.Fatal("fixture has no relational mutant of Less")
	}
	// The diff names the file as the repository does, while the mutant's
	// file is absolute: only the added line has to line up.
	file := path.Join("mutator/testdata/schemata", filepath.Base(target.File))
	d, err := runner.ParseDiff(strings.NewReader(fmt.Sprintf(
		"--- a/%[1]s\n+++ b/%[1]s\n@@ -%[2]d,1 +%[2]d,1 @@\n-func Less(a, b int) bool { return a <= b }\n+func Less(a, b int) bool { return a < b }\n",
		file, target.Line,
	)))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, InDiff: d, Log: &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != len(mutants) {
		t.Errorf("results = %d, want one per mutant (%d)", len(report.Results), len(mutants))
	}
	var hit bool
	for _, res := range report.Results {
		m := byID[res.MutantID]
		onLine := m.Line <= target.Line && target.Line <= max(m.EndLine, m.Line)
		switch {
		case res.Status == runner.Skipped:
			if onLine {
				t.Errorf("%s at %s:%d-%d is on the changed line but was skipped", res.MutantID, m.Func, m.Line, m.EndLine)
			}
			if res.TestsRun != 0 || res.DurationMS != 0 || len(res.KilledBy) != 0 {
				t.Errorf("%s SKIPPED but executed: %+v", res.MutantID, res)
			}
		case !onLine:
			t.Errorf("%s at %s:%d is outside the diff but ran as %s", res.MutantID, m.Func, m.Line, res.Status)
		default:
			hit = true
			if !res.Changed {
				t.Errorf("%s ran on the changed line but is not marked changed", res.MutantID)
			}
		}
		if res.MutantID == target.ID && (res.Status != runner.Killed || !slices.Contains(res.KilledBy, "TestComparisons")) {
			t.Errorf("the mutant on the changed line = %+v, want KILLED by TestComparisons", res)
		}
	}
	if !hit {
		t.Error("the diff selected no mutant at all")
	}

	tot := report.Totals
	if tot.Skipped == 0 || tot.Killed == 0 {
		t.Errorf("totals = %+v, want some mutants skipped and some run", tot)
	}
	if tot.Killed+tot.Lived+tot.Timeout+tot.NoCoverage+tot.NotViable+tot.Ignored+tot.Skipped != tot.Mutants {
		t.Errorf("totals do not add up: %+v", tot)
	}
	// Skipped mutants stay out of the score, so it is the score of the diff.
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.Lived+tot.NoCoverage); tot.Score != want {
		t.Errorf("score = %v, want %v", tot.Score, want)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("%d of %d mutants skipped", tot.Skipped, tot.Mutants)) {
		t.Errorf("the log must say how much the diff dropped:\n%s", logs.String())
	}

	// A diff that touches nothing of the package runs nothing.
	empty, err := runner.ParseDiff(strings.NewReader("--- a/other.go\n+++ b/other.go\n@@ -1,1 +1,2 @@\n package other\n+var x = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	none, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, InDiff: empty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if none.Totals.Skipped != none.Totals.Mutants || none.Totals.Score != 0 {
		t.Errorf("an unrelated diff must skip everything: %+v", none.Totals)
	}
}

// -diff-expand widens a diff to the commit-relevant mutants: those on the
// changed lines, and every mutant a test reaching one of them reaches.
// Both are scored on their own in totals.diff.
func TestRunDiffExpand(t *testing.T) {
	bin, mutants := buildFixture(t)
	var target mutator.Mutant
	for _, m := range mutants {
		if m.Func == "Less" && m.Operator == "relational" {
			target = m
		}
	}
	if target.ID == "" {
		t.Fatal("fixture has no relational mutant of Less")
	}
	file := path.Join("mutator/testdata/schemata", filepath.Base(target.File))
	d, err := runner.ParseDiff(strings.NewReader(fmt.Sprintf(
		"--- a/%[1]s\n+++ b/%[1]s\n@@ -%[2]d,1 +%[2]d,1 @@\n-func Less(a, b int) bool { return a <= b }\n+func Less(a, b int) bool { return a < b }\n",
		file, target.Line,
	)))
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, InDiff: d, DiffExpand: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	changed := map[string]bool{}
	for _, m := range mutants {
		if d.Touches(m.File, m.Line, m.EndLine) {
			changed[m.ID] = true
		}
	}
	relevant := maps.Clone(changed)
	for _, tt := range report.Tests {
		if slices.ContainsFunc(tt.Sites, func(id string) bool { return changed[id] }) {
			for _, id := range tt.Sites {
				relevant[id] = true
			}
		}
	}
	var outside int
	for _, res := range report.Results {
		if res.Changed != changed[res.MutantID] {
			t.Errorf("%s: changed = %v, want %v", res.MutantID, res.Changed, changed[res.MutantID])
		}
		if (res.Status == runner.Skipped) == relevant[res.MutantID] {
			t.Errorf("%s: status %s, commit-relevant %v", res.MutantID, res.Status, relevant[res.MutantID])
		}
		if res.Status != runner.Skipped && !res.Changed {
			outside++
		}
	}
	if outside == 0 {
		t.Error("the expansion selected no mutant outside the changed lines")
	}

	tot := report.Totals
	if tot.Diff == nil {
		t.Fatalf("totals.diff is missing: %+v", tot)
	}
	if got, want := tot.Diff.ChangedLines.Mutants, len(changed); got != want {
		t.Errorf("changed_lines.mutants = %d, want %d", got, want)
	}
	if got, want := tot.Diff.CommitRelevant.Mutants, tot.Mutants-tot.Skipped; got != want {
		t.Errorf("commit_relevant.mutants = %d, want %d", got, want)
	}
	if tot.Diff.ChangedLines.Killed == 0 || tot.Diff.CommitRelevant.Killed < tot.Diff.ChangedLines.Killed {
		t.Errorf("totals.diff = %+v, want the changed line's kill counted in both", tot.Diff)
	}
}

// Confirmation reruns keep a flaky observation out of the kill matrix: a
// kill -confirm-kills cannot reproduce becomes suspicious, and a test
// -confirm-baseline finds unreliable is marked flaky and never credited
// with a kill at all. Without the reruns the flaky fixture looks like a
// broken suite in one run and a killed mutant in the next.
func TestRunConfirmsKillsAndBaseline(t *testing.T) {
	bin, mutants := buildPkg(t, flakyDir)
	var target, stable mutator.Mutant
	for _, m := range mutants {
		switch {
		case m.Viable && m.Func == "Unasserted" && m.Operator == "arithmetic" && target.ID == "":
			target = m
		case m.Viable && m.Func == "Compared" && stable.ID == "":
			stable = m
		}
	}
	if target.ID == "" || stable.ID == "" {
		t.Fatalf("fixture has no arithmetic mutant of Unasserted (%q) or mutant of Compared (%q)", target.ID, stable.ID)
	}
	t.Setenv("MUTRIM_FLAKY_MUTANT", target.ID)

	// Each Run needs its own state directory, since the fixture flakes
	// only on the first process that claims a step.
	run := func(t *testing.T, o runner.Options) (*runner.Report, error) {
		t.Helper()
		t.Setenv("MUTRIM_FLAKY_STATE", t.TempDir())
		o.TestBin, o.Mutants, o.Dir, o.Timeout = bin, mutants, flakyDir, 5*time.Second
		return runner.Run(t.Context(), o)
	}

	// One trace run per test cannot tell a flaky test from a broken one,
	// so the run stops, as it does for any test failing on its own.
	t.Run("unconfirmed baseline", func(t *testing.T) {
		if _, err := run(t, runner.Options{}); err == nil || !strings.Contains(err.Error(), "TestFlakyBaseline fails when run on its own") {
			t.Errorf("err = %v, want TestFlakyBaseline failing on its own", err)
		}
	})

	// Two trace runs disagree, so the test is flaky rather than broken;
	// its failure under the mutant is suspicious, while the kill of the
	// test that is not flaky is still recorded without -confirm-kills.
	t.Run("confirmed baseline only", func(t *testing.T) {
		rep, err := run(t, runner.Options{ConfirmBaseline: 2})
		if err != nil {
			t.Fatal(err)
		}
		flaky := map[string]bool{}
		for _, tt := range rep.Tests {
			flaky[tt.Name] = tt.Flaky
		}
		want := map[string]bool{"TestStable": false, "TestFlakyKill": false, "TestFlakyBaseline": true}
		if !maps.Equal(flaky, want) {
			t.Errorf("flaky rows = %v, want %v", flaky, want)
		}
		res := result(t, rep, target.ID)
		if res.Status != runner.Killed || !slices.Equal(res.KilledBy, []string{"TestFlakyKill"}) || !slices.Equal(res.SuspiciousBy, []string{"TestFlakyBaseline"}) {
			t.Errorf("the flaky mutant = %+v, want KILLED by TestFlakyKill with TestFlakyBaseline suspicious", res)
		}
		if got := result(t, rep, stable.ID); got.Status != runner.Killed || !slices.Equal(got.KilledBy, []string{"TestStable"}) {
			t.Errorf("the mutant of Compared = %+v, want KILLED by TestStable", got)
		}
		if rep.Totals.Suspicious != 1 {
			t.Errorf("totals.suspicious = %d, want 1", rep.Totals.Suspicious)
		}
	})

	// The rerun does not reproduce the remaining kill either, so nothing
	// is left to tell the mutant from the original and it LIVED.
	t.Run("confirmed kills", func(t *testing.T) {
		rep, err := run(t, runner.Options{ConfirmBaseline: 2, ConfirmKills: 2})
		if err != nil {
			t.Fatal(err)
		}
		res := result(t, rep, target.ID)
		if res.Status != runner.Lived || len(res.KilledBy) != 0 {
			t.Errorf("the flaky mutant = %+v, want LIVED with no killer", res)
		}
		if want := []string{"TestFlakyBaseline", "TestFlakyKill"}; !slices.Equal(slices.Sorted(slices.Values(res.SuspiciousBy)), want) {
			t.Errorf("suspicious_by = %v, want %v", res.SuspiciousBy, want)
		}
		// A real kill survives the rerun untouched.
		if got := result(t, rep, stable.ID); got.Status != runner.Killed || !slices.Equal(got.KilledBy, []string{"TestStable"}) || len(got.SuspiciousBy) != 0 {
			t.Errorf("the mutant of Compared = %+v, want KILLED by TestStable with nothing suspicious", got)
		}
	})
}

// A run that dies from outside the tests is a RUN_ERROR, not a kill: no
// test failed, so nothing the run observed says anything about the
// mutant. It counts towards no score, is no weak spot, and a -previous
// report never copies it forward: the next run executes the mutant
// again, and since its first execution claimed the exit, this one
// returns 0, the test fails, and the run is a regular kill instead.
func TestRunInfraError(t *testing.T) {
	bin, mutants := buildPkg(t, runerrorDir)
	var target mutator.Mutant
	for _, m := range mutants {
		if m.Viable && m.Func == "Die" && m.Operator == "branch" && target.ID == "" {
			target = m // the branch mutant of Die's first if, in source order
		}
	}
	if target.ID == "" {
		t.Fatal("fixture has no viable branch mutant of Die")
	}
	t.Setenv("MUTRIM_RUNERROR_STATE", t.TempDir())

	first, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: runerrorDir, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := result(t, first, target.ID)
	if res.Status != runner.RunError || res.TestsRun != 1 || len(res.KilledBy) != 0 {
		t.Fatalf("the branch mutant = %+v, want RUN_ERROR by no test", res)
	}
	if first.Totals.RunError == 0 {
		t.Errorf("totals.run_error = %d, want at least the branch mutant", first.Totals.RunError)
	}
	// The score counts nothing the run died from: RUN_ERROR is not in
	// its denominator.
	if want := float64(first.Totals.Killed+first.Totals.Timeout) /
		float64(first.Totals.Killed+first.Totals.Timeout+first.Totals.Lived+first.Totals.NoCoverage); first.Totals.Score != want {
		t.Errorf("score = %v, want %v (RUN_ERROR excluded)", first.Totals.Score, want)
	}

	second, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: runerrorDir, Timeout: time.Second,
		Previous: first, // the RUN_ERROR must not be copied forward
	})
	if err != nil {
		t.Fatal(err)
	}
	if res = result(t, second, target.ID); res.Status != runner.Killed || !slices.Equal(res.KilledBy, []string{"TestDie"}) {
		t.Errorf("the branch mutant = %+v, want KILLED by TestDie (the previous RUN_ERROR executed again)", res)
	}
	if second.Totals.RunError != 0 {
		t.Errorf("totals.run_error = %d, want 0 (the re-executed mutants no longer die)", second.Totals.RunError)
	}
}

// A RUN_ERROR is not a survivor — the run died from infrastructure, so
// nothing was observed about the mutant — and does not make its function
// a weak spot either.
func TestWeakSpotsIgnoresRunError(t *testing.T) {
	mutant := mutator.Mutant{ID: "1", Pkg: "p", File: "p/a.go", Line: 4, Func: "Die", Operator: "branch"}
	report := &runner.Report{Results: []runner.Result{{MutantID: "1", Status: runner.RunError}}}
	if spots := runner.WeakSpots([]mutator.Mutant{mutant}, report); len(spots) != 0 {
		t.Errorf("weak spots = %+v, want none (a RUN_ERROR is not a survivor)", spots)
	}
}

// result is the report entry of one mutant.
func result(t *testing.T, rep *runner.Report, id string) runner.Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.MutantID == id {
			return r
		}
	}
	t.Fatalf("no result for mutant %s", id)
	return runner.Result{}
}

// The tests of a package importing the mutated one kill the mutants its
// own tests cannot: lib's test checks a value inside the range only, and
// app's tests, built against the same schemata sources, check the bounds.
// Their rows are qualified with app's import path, subtests and parents
// alike.
func TestRunExtraTests(t *testing.T) {
	const (
		libDir = "./testdata/cross/lib"
		appDir = "./testdata/cross/app"
		appPkg = "github.com/illumination-k/mutrim/runner/testdata/cross/app"
	)
	bin, mutants, overlay := buildPkgOverlay(t, libDir)
	appBin := filepath.Join(t.TempDir(), "app.test")
	compileTest(t, overlay, appBin, appDir)

	alone, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: libDir, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// lib's test takes the same path through a boundary mutant, so its
	// survivors are suspects as much as plain LIVED.
	survivors := func(t runner.Totals) int { return t.Lived + t.SuspectEquivalent }
	if survivors(alone.Totals) == 0 {
		t.Fatalf("lib's own test must leave mutants alive: %+v", alone.Totals)
	}

	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: libDir, Timeout: time.Second, Subtests: true,
		ExtraTests: []runner.Binary{{Path: appBin, Pkg: appPkg, Dir: appDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Pkg != "github.com/illumination-k/mutrim/runner/testdata/cross/lib" {
		t.Errorf("pkg = %q, want the mutated package", report.Pkg)
	}
	names := make([]string, 0, len(report.Tests))
	for _, tt := range report.Tests {
		names = append(names, tt.Name+"|"+tt.Pkg+"|"+tt.Parent)
	}
	want := []string{
		"TestClampInside||",
		appPkg + ".TestPercent/below|" + appPkg + "|" + appPkg + ".TestPercent",
		appPkg + ".TestPercent/above|" + appPkg + "|" + appPkg + ".TestPercent",
	}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("tests = %v, want %v", names, want)
	}
	if report.Totals.Killed <= alone.Totals.Killed || survivors(report.Totals) >= survivors(alone.Totals) {
		t.Errorf("app's tests must kill more: alone %+v, with app %+v", alone.Totals, report.Totals)
	}
	var byApp int
	for _, r := range report.Results {
		for _, k := range r.KilledBy {
			if strings.HasPrefix(k, appPkg+".TestPercent/") {
				byApp++
			} else if k != "TestClampInside" {
				t.Errorf("%s killed by unknown row %q", r.MutantID, k)
			}
		}
	}
	if byApp == 0 {
		t.Error("no mutant was killed by app's tests")
	}

	for name, extra := range map[string][]runner.Binary{
		"no pkg":       {{Path: appBin}},
		"no path":      {{Pkg: appPkg}},
		"same package": {{Path: appBin, Pkg: appPkg}, {Path: appBin, Pkg: appPkg}},
	} {
		if _, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: libDir, ExtraTests: extra}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// A mutant a static rule proves equivalent is never executed and counts
// towards no score. A survivor whose tests reached exactly the sites
// they reach without it is SUSPECT_EQUIVALENT, left out of the score
// unless CountSuspect; one that changed the path its tests took stays
// LIVED.
func TestRunEquivalent(t *testing.T) {
	bin, mutants := buildPkg(t, equivalentDir)
	find := func(fn, op, desc string) mutator.Mutant {
		t.Helper()
		for _, m := range mutants {
			if m.Func == fn && m.Operator == op && m.Description == desc {
				return m
			}
		}
		t.Fatalf("fixture has no %s %s %q mutant", fn, op, desc)
		return mutator.Mutant{}
	}
	scale := find("Scale", "arithmetic", "* -> /")
	maxBoundary := find("Max", "relational", "> -> >=")
	record := find("Record", "relational", "> -> >=")
	if scale.Equivalent == "" {
		t.Fatalf("Scale's %q mutant is not marked equivalent: %+v", scale.Description, scale)
	}

	rep, err := runner.Run(t.Context(), runner.Options{TestBin: bin, Mutants: mutants, Dir: equivalentDir, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res := result(t, rep, scale.ID); res.Status != runner.Equivalent || res.TestsRun != 0 {
		t.Errorf("Scale's mutant = %+v, want EQUIVALENT and not executed", res)
	}
	if res := result(t, rep, maxBoundary.ID); res.Status != runner.SuspectEquivalent || res.TestsRun != 1 {
		t.Errorf("Max's boundary mutant = %+v, want SUSPECT_EQUIVALENT after running TestMax", res)
	}
	if res := result(t, rep, record.ID); res.Status != runner.Lived {
		t.Errorf("Record's boundary mutant = %+v, want LIVED (its test reached note)", res)
	}
	tot := rep.Totals
	if tot.Equivalent == 0 || tot.SuspectEquivalent == 0 {
		t.Fatalf("totals = %+v, want equivalent and suspect_equivalent counted", tot)
	}
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.Lived+tot.NoCoverage); tot.Score != want {
		t.Errorf("score = %v, want %v (suspects excluded)", tot.Score, want)
	}
	if rep.Survived(runner.SuspectEquivalent) {
		t.Error("a suspect counts as a survivor without CountSuspect")
	}

	counted, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin, Mutants: mutants, Dir: equivalentDir, Timeout: 5 * time.Second,
		CountSuspect: true, Previous: rep,
	})
	if err != nil {
		t.Fatal(err)
	}
	tot = counted.Totals
	if want := float64(tot.Killed+tot.Timeout) / float64(tot.Killed+tot.Timeout+tot.Lived+tot.NoCoverage+tot.SuspectEquivalent); tot.Score != want || tot.Score >= rep.Totals.Score {
		t.Errorf("score with CountSuspect = %v, want %v, below %v", tot.Score, want, rep.Totals.Score)
	}
	spots := runner.WeakSpots(mutants, counted)
	if !slices.ContainsFunc(spots, func(s runner.Spot) bool { return s.Func == "Max" && s.Lived > 0 }) {
		t.Errorf("weak spots = %+v, want Max counted with CountSuspect", spots)
	}
}
