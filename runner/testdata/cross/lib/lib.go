// Package lib is a runner fixture for cross-package kills: its own test
// only checks a value inside the range, so the mutants of Clamp's bounds
// survive it, and only the tests of package app, which imports lib, kill
// them.
package lib

// Clamp limits x to [lo, hi].
func Clamp(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
