package runner_test

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/illumination-k/mutrim/runner"
)

// BenchmarkRun measures a whole run over the schemata fixture, one process
// at a time and GOMAXPROCS at a time. The fixture's looping mutants wait
// out the timeout, as real ones do, so it is short but not zero.
func BenchmarkRun(b *testing.B) {
	bin, mutants := buildFixture(b)
	for _, jobs := range []int{1, runtime.GOMAXPROCS(0)} {
		b.Run(fmt.Sprintf("jobs=%d", jobs), func(b *testing.B) {
			for b.Loop() {
				rep, err := runner.Run(b.Context(), runner.Options{
					TestBin: bin, Mutants: mutants, Dir: fixtureDir, Timeout: time.Second, Jobs: jobs,
				})
				if err != nil {
					b.Fatal(err)
				}
				if rep.Totals.Killed == 0 {
					b.Fatal("no mutant killed")
				}
			}
			b.ReportMetric(float64(b.Elapsed().Milliseconds())/float64(b.N)/float64(len(mutants)), "ms/mutant")
		})
	}
}
