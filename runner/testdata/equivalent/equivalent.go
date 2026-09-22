// Package equivalent is a runner fixture for the EQUIVALENT and
// SUSPECT_EQUIVALENT statuses: Scale's swap is equivalent by a static
// rule, Max's boundary mutant survives on the same path its tests take
// without it, and Record's survives on a path its test never took.
package equivalent

// Scale multiplies by one; its "* -> /" mutant divides by one instead,
// which a static rule proves equivalent.
func Scale(x int) int {
	return x * 1
}

// Max's "> -> >=" mutant only differs when a == b, where both branches
// return the same value: no test can kill it, and the tests reach the
// same sites with it as without it.
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// last is written by note and never read by a test.
var last int

// Record's "> -> >=" mutant calls note for n == 1, which no test
// observes: it survives, but its test now reaches note's sites.
func Record(n int) {
	if n > 1 {
		note(n)
	}
}

func note(n int) {
	last = n + 1
}
