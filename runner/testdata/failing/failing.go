// Package failing is a runner fixture for Options.SkipFailing. With
// MUTRIM_FAILING set some of its tests fail without a mutant: always, in
// the whole baseline only, when traced on their own only, or by panicking
// and so hiding the tests after it. Without it every test passes, so the
// fixture passes under a plain `go test`.
package failing

// Double is asserted on by the passing tests, so its mutants are killed.
func Double(x int) int { return x * 2 }
