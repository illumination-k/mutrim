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

// Options configures Run.
type Options struct {
	// TestBin is a test binary built from schemata sources (`go test -c`).
	TestBin string
	// Mutants is the content of mutants.json for that package.
	Mutants []mutator.Mutant
	// Dir is the working directory of the test binary; empty means the
	// current directory.
	Dir string
	// Args are passed to the binary after the runner's own test flags.
	Args []string
	// Tests restricts the run to these top-level tests; nil means every
	// test the binary has.
	Tests []string
	// Subtests makes every subtest (`TestX/case`) a row of the report: it is
	// traced on its own and named in killed_by, so the minimizer can call a
	// table row redundant. A test without subtests stays a row of its own.
	// Subtest names must be stable across runs, so a name generated from
	// random data is not supported.
	Subtests bool
	// Timeout per test process (one per mutant, or with Subtests one per
	// parent of the rows reaching it); zero derives 3× the baseline run, at
	// least MinTimeout.
	Timeout time.Duration
	// Shard selects mutants whose ID % Shards == Shard. Shards <= 1 runs all.
	Shard, Shards int
	// Previous, if set, supplies results copied forward for mutants whose
	// ID it already contains; only new IDs are executed.
	Previous *Report
	// Log receives one line per mutant; nil discards them.
	Log io.Writer
}

// Run executes every viable mutant of the shard against the tests that
// reach it and returns the report. The baseline (no mutant) must pass
// first, or Run fails; its output names the tests, which are the rows of
// the report (the subtests too with o.Subtests). Then every row runs once
// on its own with GOMUTANT_TRACE set to learn which sites it reaches, so a
// mutant only runs the tests that can kill it and the report holds a
// per-test kill matrix.
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

	base, err := o.exec(ctx, "", "", testPattern(o.Tests), 0)
	if err != nil {
		return nil, err
	}
	if base.Status != Lived {
		return nil, fmt.Errorf("runner: baseline run failed (%s); the tests must pass without a mutant\n%s", base.Status, base.output)
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = max(3*time.Duration(base.DurationMS)*time.Millisecond, MinTimeout)
	}
	names := rows(started(base.output), o.Subtests)
	logger.Printf("baseline %dms, timeout %s, %d tests", base.DurationMS, timeout, len(names))

	tests, err := o.traceTests(ctx, names)
	if err != nil {
		return nil, err
	}
	reachers := map[string][]string{}
	for _, t := range tests {
		for _, id := range t.Sites {
			reachers[id] = append(reachers[id], t.Name)
		}
	}

	previous := map[string]Result{}
	if o.Previous != nil {
		for _, r := range o.Previous.Results {
			previous[r.MutantID] = r
		}
	}

	report := &Report{BaselineMS: base.DurationMS, TimeoutMS: timeout.Milliseconds(), Tests: tests, Results: []Result{}}
	if len(o.Mutants) > 0 {
		report.Pkg = o.Mutants[0].Pkg
	}
	for _, m := range o.Mutants {
		if !inShard(m.ID, o.Shard, o.Shards) {
			continue
		}
		var r Result
		switch prev, cached := previous[m.ID]; {
		case m.Ignored != "":
			r = Result{MutantID: m.ID, Status: Ignored}
		case !m.Viable:
			r = Result{MutantID: m.ID, Status: NotViable}
		case len(reachers[m.ID]) == 0:
			r = Result{MutantID: m.ID, Status: NoCoverage}
			logger.Printf("%s %s %s:%d %s %q", m.ID, r.Status, m.File, m.Line, m.Func, m.Description)
		case cached && prev.Status.Executed():
			r = prev
			logger.Printf("%s %s (previous)", m.ID, r.Status)
		default:
			if r, err = o.runMutant(ctx, m.ID, reachers[m.ID], timeout); err != nil {
				return nil, err
			}
			logger.Printf("%s %s %s:%d %s %q (%dms)", m.ID, r.Status, m.File, m.Line, m.Func, m.Description, r.DurationMS)
		}
		report.Results = append(report.Results, r)
	}
	report.total()
	return report, nil
}

// runMutant runs the rows reaching mutant id against it and merges the
// outcomes. A -test.run pattern selects the subtests of one parent, so the
// rows run in one process per parent; KILLED wins over TIMEOUT, which wins
// over LIVED, and every process's killers are recorded, so killed_by is
// the complete kill matrix.
func (o Options) runMutant(ctx context.Context, id string, rows []string, timeout time.Duration) (Result, error) {
	r := Result{MutantID: id, Status: Lived}
	for _, group := range groupByParent(rows) {
		res, err := o.exec(ctx, id, "", testPattern(group), timeout)
		if err != nil {
			return Result{}, err
		}
		ran, failed := parseOutput(res.output, res.Status == Timeout, group)
		r.TestsRun += ran
		r.KilledBy = append(r.KilledBy, failed...)
		r.DurationMS += res.DurationMS
		if res.Status == Killed || r.Status == Lived {
			r.Status = res.Status
		}
	}
	return r, nil
}

// traceTests runs every row on its own with GOMUTANT_TRACE set and
// returns, per row, its duration and the sites it reached. A test that
// fails on its own is an error, like a failing baseline.
func (o Options) traceTests(ctx context.Context, names []string) ([]Test, error) {
	dir, err := os.MkdirTemp("", "mutrim-trace-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temp dir is harmless

	tests := make([]Test, 0, len(names))
	for i, name := range names {
		trace := filepath.Join(dir, strconv.Itoa(i)+".trace")
		res, err := o.exec(ctx, "", trace, testPattern([]string{name}), 0)
		if err != nil {
			return nil, err
		}
		if res.Status != Lived {
			return nil, fmt.Errorf("runner: %s fails when run on its own (%s); the tests must pass without a mutant\n%s", name, res.Status, res.output)
		}
		data, err := os.ReadFile(trace) //nolint:gosec // our own temp file
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		sites := strings.Fields(string(data))
		slices.Sort(sites)
		tests = append(tests, Test{Name: name, Parent: parent(name), DurationMS: res.DurationMS, Sites: sites})
	}
	return tests, nil
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
)

// exec runs the tests the -test.run pattern selects (all when empty) with
// mutant id active (none when empty), tracing to the trace file when
// given, and classifies the exit. A zero timeout means none.
func (o Options) exec(ctx context.Context, id, trace, pattern string, timeout time.Duration) (*execResult, error) {
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

	cmd := exec.CommandContext(ctx, o.TestBin, args...) //nolint:gosec // running the user's test binary is the point
	cmd.Dir = o.Dir
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
		res.Status = Killed
	default:
		return nil, fmt.Errorf("runner: exec %s: %w", o.TestBin, err)
	}
	return res, nil
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
