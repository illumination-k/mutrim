// Package runerror is a runner fixture for the RUN_ERROR status: the
// branch mutant of Die's if empties its body, so the test binary falls
// through to os.Exit(3) and dies from outside the tests, before any
// test asserts anything. The exit happens on a mutant's first execution
// only, so a previous report that copied the RUN_ERROR forward would
// report RUN_ERROR again while one that executes the mutant again reports
// a kill: the test binary returns 0, the test fails, the run is a
// regular one. The claim (the flaky fixture's, keyed by GOMUTANT_ID)
// keeps the first execution of every mutant apart from its later ones.
package runerror

import (
	"os"
	"path/filepath"
)

// died claims the exit of the mutant GOMUTANT_ID selects: the first
// process to run under that ID exits, later ones return 0 instead.
func died() bool {
	id := os.Getenv("GOMUTANT_ID")
	dir := os.Getenv("MUTRIM_RUNERROR_STATE")
	if id == "" || dir == "" {
		return false
	}
	return os.Mkdir(filepath.Join(dir, id), 0o700) == nil
}

// Die returns x for positive x. Its branch mutant (the if body emptied)
// makes the caller fall through to the exit below: the test binary dies
// with no --- FAIL: line and a code the testing package never uses, so
// the run of that mutant is a RUN_ERROR, not a kill.
func Die(x int) int {
	if x > 0 {
		return x
	}
	// Only a run whose mutant skips the body above reaches this far; the
	// first execution of one exits, later ones return 0 instead.
	if died() {
		os.Exit(3)
	}
	return 0
}
