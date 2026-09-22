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
	// test the binary lists.
	Tests []string
	// Timeout per mutant; zero derives 3× the baseline run, at least MinTimeout.
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
// first, or Run fails; then every test runs once on its own with
// GOMUTANT_TRACE set to learn which sites it reaches, so a mutant only
// runs the tests that can kill it and the report holds a per-test kill
// matrix.
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

	base, err := o.exec(ctx, "", "", o.Tests, 0)
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
	logger.Printf("baseline %dms, timeout %s, %d tests", base.DurationMS, timeout, base.TestsRun)

	tests, err := o.traceTests(ctx)
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
		case !m.Viable:
			r = Result{MutantID: m.ID, Status: NotViable}
		case len(reachers[m.ID]) == 0:
			r = Result{MutantID: m.ID, Status: NoCoverage}
			logger.Printf("%s %s %s:%d %s %q", m.ID, r.Status, m.File, m.Line, m.Func, m.Description)
		case cached && prev.Status.executed():
			r = prev
			logger.Printf("%s %s (previous)", m.ID, r.Status)
		default:
			res, err := o.exec(ctx, m.ID, "", reachers[m.ID], timeout)
			if err != nil {
				return nil, err
			}
			r = res.Result
			logger.Printf("%s %s %s:%d %s %q (%dms)", m.ID, r.Status, m.File, m.Line, m.Func, m.Description, r.DurationMS)
		}
		report.Results = append(report.Results, r)
	}
	report.total()
	return report, nil
}

// traceTests runs every top-level test on its own with GOMUTANT_TRACE set
// and returns, per test, its duration and the sites it reached. A test
// that fails on its own is an error, like a failing baseline.
func (o Options) traceTests(ctx context.Context) ([]Test, error) {
	names := o.Tests
	if names == nil {
		var err error
		if names, err = o.list(ctx); err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp("", "mutrim-trace-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temp dir is harmless

	tests := make([]Test, 0, len(names))
	for _, name := range names {
		trace := filepath.Join(dir, name+".trace")
		res, err := o.exec(ctx, "", trace, []string{name}, 0)
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
		tests = append(tests, Test{Name: name, DurationMS: res.DurationMS, Sites: sites})
	}
	return tests, nil
}

// list asks the binary for its top-level tests.
func (o Options) list(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, o.TestBin, append([]string{"-test.list", "^Test"}, o.Args...)...) //nolint:gosec // running the user's test binary is the point
	cmd.Dir = o.Dir
	cmd.Env = childEnv(os.Environ(), "", "")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("runner: list tests of %s: %w", o.TestBin, err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); strings.HasPrefix(name, "Test") {
			names = append(names, name)
		}
	}
	return names, nil
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
	Result
	output []byte
}

var (
	runLine  = regexp.MustCompile(`(?m)^=== RUN\s+(\S+)$`)
	doneLine = regexp.MustCompile(`(?m)^--- (?:PASS|FAIL|SKIP): (\S+)`)
	failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
)

// exec runs the tests (all when nil) with mutant id active (none when
// empty), tracing to the trace file when given, and classifies the exit.
// A zero timeout means none.
func (o Options) exec(ctx context.Context, id, trace string, tests []string, timeout time.Duration) (*execResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	args := []string{"-test.v"}
	if len(tests) > 0 {
		quoted := make([]string, len(tests))
		for i, t := range tests {
			quoted[i] = regexp.QuoteMeta(t)
		}
		args = append(args, "-test.run", "^("+strings.Join(quoted, "|")+")$")
	}
	args = append(args, o.Args...)

	cmd := exec.CommandContext(ctx, o.TestBin, args...) //nolint:gosec // running the user's test binary is the point
	cmd.Dir = o.Dir
	cmd.Env = childEnv(os.Environ(), id, trace)
	cmd.WaitDelay = time.Second

	start := time.Now()
	out, err := cmd.CombinedOutput()
	res := &execResult{output: out}
	res.MutantID = id
	res.DurationMS = time.Since(start).Milliseconds()

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
	res.TestsRun, res.KilledBy = parseOutput(out, res.Status == Timeout)
	return res, nil
}

// parseOutput counts the top-level tests that started and lists those
// that failed. On a timeout the tests that started but never finished are
// the ones that hung, so they count as killers too.
func parseOutput(out []byte, timedOut bool) (started int, failed []string) {
	done := map[string]bool{}
	for _, m := range doneLine.FindAllSubmatch(out, -1) {
		done[string(m[1])] = true
	}
	for _, m := range failLine.FindAllSubmatch(out, -1) {
		failed = append(failed, string(m[1]))
	}
	for _, m := range runLine.FindAllSubmatch(out, -1) {
		name := string(m[1])
		if strings.Contains(name, "/") {
			continue
		}
		started++
		if timedOut && !done[name] {
			failed = append(failed, name)
		}
	}
	return started, failed
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
