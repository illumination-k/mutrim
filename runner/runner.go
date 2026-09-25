// Package runner executes a compiled test binary once per mutant and
// classifies the outcome. It is what a Bazel mutation_test target runs; it
// only needs the binary, mutants.json and the GOMUTANT_ID / GOMUTANT_TRACE
// conventions of the mut runtime.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/illumination-k/mutrim/mutator"
)

// DefaultMinTimeout is the floor of the derived per-mutant timeout when
// Options.MinTimeout is zero. A short baseline says little about the worst
// case: process startup jitter, and tests that spawn the Go toolchain and
// miss the build cache under a mutant, can exceed 3× a sub-second
// baseline. Mutating mutrim itself produced false TIMEOUTs with a 2s
// floor. The floor is also what every looping mutant costs, so a package
// of fast, self-contained tests runs much faster with a lower one.
const DefaultMinTimeout = 10 * time.Second

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
	// Srcs are the package's _test.go files, which Test.Hash is read from;
	// none leaves the hashes empty.
	Srcs []string
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
	// TestSrcs are TestBin's _test.go files, which Test.Hash is read from.
	TestSrcs []string
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
	// SkipFailing keeps a failing test from stopping the run: a row that
	// fails without a mutant, in the baseline or when run on its own, is
	// recorded with Test.Status TestFailing and runs against no mutant, so
	// it kills none. The baseline is rerun without the failing top-level
	// tests until it passes, so a panicking test does not hide the tests
	// after it. Run still fails when no row passes. Without it any such
	// failure fails the run.
	SkipFailing bool
	// Timeout per test process (one per mutant, or with Subtests one per
	// parent of the rows reaching it) overrides the derived one. Zero
	// derives it per mutant from the traced durations of the rows reaching
	// it: TimeoutFactor × their sum + TimeoutConst, at least MinTimeout and
	// at most TimeoutFactor × the baseline run.
	Timeout time.Duration
	// MinTimeout is the floor of the derived timeout, and of its cap;
	// zero means DefaultMinTimeout.
	MinTimeout time.Duration
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
	// outside it is reported Skipped without being executed. A mutant on
	// an added line is marked Result.Changed.
	InDiff *Diff
	// DiffExpand widens InDiff from the changed lines to the
	// commit-relevant mutants: every mutant reached by a row that reaches
	// a changed line's site, wherever it lies (Ojdanić et al., TOSEM
	// 2023: most commit-relevant mutants lie outside the changed lines).
	DiffExpand bool
	// Sample is the fraction of the mutants the run keeps, selected by
	// a hash of their ID and Seed below it: the rest are reported Skipped
	// without being executed, so every score is computed over the sample
	// (Totals.Sampled records the fraction kept). Zero or one keeps
	// everything.
	Sample float64
	// Seed is the seed of the Sample selection: the same seed keeps the
	// same mutants in every shard of a run and in every incremental run
	// with the same seed. Zero is a seed like any other.
	Seed int64
	// Previous, if set, supplies results copied forward for mutants whose
	// ID it already contains, as long as the tests they were observed with
	// are unchanged (see Test.Hash); the rest are executed.
	Previous *Report
	// Jobs is the number of test processes run at once, while tracing and
	// while running the mutants; zero means GOMAXPROCS. The derived
	// timeouts come from trace runs made under the same load.
	Jobs int
	// Log receives one line per mutant, in the order they finish; nil
	// discards them.
	Log io.Writer

	// bins is TestBin (with Dir and TestSrcs) followed by ExtraTests, set
	// by Run.
	bins []Binary
	// hashes holds the hashes of each of bins' test functions, by name.
	hashes []map[string]string
	// sites holds the IDs of Mutants, which tell a traced site from a
	// traced block.
	sites map[string]bool
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
// first, or Run fails (see Options.SkipFailing); its output names the tests, which are the rows of
// the report (the subtests too with o.Subtests). Then every row runs once
// on its own with GOMUTANT_TRACE set to learn which sites it reaches, so a
// mutant only runs the tests that can kill it and the report holds a
// per-test kill matrix. Options.InDiff scopes the run to the lines of a
// diff, or with Options.DiffExpand to the mutants the tests of those lines
// reach; Options.Sample keeps a fraction of the mutants, selected by a
// hash of their ID and Seed, the rest reported Skipped.
func Run(ctx context.Context, o Options) (*Report, error) {
	if err := o.setup(); err != nil {
		return nil, err
	}
	logger := log.New(o.Log, "", 0)

	// The baseline runs every binary; its output names the rows.
	names, failing, baseMS, err := o.baselines(ctx)
	if err != nil {
		return nil, err
	}
	maxTimeout := o.Timeout
	if maxTimeout == 0 {
		maxTimeout = max(o.scale(baseMS), o.MinTimeout)
	}
	logger.Printf("baseline %dms, timeout at most %s, %d tests", baseMS, maxTimeout, len(names))

	tests, err := o.traceTests(ctx, names, failing)
	if err != nil {
		return nil, err
	}
	if err = logFailing(logger, tests); err != nil {
		return nil, err
	}
	reachers := map[string][]row{}
	durations := map[string]int64{}
	ref := baseline{flaky: map[string]bool{}, sites: map[string][]string{}}
	for i, t := range tests {
		durations[t.Name] = t.DurationMS
		ref.sites[t.Name] = slices.Concat(t.Sites, t.Blocks)
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

	previous, before, now := map[string]Result{}, map[string]Test{}, map[string]Test{}
	if o.Previous != nil {
		for _, r := range o.Previous.Results {
			previous[r.MutantID] = r
		}
		for _, t := range o.Previous.Tests {
			before[t.Name] = t
		}
	}
	for _, t := range tests {
		now[t.Name] = t
	}

	changed, scope := o.diffScope(tests)
	report := &Report{BaselineMS: baseMS, TimeoutMS: maxTimeout.Milliseconds(), CountSuspect: o.CountSuspect, Tests: tests, Results: []Result{}}
	if len(o.Mutants) > 0 {
		report.Pkg = o.Mutants[0].Pkg
	}
	// Each mutant's result is decided on its own, so they run o.Jobs at a
	// time; results keep the order of o.Mutants.
	results := make([]*Result, len(o.Mutants))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(o.jobs())
	var samplable, kept int // mutants of the shard the sample could select, and the ones it did
	for i, m := range o.Mutants {
		if !inShard(m.ID, o.Shard, o.Shards) {
			continue
		}
		r := &Result{}
		results[i] = r
		// The sample selects among the mutants that pass the exclusions
		// below it in the switch; the ones it drops are Skipped without
		// being executed.
		if selectable(scope, m) {
			samplable++
			if o.keeps(m.ID) {
				kept++
			}
		}
		switch prev, cached := previous[m.ID]; {
		case scope != nil && !scope[m.ID]:
			*r = Result{MutantID: m.ID, Status: Skipped}
		case m.Ignored != "":
			*r = Result{MutantID: m.ID, Status: Ignored}
		case m.Equivalent != "":
			*r = Result{MutantID: m.ID, Status: Equivalent}
		case !m.Viable:
			*r = Result{MutantID: m.ID, Status: NotViable}
		case !o.keeps(m.ID):
			*r = Result{MutantID: m.ID, Status: Skipped}
		case len(reachers[m.ID]) == 0:
			*r = Result{MutantID: m.ID, Status: NoCoverage}
			logger.Printf("%s %s %s:%d %s %q", m.ID, r.Status, m.File, m.Line, m.Func, m.Description)
		case cached && reusable(m.ID, prev, o.qualifyAll(reachers[m.ID]), now, before):
			*r = prev
			logger.Printf("%s %s (previous)", m.ID, r.Status)
		default:
			g.Go(func() error {
				timeout := o.mutantTimeout(o.qualifyAll(reachers[m.ID]), durations, maxTimeout)
				res, err := o.runMutant(gctx, m.ID, reachers[m.ID], timeout, ref)
				if err != nil {
					return err
				}
				*r = res
				logger.Printf("%s %s %s:%d %s %q (%dms, timeout %s)%s", m.ID, r.Status, m.File, m.Line, m.Func, m.Description, r.DurationMS, timeout, suspicion(r.SuspiciousBy))
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for i, r := range results {
		if r != nil {
			r.Class = o.Mutants[i].Class
			r.Changed = changed[r.MutantID]
			report.Results = append(report.Results, *r)
		}
	}
	report.total()
	if samplable > 0 {
		report.Totals.Sampled = float64(kept) / float64(samplable)
	}
	if o.Sample > 0 && o.Sample < 1 {
		logger.Printf("the sample kept %d of %d mutants", kept, samplable)
	}
	logSummary(logger, report)
	return report, nil
}

// setup validates o's inputs and prepares the state Run derives from
// them: the binaries under test (TestBin and ExtraTests) with their test
// hashes, and the defaults of the derived timeout. It fails on an input
// Run cannot run with.
func (o *Options) setup() error {
	if o.TestBin == "" {
		return errors.New("runner: test binary is required")
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Sample < 0 || o.Sample > 1 {
		return fmt.Errorf("runner: -sample %g is out of range 0..1", o.Sample)
	}
	o.sites = map[string]bool{}
	for _, m := range o.Mutants {
		if _, err := strconv.ParseUint(m.ID, 16, 64); err != nil {
			return fmt.Errorf("runner: mutant ID %q is not a hex hash", m.ID)
		}
		o.sites[m.ID] = true
	}
	for _, t := range o.Tests {
		if strings.Contains(t, "/") {
			return fmt.Errorf("runner: %q is a subtest; Tests names top-level tests", t)
		}
	}
	o.bins = []Binary{{Path: o.TestBin, Dir: o.Dir, Srcs: o.TestSrcs}}
	pkgs := map[string]bool{}
	for _, b := range o.ExtraTests {
		if b.Path == "" || b.Pkg == "" {
			return fmt.Errorf("runner: extra test binary %q of package %q: both are required", b.Path, b.Pkg)
		}
		if pkgs[b.Pkg] {
			return fmt.Errorf("runner: two extra test binaries of package %q", b.Pkg)
		}
		pkgs[b.Pkg] = true
		o.bins = append(o.bins, b)
	}
	for _, b := range o.bins {
		h, err := HashTests(b.Srcs)
		if err != nil {
			return err
		}
		o.hashes = append(o.hashes, h)
	}
	if o.TimeoutFactor == 0 {
		o.TimeoutFactor = DefaultTimeoutFactor
	}
	if o.MinTimeout == 0 {
		o.MinTimeout = DefaultMinTimeout
	}
	return nil
}

// baselines runs every binary once without a mutant: the baseline must
// pass, or Run fails with its output. It returns the rows the baselines'
// output names, the rows that failed in it (by their name in the report),
// and how long the runs took. With Options.SkipFailing a failing baseline
// is rerun without the top-level tests of its failing rows, until it
// passes or no failing row is left to skip; the rows are the union of the
// runs', in the order they first started.
func (o Options) baselines(ctx context.Context) (names []row, failing map[string]bool, ms int64, err error) {
	failing = map[string]bool{}
	for i, b := range o.bins {
		pattern := ""
		if i == 0 {
			pattern = testPattern(o.Tests)
		}
		seen := map[string]bool{}
		var skip []string
		for {
			base, err := o.exec(ctx, b, "", "", pattern, testPattern(skip), 0)
			if err != nil {
				return nil, nil, 0, err
			}
			ms += base.DurationMS
			ran := rows(started(base.output), o.Subtests)
			for _, name := range ran {
				if !seen[name] {
					seen[name] = true
					names = append(names, row{bin: i, name: name})
				}
			}
			if base.Status == Lived {
				break
			}
			fail := func() error {
				return fmt.Errorf("runner: baseline run of %s failed (%s); the tests must pass without a mutant\n%s", b.Path, base.Status, base.output)
			}
			if !o.SkipFailing {
				return nil, nil, 0, fail()
			}
			_, failed := parseOutput(base.output, base.Status == Timeout, ran)
			more := false
			for _, name := range failed {
				failing[o.qualify(row{i, name})] = true
				if top, _, _ := strings.Cut(name, "/"); !slices.Contains(skip, top) {
					skip = append(skip, top)
					more = true
				}
			}
			if !more {
				// Nothing the run blames on a test is left to skip.
				return nil, nil, 0, fail()
			}
		}
	}
	return names, failing, ms, nil
}

// logFailing logs the rows Options.SkipFailing kept out of the run, and
// fails when no row is left.
func logFailing(logger *log.Logger, tests []Test) error {
	passing := 0
	for _, t := range tests {
		if t.Status == TestFailing {
			logger.Printf("%s fails without a mutant; skipped", t.Name)
		} else {
			passing++
		}
	}
	if passing == 0 && len(tests) > 0 {
		return errors.New("runner: every test fails without a mutant; nothing is left to run against the mutants")
	}
	return nil
}

// diffScope returns the mutants on the lines Options.InDiff adds, and the
// mutants the run is scoped to: those, and with Options.DiffExpand every
// mutant reached by a row that reaches one of them, since that row
// exercises the change. Both are nil without a diff, which scopes nothing.
func (o Options) diffScope(tests []Test) (changed, scope map[string]bool) {
	if o.InDiff == nil {
		return nil, nil
	}
	changed = map[string]bool{}
	for _, m := range o.Mutants {
		if o.InDiff.Touches(m.File, m.Line, m.EndLine) {
			changed[m.ID] = true
		}
	}
	if !o.DiffExpand {
		return changed, changed
	}
	scope = maps.Clone(changed)
	for _, t := range tests {
		if slices.ContainsFunc(t.Sites, func(id string) bool { return changed[id] }) {
			for _, id := range t.Sites {
				scope[id] = true
			}
		}
	}
	return changed, scope
}

// logSummary logs the report's diff, per-class and suspicious-kill totals,
// after report.total.
func logSummary(logger *log.Logger, report *Report) {
	if d := report.Totals.Diff; d != nil {
		logger.Printf("%d of %d mutants skipped: not in the diff", report.Totals.Skipped, report.Totals.Mutants)
		logger.Printf("changed lines: %.0f%% killed (%d of %d scored)", 100*d.ChangedLines.Score, d.ChangedLines.Killed, d.ChangedLines.Killed+d.ChangedLines.Survived)
		logger.Printf("commit-relevant: %.0f%% killed (%d of %d scored)", 100*d.CommitRelevant.Score, d.CommitRelevant.Killed, d.CommitRelevant.Killed+d.CommitRelevant.Survived)
	}
	for _, class := range slices.Sorted(maps.Keys(report.Totals.Classes)) {
		c := report.Totals.Classes[class]
		logger.Printf("%s: %.0f%% killed (%d of %d scored)", class, 100*c.Score, c.Killed, c.Killed+c.Survived)
	}
	if report.Totals.Suspicious > 0 {
		logger.Printf("%d of %d mutants have an unconfirmed kill; see suspicious_by", report.Totals.Suspicious, report.Totals.Mutants)
	}
}

// jobs is the number of test processes run at once: Options.Jobs, or
// GOMAXPROCS when unset.
func (o Options) jobs() int {
	if o.Jobs > 0 {
		return o.Jobs
	}
	return runtime.GOMAXPROCS(0)
}

// mutantTimeout is the timeout of a mutant reached by rows: Options.Timeout
// when set, else TimeoutFactor × the rows' traced durations + TimeoutConst,
// clamped to [Options.MinTimeout, maxTimeout]. A mutant reached by one quick test
// then times out long before one reached by the whole suite.
func (o Options) mutantTimeout(rows []string, durations map[string]int64, maxTimeout time.Duration) time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	var sum int64
	for _, r := range rows {
		sum += durations[r]
	}
	return min(max(o.scale(sum)+o.TimeoutConst, o.MinTimeout), maxTimeout)
}

// scale is TimeoutFactor × ms milliseconds.
func (o Options) scale(ms int64) time.Duration {
	return time.Duration(o.TimeoutFactor * float64(ms) * float64(time.Millisecond))
}

// suspicion is the log suffix naming a result's unconfirmed kills.
func suspicion(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " suspicious: " + strings.Join(names, ",")
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
	for elem := range strings.SplitSeq(parent(tests[0]), "/") {
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

var (
	runLine  = regexp.MustCompile(`(?m)^=== RUN\s+(\S+)$`)
	doneLine = regexp.MustCompile(`(?m)^\s*--- (?:PASS|FAIL|SKIP): (\S+)`)
	failLine = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
)

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
