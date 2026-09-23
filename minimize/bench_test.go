package minimize_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/illumination-k/mutrim/criteria"
	"github.com/illumination-k/mutrim/minimize"
)

// synthetic builds the rows of a matrix shaped like a real run's: each
// test reaches a window of neighboring sites, as a test exercises a few
// functions, and kills part of the mutants it reaches. A few tests reach
// far more than the rest, as integration tests do.
func synthetic(tests, sites int) (map[string]int64, []criteria.Weighted) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // reproducible, not secret
	reach := criteria.SiteCoverage{}
	kills := criteria.Mutation{}
	durations := map[string]int64{}
	for i := range tests {
		name := fmt.Sprintf("Test%05d", i)
		width := 1 + r.IntN(sites/50)
		if r.IntN(20) == 0 {
			width = sites / 4
		}
		start := r.IntN(sites - width)
		for s := start; s < start+width; s++ {
			id := fmt.Sprintf("%016x", s)
			reach[name] = append(reach[name], id)
			if r.IntN(3) > 0 {
				kills[name] = append(kills[name], id)
			}
		}
		durations[name] = 1 + int64(r.IntN(100))
	}
	return durations, []criteria.Weighted{{Criterion: reach, Weight: 1}, {Criterion: kills, Weight: 5}}
}

// sizes run from a small package to a large one with subtests as rows.
var sizes = []struct{ tests, sites int }{{100, 1000}, {1000, 10000}, {3000, 30000}}

// BenchmarkGreedy measures the cover.
func BenchmarkGreedy(b *testing.B) {
	for _, size := range sizes {
		durations, crit := synthetic(size.tests, size.sites)
		m := criteria.Compose(durations, crit...)
		b.Run(fmt.Sprintf("tests=%d/sites=%d", size.tests, size.sites), func(b *testing.B) {
			var res minimize.Result
			for b.Loop() {
				res = minimize.Greedy(m, minimize.Options{})
			}
			b.ReportMetric(float64(len(res.Selected)), "selected")
		})
	}
}

// BenchmarkExclusives measures the reporting of a cover, separate from
// it: the transpose it builds is the size of the matrix itself
// (requirements × tests bits), which could never fit inside the cover.
func BenchmarkExclusives(b *testing.B) {
	for _, size := range sizes {
		durations, crit := synthetic(size.tests, size.sites)
		m := criteria.Compose(durations, crit...)
		res := minimize.Greedy(m, minimize.Options{})
		b.Run(fmt.Sprintf("tests=%d/sites=%d", size.tests, size.sites), func(b *testing.B) {
			for b.Loop() {
				minimize.Exclusives(m, &res)
			}
		})
	}
}

// BenchmarkCompose measures building the matrix Greedy runs on.
func BenchmarkCompose(b *testing.B) {
	for _, size := range sizes {
		durations, crit := synthetic(size.tests, size.sites)
		b.Run(fmt.Sprintf("tests=%d/sites=%d", size.tests, size.sites), func(b *testing.B) {
			for b.Loop() {
				criteria.Compose(durations, crit...)
			}
		})
	}
}

// BenchmarkDominators measures dropping the subsumed mutants from the
// kill criterion before Compose.
func BenchmarkDominators(b *testing.B) {
	for _, size := range sizes {
		_, crit := synthetic(size.tests, size.sites)
		kills := crit[1].Criterion.(criteria.Mutation) //nolint:forcetypeassert // synthetic's second criterion
		b.Run(fmt.Sprintf("tests=%d/sites=%d", size.tests, size.sites), func(b *testing.B) {
			var dominators criteria.Mutation
			for b.Loop() {
				dominators = kills.Dominators()
			}
			b.ReportMetric(float64(len(dominators.Mutants())), "dominators")
		})
	}
}
