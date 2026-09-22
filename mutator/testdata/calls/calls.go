// Package calls exercises the method operator and the opt-in call operator.
package calls

import (
	"slices"
	"strings"
	"time"
)

// A library call is replaced by its mirror image.
func Prefixed(s string) bool { return strings.HasPrefix(s, "go") }

func Lower(s string) string { return strings.ToLower(s) }

// A generic function has no function value without instantiation, so the
// swap is applied but not lowered.
func Smallest(xs []int) int { return slices.Min(xs) }

// A method swap binds its receiver, which must not be a call: evaluating it
// twice could repeat a side effect.
func Earlier(a, b time.Time) bool { return a.Before(b) }

func EarlierCall(now func() time.Time, b time.Time) bool { return now().Before(b) }

// A call whose single result has a predeclared type is replaced by that
// type's zero value, so the call never happens.
func Count(s string) int { return width(s) + 1 }

func width(s string) int { return len(s) }

// A conversion, a builtin, a multi-value call and a result whose type has no
// predeclared name are no sites of the call operator.
func Widths(xs []string) []int {
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		out = append(out, int(int64(width(x))))
	}
	return out
}

func Split(s string) (string, string, bool) { return strings.Cut(s, "=") }

// A call in a go or defer statement, and one whose arguments call recover,
// keep their site out of the closure the rewrite would build.
func Deferred(s string) int {
	defer width(s)
	go width(s)
	return width(s)
}

func Recovered() (n int) {
	defer func() { n = boxed(recover()) }()
	panic("boom")
}

func boxed(v any) int {
	if v == nil {
		return 0
	}
	return 1
}
