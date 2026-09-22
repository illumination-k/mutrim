// Package calc is a fixture for mutation_test: helpers whose tests kill
// every mutant, plus one that no test covers.
package calc

// Abs returns the absolute value of x.
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Clamp limits x to the range [lo, hi].
func Clamp(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// Sum adds the numbers.
func Sum(xs ...int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// Untested has no test, so its mutants live and show up in report.json.
func Untested(x int) int { return x * 2 }

// PoolSize is suppressed by an inline directive, so its mutants are
// reported IGNORED instead of being built and run.
//
//mutrim:disable-func the pool size is tuned by hand
func PoolSize(cpus int) int { return cpus*2 + 1 }
