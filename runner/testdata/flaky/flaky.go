// Package flaky is a runner fixture for the confirmation reruns. Its tests
// fail on the first process of a phase and pass on every later one, so a
// single run and a confirmed run disagree about them. The phase is read
// from the environment the runner itself sets, so the baseline always
// passes: only tracing (GOMUTANT_TRACE) and one named mutant
// (MUTRIM_FLAKY_MUTANT) are made to flake.
package flaky

// Compared is asserted on by TestStable, so the kills of its mutants are
// real ones.
func Compared(a, b int) bool { return a < b }

// Unasserted is reached by every test of the fixture and checked by none,
// so the only failure ever recorded against its mutants is the fixture's
// flaky one.
func Unasserted(a, b int) int { return a + b }
