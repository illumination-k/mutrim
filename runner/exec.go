// Process execution: how a test binary runs, how its exit is classified,
// and how the runs are traced and rerun against a mutant. What runs, in
// what order, and what the output means is runner.go's.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

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
	tests := make([]Test, len(rows))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(o.jobs())
	for i, rw := range rows {
		g.Go(func() error {
			t, err := o.traceTest(ctx, rw, runs, func(run int) string {
				return filepath.Join(dir, fmt.Sprintf("%d-%d.trace", i, run))
			})
			tests[i] = t
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return tests, nil
}

// traceTest runs row rw runs times on its own, tracing run k to trace(k),
// and merges the runs as traceTests describes.
func (o Options) traceTest(ctx context.Context, rw row, runs int, trace func(run int) string) (Test, error) {
	name := o.qualify(rw)
	top, _, _ := strings.Cut(rw.name, "/")
	t := Test{Name: name, Pkg: o.bins[rw.bin].Pkg, Hash: o.hashes[rw.bin][top]}
	if p := parent(rw.name); p != "" {
		t.Parent = o.qualify(row{rw.bin, p})
	}
	sites := map[string]bool{}
	var failures int
	var failed *execResult
	for run := range runs {
		res, err := o.exec(ctx, o.bins[rw.bin], "", trace(run), testPattern([]string{rw.name}), 0)
		if err != nil {
			return Test{}, err
		}
		if res.Status == RunError {
			return Test{}, fmt.Errorf("runner: %s: infrastructure failure while run on its own; the tests must pass without a mutant\n%s", name, res.output)
		}
		if res.Status != Lived {
			failures++
			failed = res
		}
		reached, err := readTrace(trace(run))
		if err != nil {
			return Test{}, err
		}
		maps.Copy(sites, reached)
		t.DurationMS = max(t.DurationMS, res.DurationMS)
	}
	if failures == runs {
		return Test{}, fmt.Errorf("runner: %s fails when run on its own (%s); the tests must pass without a mutant\n%s", name, failed.Status, failed.output)
	}
	t.Flaky = failures > 0
	t.Sites = slices.Sorted(maps.Keys(sites))
	return t, nil
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

type execResult struct {
	Status     Status
	DurationMS int64
	output     []byte
}

var (
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
