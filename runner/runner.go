// Package runner executes a compiled test binary once per mutant and
// classifies the outcome. It is what a Bazel mutation_test target runs; it
// only needs the binary, mutants.json and the GOMUTANT_ID convention of the
// mut runtime.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
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
	// Tests restricts the run to these top-level tests; nil runs everything.
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

// Run executes every viable mutant of the shard and returns the report.
// The baseline (no mutant) must pass first, or Run fails.
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

	base, err := o.exec(ctx, "", 0)
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

	previous := map[string]Result{}
	if o.Previous != nil {
		for _, r := range o.Previous.Results {
			previous[r.MutantID] = r
		}
	}

	report := &Report{BaselineMS: base.DurationMS, TimeoutMS: timeout.Milliseconds(), Results: []Result{}}
	for _, m := range o.Mutants {
		if !inShard(m.ID, o.Shard, o.Shards) {
			continue
		}
		var r Result
		switch prev, cached := previous[m.ID]; {
		case !m.Viable:
			r = Result{MutantID: m.ID, Status: NotViable}
		case cached && prev.Status != NotViable:
			r = prev
			logger.Printf("%s %s (previous)", m.ID, r.Status)
		default:
			res, err := o.exec(ctx, m.ID, timeout)
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
	failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
)

// exec runs the test binary with mutant id active (none when empty) and
// classifies the exit. A zero timeout means none.
func (o Options) exec(ctx context.Context, id string, timeout time.Duration) (*execResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	args := []string{"-test.v", "-test.failfast"}
	if len(o.Tests) > 0 {
		quoted := make([]string, len(o.Tests))
		for i, t := range o.Tests {
			quoted[i] = regexp.QuoteMeta(t)
		}
		args = append(args, "-test.run", "^("+strings.Join(quoted, "|")+")$")
	}
	args = append(args, o.Args...)

	cmd := exec.CommandContext(ctx, o.TestBin, args...) //nolint:gosec // running the user's test binary is the point
	cmd.Dir = o.Dir
	cmd.Env = append(envWithout("GOMUTANT_ID"), "GOMUTANT_ID="+id)
	cmd.WaitDelay = time.Second

	start := time.Now()
	out, err := cmd.CombinedOutput()
	res := &execResult{output: out}
	res.MutantID = id
	res.DurationMS = time.Since(start).Milliseconds()
	for _, m := range runLine.FindAllSubmatch(out, -1) {
		if !strings.Contains(string(m[1]), "/") {
			res.TestsRun++
		}
	}
	for _, m := range failLine.FindAllSubmatch(out, -1) {
		res.KilledBy = append(res.KilledBy, string(m[1]))
	}

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

func envWithout(key string) []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}
