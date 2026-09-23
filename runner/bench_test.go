package runner_test

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// BenchmarkRun measures a whole run of the schemata fixture (baseline,
// tracing, every mutant), one process at a time and GOMAXPROCS at a time.
// The mutants that time out are dropped first: they only wait out the
// timeout, a fixed cost that would drown what the runner itself costs.
func BenchmarkRun(b *testing.B) {
	bin, mutants := buildFixture(b)
	opts := runner.Options{TestBin: bin, Dir: fixtureDir, Timeout: time.Second}
	mutants = withoutTimeouts(b, opts, mutants)
	opts.Mutants = mutants
	for _, jobs := range []int{1, runtime.GOMAXPROCS(0)} {
		b.Run(fmt.Sprintf("jobs=%d", jobs), func(b *testing.B) {
			opts.Jobs = jobs
			for b.Loop() {
				if _, err := runner.Run(b.Context(), opts); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(b.Elapsed().Seconds()/float64(b.N)/float64(len(mutants)), "sec/mutant")
		})
	}
}

// withoutTimeouts runs mutants once and returns those that did not time out.
func withoutTimeouts(b *testing.B, opts runner.Options, mutants []mutator.Mutant) []mutator.Mutant {
	b.Helper()
	opts.Mutants = mutants
	rep, err := runner.Run(b.Context(), opts)
	if err != nil {
		b.Fatal(err)
	}
	timedOut := map[string]bool{}
	for _, r := range rep.Results {
		timedOut[r.MutantID] = r.Status == runner.Timeout
	}
	var out []mutator.Mutant
	for _, m := range mutants {
		if !timedOut[m.ID] {
			out = append(out, m)
		}
	}
	return out
}
