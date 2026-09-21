// Package stats is a fixture for mutation_test with a dependency on another
// package of the workspace and on the standard library.
package stats

import (
	"errors"

	"github.com/illumination-k/mutrim/examples/calc"
)

// ErrEmpty is returned for an empty sample.
var ErrEmpty = errors.New("stats: empty sample")

// Mean returns the average of xs.
func Mean(xs ...int) (float64, error) {
	if len(xs) == 0 {
		return 0, ErrEmpty
	}
	return float64(calc.Sum(xs...)) / float64(len(xs)), nil
}

// Spread returns the distance between the largest and smallest of xs.
func Spread(xs ...int) int {
	if len(xs) == 0 {
		return 0
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs[1:] {
		lo = min(lo, x)
		hi = max(hi, x)
	}
	return calc.Abs(hi - lo)
}
