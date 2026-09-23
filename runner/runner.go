// Package runner executes a compiled test binary once per mutant and
// classifies the outcome. It is what a Bazel mutation_test target runs; it
// only needs the binary, mutants.json and the GOMUTANT_ID / GOMUTANT_TRACE
// conventions of the mut runtime.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/illumination-k/mutrim/mutator"
)

// MinTimeout is the floor of the derived per-mutant timeout. A short
// baseline says little about the worst case: process startup jitter, and
// tests that spawn the Go toolchain and miss the build cache under a
// mutant, can exceed 3× a sub-second baseline. Mutating mutrim itself
// produced false TIMEOUTs with a 2s floor.
const MinTimeout = 10 * time.Second

// DefaultTimeoutFactor multiplies the reaching tests' durations, and the
// baseline for the cap, when Options.TimeoutFactor is zero.
const DefaultTimeoutFactor = 3

// Binary is the test binary of a package other than the mutated one,
// linked against the mutated package's schemata sources, so its tests can
// kill the mutants too (Options.ExtraTests).
type Binary struct {
	// Path of the test binary.
	Path string
	// Pkg is the import path of the package the binary tests. Its rows are
	// named Pkg.TestX in the report, so they never collide with the
	// mutated package's own tests or another package's.
	Pkg string
	// Dir is the working directory of the test binary; empty means the
	// current directory.
	Dir string
}

// Options configures Run.
type Options struct {
	// TestBin is a test binary built from schemata sources (`go test -c`).
	TestBin string
	// ExtraTests are the test binaries of other packages that import the
	// mutated one, built against the same schemata sources. Their tests are
	// traced and run against the mutants like TestBin's, so a mutant only a
	// downstream package's tests catch is KILLED rather than LIVED or
	// NO_COVERAGE. Their rows are qualified with Binary.Pkg.
	ExtraTests []Binary
	// Mutants is the content of mutants.json for that package.
	Mutants []mutator.Mutant
	// Dir is the working directory of the test binary; empty means the
	// current directory.
	Dir string
	// Args are passed to the binary after the runner's own test flags.
	Args []string
	// Tests restricts the run to these top-level tests of TestBin; nil
	// means every test the binary has. ExtraTests always run all theirs.
	Tests []string
	// Subtests makes every subtest (`TestX/case`) a row of the report: it is
	// traced on its own and named in killed_by, so the minimizer can call a
	// table row redundant. A test without subtests stays a row of its own.
	// Subtest names must be stable across runs, so a name generated from
	// random data is not supported.
	Subtests bool
	// ConfirmKills reruns the rows that killed a mutant, up to this many
	// runs in total, and keeps only the failures every rerun reproduced;
	// the rest are recorded in Result.SuspiciousBy. Zero or one records
	// the first run's kills as they are.
	ConfirmKills int
	// ConfirmBaseline runs each row this many times during tracing
	// instead of once. A row that fails in some runs and passes in others
	// is marked Test.Flaky rather than failing the run; one that fails
	// every time is an error, as a failing baseline always is.
	ConfirmBaseline int
	// Timeout per test process (one per mutant, or with Subtests one per
	// parent of the rows reaching it) overrides the derived one. Zero
	// derives it per mutant from the traced durations of the rows reaching
	// it: TimeoutFactor × their sum + TimeoutConst, at least MinTimeout and
	// at most TimeoutFactor × the baseline run.
	Timeout time.Duration
	// TimeoutFactor scales the derived timeout; zero means
	// DefaultTimeoutFactor.
	TimeoutFactor float64
	// TimeoutConst is added to the derived timeout for process startup.
	TimeoutConst time.Duration
	// CountSuspect counts SUSPECT_EQUIVALENT mutants as survivors, in the
	// score and wherever the report is read (Report.CountSuspect); by
	// default they count towards no score.
	CountSuspect bool
	// Shard selects mutants whose ID % Shards == Shard. Shards <= 1 runs all.
	Shard, Shards int
	// InDiff, if set, scopes the run to the lines it adds: a mutant
	// outside it is reported Skipped without being executed.
	InDiff *Diff
	// Previous, if set, supplies results copied forward for mutants whose
	// ID it already contains; only new IDs are executed.
	Previous *Report
	// Log receives one line per mutant; nil discards them.
	Log io.Writer

	// bins is TestBin (with Dir) followed by ExtraTests, set by Run.
	bins []Binary
}

// row is a row of the report: a test of one of Options.bins, by its name
// in that binary.
type row struct {
	bin  int
	name string
}

// qualify is the row's name in the report: bare for the mutated package's
// own tests, Pkg.TestX for another package's.
func (o Options) qualify(r row) string {
	if r.bin == 0 {
		return r.name
	}
	return o.bins[r.bin].Pkg + "." + r.name
}

// qualifyAll is the rows' names in the report.
func (o Options) qualifyAll(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = o.qualify(r)
	}
	return out
}

// Run executes every viable mutant of the shard against the tests that
// reach it and returns the report. The baseline (no mutant) must pass
// first, or Run fails; its output names the tests, which are the rows of
// the report (the subtests too with o.Subtests). Then every row runs once
// on its own with GOMUTANT_TRACE set to learn which sites it reaches, so a
// mutant only runs the tests that can kill it and the report holds a
// per-test kill matrix. Options.InDiff scopes the run to the lines of a
// diff.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.TestBin == "" {
		return nil, errors.New("runner: test binary is required")
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	logger := log.New(o.Log, "", 0)
	for _, m := range o.Mutants {
		if _, err := strconv.ParseUint(m.ID, 16, 64); err != nil {
			return nil, fmt.Errorf("runner: mutant ID %q is not a hex hash", m.ID)
		}
	}
	for _, t := range o.Tests {
		if strings.Contains(t, "/") {
			return nil, fmt.Errorf("runner: %q is a subtest; Tests names top-level tests", t)
		}
	}
	o.bins = []Binary{{Path: o.TestBin, Dir: o.Dir}}
	pkgs := map[string]bool{}
	for _, b := range o.ExtraTests {
		if b.Path == "" || b.Pkg == "" {
			return nil, fmt.Errorf("runner: extra test binary %q of package %q: both are required", b.Path, b.Pkg)
		}
		if pkgs[b.Pkg] {
			return nil, fmt.Errorf("runner: two extra test binaries of package %q", b.Pkg)
		}
		pkgs[b.Pkg] = true
		o.bins = append(o.bins, b)
	}

	// The baseline runs every binary; its output names the rows.
	var baseMS int64
	var names []row
	for i, b := range o.bins {
		pattern := ""
		if i == 0 {
			pattern = testPattern(o.Tests)
		}
		base, err := o.exec(ctx, b, "", "", pattern, 0)
		if err != nil {
			return nil, err
		}
		if base.Status != Lived {
			return nil, fmt.Errorf("runner: baseline run of %s failed (%s); the tests must pass without a mutant\n%s", b.Path, base.Status, base.output)
		}
		baseMS += base.DurationMS
		for _, name := range rows(started(base.output), o.Subtests) {
			names = append(names, row{bin: i, name: name})
		}
	}
	if o.TimeoutFactor == 0 {
		o.TimeoutFactor = DefaultTimeoutFactor
	}
	maxTimeout := o.Timeout
	if maxTimeout == 0 {
		maxTimeout = max(o.scale(baseMS), MinTimeout)
	}
	logger.Printf("baseline %dms, timeout at most %s, %d tests", baseMS, maxTimeout, len(names))

	tests, err := o.traceTests(ctx, names)
	if err != nil {
		return nil, err
	}
	reachers := map[string][]row{}
	durations := map[string]int64{}
	ref := baseline{flaky: map[string]bool{}, sites: map[string][]string{}}
	for i, t := range tests {
		durations[t.Name] = t.DurationMS
		ref.sites[t.Name] = t.Sites
		for _, id := range t.Sites {
			reachers[id] = append(reachers[id], names[i])
		}
		if t.Flaky {
			ref.flaky[t.Name] = true
			logger.Printf("%s is flaky: it failed some of the %d baseline runs; its failures are no kills", t.Name, o.ConfirmBaseline)
		}
	}

	if ref.traceDir, err = os.MkdirTemp("", "mutrim-mutant-trace-"); err != nil {
		return nil, err
	}
	defer os.RemoveAll(ref.traceDir) //nolint:errcheck // a leftover temp dir is harmless

	previous := map[string]Result{}
	if o.Previous != nil {
		for _, r := range o.Previous.Results {
			previous[r.MutantID] = r
		}
	}

	report := &Report{BaselineMS: baseMS, TimeoutMS: maxTimeout.Milliseconds(), CountSuspect: o.CountSuspect, Tests: tests, Results: []Result{}}
	if len(o.Mutants) > 0 {
		report.Pkg = o.Mutants[0].Pkg
	}
	for _, m := range o.Mutants {
		if !inShard(m.ID, o.Shard, o.Shards) {
			continue
		}
		var r Result
		switch prev, cached := previous[m.ID]; {
		case !o.InDiff.Touches(m.File, m.Line, m.EndLine):
			r = Result{MutantID: m.ID, Status: Skipped}
		case m.Ignored != "":
			r = Result{MutantID: m.ID, Status: Ignored}
		case m.Equivalent != "":
			r = Result{MutantID: m.ID, Status: Equivalent}
		case !m.Viable:
			r = Result{MutantID: m.ID, Status: NotViable}
		case len(reachers[m.ID]) == 0:
			r = Result{MutantID: m.ID, Status: NoCoverage}
			logger.Printf("%s %s %s:%d %s %q", m.ID, r.Status, m.File, m.Line, m.Func, m.Description)
		case cached && prev.Status.Executed():
			r = prev
			logger.Printf("%s %s (previous)", m.ID, r.Status)
		default:
			timeout := o.mutantTimeout(o.qualifyAll(reachers[m.ID]), durations, maxTimeout)
			if r, err = o.runMutant(ctx, m.ID, reachers[m.ID], timeout, ref); err != nil {
				return nil, err
			}
			logger.Printf("%s %s %s:%d %s %q (%dms, timeout %s)%s", m.ID, r.Status, m.File, m.Line, m.Func, m.Description, r.DurationMS, timeout, suspicion(r.SuspiciousBy))
		}
		r.Class = m.Class
		report.Results = append(report.Results, r)
	}
	report.total()
	if o.InDiff != nil {
		logger.Printf("%d of %d mutants skipped: not in the diff", report.Totals.Skipped, report.Totals.Mutants)
	}
	for _, class := range slices.Sorted(maps.Keys(report.Totals.Classes)) {
		c := report.Totals.Classes[class]
		logger.Printf("%s: %.0f%% killed (%d of %d scored)", class, 100*c.Score, c.Killed, c.Killed+c.Survived)
	}
	if report.Totals.Suspicious > 0 {
		logger.Printf("%d of %d mutants have an unconfirmed kill; see suspicious_by", report.Totals.Suspicious, report.Totals.Mutants)
	}
	return report, nil
}

// mutantTimeout is the timeout of a mutant reached by rows: Options.Timeout
// when set, else TimeoutFactor × the rows' traced durations + TimeoutConst,
// clamped to [MinTimeout, maxTimeout]. A mutant reached by one quick test
// then times out long before one reached by the whole suite.
func (o Options) mutantTimeout(rows []string, durations map[string]int64, maxTimeout time.Duration) time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	var sum int64
	for _, r := range rows {
		sum += durations[r]
	}
	return min(max(o.scale(sum)+o.TimeoutConst, MinTimeout), maxTimeout)
}

// scale is TimeoutFactor × ms milliseconds.
func (o Options) scale(ms int64) time.Duration {
	return time.Duration(o.TimeoutFactor * float64(ms) * float64(time.Millisecond))
}

// runMutant runs the rows reaching mutant id against it and merges the
// outcomes. A -test.run pattern selects the subtests of one parent, so the
// rows run in one process per binary and parent; KILLED wins over TIMEOUT, which wins
// over RUN_ERROR, which wins over LIVED, and every process's killers are
// recorded, so killed_by is the complete kill matrix.
//
// A failure is a kill only when it is trustworthy: a row flaky marks fails
// on its own, so its failure says nothing about the mutant, and with
// Options.ConfirmKills the other failures must reproduce in every rerun.
// The rest are recorded in suspicious_by, and a group whose every killer
// turned suspicious counts as LIVED.
//
// A group that dies from infrastructure is a RUN_ERROR: no test failed, so
// nothing can be said of the mutant. It never overrides a kill of another
// group — that kill is a real observation — but it does override LIVED:
// nothing was observed to pass either.
//
// Every process is traced, and a LIVED mutant no row failed against, not
// even suspiciously, and whose processes reached exactly the sites their
// rows reach without it is SUSPECT_EQUIVALENT: it changed neither an
// outcome nor the path taken. The trace is the union over a process's
// rows, compared with the union of their own.
func (o Options) runMutant(ctx context.Context, id string, rows []row, timeout time.Duration, ref baseline) (Result, error) {
	r := Result{MutantID: id, Status: Lived, TimeoutMS: timeout.Milliseconds()}
	differed := false
	byBin := make([][]string, len(o.bins))
	for _, rw := range rows {
		byBin[rw.bin] = append(byBin[rw.bin], rw.name)
	}
	for bin, names := range byBin {
		for _, group := range groupByParent(names) {
			status, changed, err := o.runGroup(ctx, &r, bin, group, timeout, ref)
			if err != nil {
				return Result{}, err
			}
			differed = differed || changed
			if rank[status] > rank[r.Status] {
				r.Status = status
			}
		}
	}
	if r.Status == Lived && !differed {
		r.Status = SuspectEquivalent
	}
	return r, nil
}

// baseline is what the trace runs observed without a mutant, which a
// mutant's runs are judged against.
type baseline struct {
	// flaky holds the rows Test.Flaky marks, by their name in the report.
	flaky map[string]bool
	// sites holds the sites each row reaches (Test.Sites), by its name in
	// the report.
	sites map[string][]string
	// traceDir receives the traces of the mutants' runs.
	traceDir string
}

// reached is the union of the sites the rows reach.
func (b baseline) reached(rows []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		for _, id := range b.sites[r] {
			out[id] = true
		}
	}
	return out
}

// runGroup runs the rows of one binary and parent against mutant r,
// records its kills in r and returns the group's status, and whether the
// mutant made a row fail or changed the sites the rows reach.
func (o Options) runGroup(ctx context.Context, r *Result, bin int, group []string, timeout time.Duration, ref baseline) (status Status, changed bool, err error) {
	b := o.bins[bin]
	trace := filepath.Join(ref.traceDir, r.MutantID+".trace")
	res, err := o.exec(ctx, b, r.MutantID, trace, testPattern(group), timeout)
	if err != nil {
		return "", false, err
	}
	reached, err := readTrace(trace)
	if err != nil {
		return "", false, err
	}
	qualified := make([]string, len(group))
	for i, name := range group {
		qualified[i] = o.qualify(row{bin, name})
	}
	ran, failed := parseOutput(res.output, res.Status == Timeout, group)
	changed = len(failed) > 0 || !maps.Equal(reached, ref.reached(qualified))
	r.TestsRun += ran
	r.DurationMS += res.DurationMS

	candidates := []string{}
	for _, name := range failed {
		if q := o.qualify(row{bin, name}); ref.flaky[q] {
			r.SuspiciousBy = append(r.SuspiciousBy, q)
		} else {
			candidates = append(candidates, name)
		}
	}
	killed, suspicious, ms, err := o.confirmKills(ctx, b, r.MutantID, candidates, timeout)
	if err != nil {
		return "", false, err
	}
	for _, name := range killed {
		r.KilledBy = append(r.KilledBy, o.qualify(row{bin, name}))
	}
	for _, name := range suspicious {
		r.SuspiciousBy = append(r.SuspiciousBy, o.qualify(row{bin, name}))
	}
	r.DurationMS += ms

	if len(failed) > 0 && len(killed) == 0 {
		// Every failure of this group is suspicious, so nothing here
		// tells the mutant from the original.
		return Lived, changed, nil
	}
	return res.Status, changed, nil
}

// rank orders the statuses of a group's run, weakest first: the run's own
// verdicts count more than what it could not observe.
var rank = map[Status]int{Lived: 0, RunError: 1, Timeout: 2, Killed: 3}

// confirmKills reruns the rows of binary b that failed against mutant id,
// which share a parent, until each of them has failed Options.ConfirmKills runs in
// total or has passed once. It returns the rows whose failure reproduced
// every time, in the order given, the rest as suspicious, and the time the
// reruns took. Fewer than two runs confirms nothing and reruns nothing.
func (o Options) confirmKills(ctx context.Context, b Binary, id string, killers []string, timeout time.Duration) (killed, suspicious []string, ms int64, err error) {
	if o.ConfirmKills < 2 || len(killers) == 0 {
		return killers, nil, 0, nil
	}
	still := killers
	for range o.ConfirmKills - 1 {
		if len(still) == 0 {
			break
		}
		res, err := o.exec(ctx, b, id, "", testPattern(still), timeout)
		if err != nil {
			return nil, nil, 0, err
		}
		ms += res.DurationMS
		_, still = parseOutput(res.output, res.Status == Timeout, still)
	}
	for _, name := range killers {
		if slices.Contains(still, name) {
			killed = append(killed, name)
		} else {
			suspicious = append(suspicious, name)
		}
	}
	return killed, suspicious, ms, nil
}

// suspicion is the log suffix naming a result's unconfirmed kills.
func suspicion(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " suspicious: " + strings.Join(names, ",")
}

// traceTests runs every row on its own with GOMUTANT_TRACE set and
// returns, per row, its duration and the sites it reached. Options.
// ConfirmBaseline repeats each row, and the results are merged: the sites
// of every run, the longest duration, and Flaky when the row failed in
// some runs but not all. A row that fails every run is an error, like a
// failing baseline. A row whose run dies from infrastructure is an
// error too: nothing was observed, so flakiness cannot be told from a
// broken trace and the run stops.
func (o Options) traceTests(ctx context.Context, rows []row) ([]Test, error) {
	dir, err := os.MkdirTemp("", "mutrim-trace-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temp dir is harmless

	runs := max(o.ConfirmBaseline, 1)
	tests := make([]Test, 0, len(rows))
	for i, rw := range rows {
		name := o.qualify(rw)
		t := Test{Name: name, Pkg: o.bins[rw.bin].Pkg}
		if p := parent(rw.name); p != "" {
			t.Parent = o.qualify(row{rw.bin, p})
		}
		sites := map[string]bool{}
		var failures int
		var failed *execResult
		for run := range runs {
			trace := filepath.Join(dir, fmt.Sprintf("%d-%d.trace", i, run))
			res, err := o.exec(ctx, o.bins[rw.bin], "", trace, testPattern([]string{rw.name}), 0)
			if err != nil {
				return nil, err
			}
			if res.Status == RunError {
				return nil, fmt.Errorf("runner: %s: infrastructure failure while run on its own; the tests must pass without a mutant\n%s", name, res.output)
			}
			if res.Status != Lived {
				failures++
				failed = res
			}
			reached, err := readTrace(trace)
			if err != nil {
				return nil, err
			}
			maps.Copy(sites, reached)
			t.DurationMS = max(t.DurationMS, res.DurationMS)
		}
		if failures == runs {
			return nil, fmt.Errorf("runner: %s fails when run on its own (%s); the tests must pass without a mutant\n%s", name, failed.Status, failed.output)
		}
		t.Flaky = failures > 0
		t.Sites = slices.Sorted(maps.Keys(sites))
		tests = append(tests, t)
	}
	return tests, nil
}

// readTrace reads the sites a GOMUTANT_TRACE file lists and removes the
// file; a missing file means no site was reached.
func readTrace(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // our own temp file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sites := map[string]bool{}
	for _, id := range strings.Fields(string(data)) {
		sites[id] = true
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return sites, nil
}

// parent is the test that runs name as a subtest; empty for a top-level
// test.
func parent(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return ""
	}
	return name[:i]
}

// rows picks the rows of the report from the tests a run started: the
// top-level tests, or with subtests every test that has no subtest of its
// own, so a top-level test without subtests stays a row. Examples and fuzz
// targets are no rows.
func rows(names []string, subtests bool) []string {
	parents := map[string]bool{}
	for _, n := range names {
		parents[parent(n)] = true
	}
	out := []string{}
	for _, n := range names {
		if !strings.HasPrefix(n, "Test") {
			continue
		}
		if (subtests && !parents[n]) || (!subtests && parent(n) == "") {
			out = append(out, n)
		}
	}
	return out
}

// groupByParent splits rows by parent, in parent order, keeping the rows'
// order within a group.
func groupByParent(rows []string) [][]string {
	byParent := map[string][]string{}
	for _, r := range rows {
		byParent[parent(r)] = append(byParent[parent(r)], r)
	}
	groups := make([][]string, 0, len(byParent))
	for _, p := range slices.Sorted(maps.Keys(byParent)) {
		groups = append(groups, byParent[p])
	}
	return groups
}

// testPattern is the -test.run pattern that selects exactly tests, which
// must share a parent: "^TestX$/^(a|b)$" runs the subtests a and b of
// TestX and nothing else, since the testing package matches the pattern
// element by element against the name. Empty selects every test.
func testPattern(tests []string) string {
	if len(tests) == 0 {
		return ""
	}
	var b strings.Builder
	for _, elem := range strings.Split(parent(tests[0]), "/") {
		if elem != "" {
			b.WriteString("^" + regexp.QuoteMeta(elem) + "$/")
		}
	}
	leaves := make([]string, len(tests))
	for i, t := range tests {
		leaves[i] = regexp.QuoteMeta(t[strings.LastIndex(t, "/")+1:])
	}
	b.WriteString("^(" + strings.Join(leaves, "|") + ")$")
	return b.String()
}

// inShard reports whether id belongs to shard index out of total.
func inShard(id string, index, total int) bool {
	if total <= 1 {
		return true
	}
	n, _ := strconv.ParseUint(id, 16, 64) // validated by Run
	return int(n%uint64(total)) == index  //nolint:gosec // total is a small positive count
}

type execResult struct {
	Status     Status
	DurationMS int64
	output     []byte
}

var (
	runLine  = regexp.MustCompile(`(?m)^=== RUN\s+(\S+)$`)
	doneLine = regexp.MustCompile(`(?m)^\s*--- (?:PASS|FAIL|SKIP): (\S+)`)
	failLine = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	// panicLine matches the runtime's panic header, which a test's
	// failure prints before any --- FAIL: line the run would show.
	panicLine = regexp.MustCompile(`(?m)^panic:`)
	// runtimeStack matches a goroutine dump, which the runtime prints
	// around a fatal error or a panic it could not recover.
	runtimeStack = regexp.MustCompile(`(?m)^goroutine \d+ \[`)
	// fatalErrors are the runtime's fatal errors that never come from a
	// test: out of memory, deadlocks, concurrent map misuse. The runtime
	// prints them with a stack and exits 2.
	fatalError = []byte("fatal error:")
)

// exec runs the tests of binary b the -test.run pattern selects (all when
// empty) with mutant id active (none when empty), tracing to the trace
// file when given, and classifies the exit. A zero timeout means none.
func (o Options) exec(ctx context.Context, b Binary, id, trace, pattern string, timeout time.Duration) (*execResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	args := []string{"-test.v"}
	if pattern != "" {
		args = append(args, "-test.run", pattern)
	}
	args = append(args, o.Args...)

	cmd := exec.CommandContext(ctx, b.Path, args...) //nolint:gosec // running the user's test binary is the point
	cmd.Dir = b.Dir
	cmd.Env = childEnv(os.Environ(), id, trace)
	cmd.WaitDelay = time.Second

	start := time.Now()
	out, err := cmd.CombinedOutput()
	res := &execResult{output: out, DurationMS: time.Since(start).Milliseconds()}

	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = Timeout
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err == nil:
		res.Status = Lived
	case errors.As(err, &exitErr):
		res.Status = classifyExit(res.output, exitErr.ExitCode())
	default:
		return nil, fmt.Errorf("runner: exec %s: %w", b.Path, err)
	}
	return res, nil
}

// classifyExit reclassifies the non-zero exit of the test binary. A ---
// FAIL: or panic: line in the output means the failure came from inside
// a test, so the tests' verdict stands: a kill, whatever else the run
// printed (the gomutants rule). Otherwise anything that says the process
// died from outside the tests is a RUN_ERROR — the infrastructure
// failed, not the tests:
//
//   - the runtime hit a fatal error (out of memory, a deadlock), which
//     looks the same from a mutant that deadlocks,
//   - the process was killed by a signal (a sandbox kill): os.ExitCode
//     reports -1, the parent's "signal: killed",
//   - exit status 2 with a runtime stack (the runtime could not recover
//     a signal, so it dumped the goroutines and exited), or
//   - an exit code the testing package never uses, which only an os.Exit
//     of the tests themselves or a mutant flipping the condition
//     guarding one produces.
//
// Anything else — a bare exit 1 or 2 without a test failing — is a
// kill: the testing package exits 1 when a test fails and 2 is left to
// the binary itself, and no pattern tells those apart.
func classifyExit(out []byte, exitCode int) Status {
	if failLine.Match(out) || panicLine.Match(out) {
		return Killed
	}
	if bytes.Contains(out, fatalError) || exitCode == -1 || (exitCode == 2 && runtimeStack.Match(out)) || exitCode >= 3 {
		return RunError
	}
	return Killed
}

// started lists the tests a -test.v output started, in order, once each.
func started(out []byte) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range runLine.FindAllSubmatch(out, -1) {
		if name := string(m[1]); !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// parseOutput counts the rows that started and lists those that failed.
// On a timeout the rows that started but never finished are the ones that
// hung, so they count as killers too; the testing package prints a
// subtest's result only when its parent finishes, so every subtest of a
// hung parent is counted. A failure above the rows (the parent's body
// failing before or after its subtests) that no row of its own explains
// is attributed to every row under it, since running any one of them
// runs that body.
func parseOutput(out []byte, timedOut bool, rows []string) (ran int, failed []string) {
	isRow := map[string]bool{}
	for _, r := range rows {
		isRow[r] = true
	}
	done := map[string]bool{}
	for _, m := range doneLine.FindAllSubmatch(out, -1) {
		done[string(m[1])] = true
	}
	add := func(name string) {
		if !slices.Contains(failed, name) {
			failed = append(failed, name)
		}
	}
	var above []string
	for _, m := range failLine.FindAllSubmatch(out, -1) {
		if name := string(m[1]); isRow[name] {
			add(name)
		} else {
			above = append(above, name)
		}
	}
	for _, name := range started(out) {
		if !isRow[name] {
			continue
		}
		ran++
		if timedOut && !done[name] {
			add(name)
		}
	}
	for _, a := range above {
		under := func(name string) bool { return strings.HasPrefix(name, a+"/") }
		if slices.ContainsFunc(failed, under) {
			continue
		}
		for _, r := range rows {
			if under(r) {
				add(r)
			}
		}
	}
	return ran, failed
}

// droppedEnv lists the variables the test binary must not inherit: the
// mutant selection and tracing themselves, and the parts of Bazel's test
// protocol that a rules_go test binary acts on. Under a sharded
// mutation_test the binary would otherwise shard its own tests again,
// apply --test_filter over the runner's -test.run, fail fast, time itself
// out, and overwrite the runner's test.xml with the last mutant's outcome.
var droppedEnv = map[string]bool{
	"GOMUTANT_ID":                      true,
	"GOMUTANT_TRACE":                   true,
	"TEST_TOTAL_SHARDS":                true,
	"TEST_SHARD_INDEX":                 true,
	"TEST_SHARD_STATUS_FILE":           true,
	"TESTBRIDGE_TEST_ONLY":             true,
	"TESTBRIDGE_TEST_RUNNER_FAIL_FAST": true,
	"TEST_TIMEOUT":                     true,
	"XML_OUTPUT_FILE":                  true,
}

// childEnv derives the test binary's environment from env, with mutant id
// selected (none when empty) and reached sites traced to the trace file
// (not traced when empty).
func childEnv(env []string, id, trace string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !droppedEnv[key] {
			out = append(out, kv)
		}
	}
	out = append(out, "GOMUTANT_ID="+id)
	if trace != "" {
		out = append(out, "GOMUTANT_TRACE="+trace)
	}
	return out
}
