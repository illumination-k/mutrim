// Package schemata exercises every lowering path of the schemata rewrite.
// Its tests kill every embedded mutant except those of Untested, so the
// runner's report golden doubles as a check that each lowering is effective.
package schemata

import (
	"errors"
	"time"
)

type Celsius float64

type Flag bool

var ErrTarget = errors.New("target")

// Ordered comparisons: ints, floats, strings, a defined type, and an
// untyped constant on either side.
func Less(a, b int) bool          { return a < b }
func LessEq(a, b float64) bool    { return a <= b }
func Greater(a, b string) bool    { return a > b }
func GreaterEq(a Celsius) bool    { return a >= 0 }
func ConstLeft(x int) bool        { return 10 < x }
func IsNil(p *int) bool           { return p == nil }
func NotNil(s []int) bool         { return s != nil }
func IsTarget(err error) bool     { return err == ErrTarget }
func SameInterface(a, b any) bool { return a == b }

// Arithmetic on every helper path: Arith, ArithInt, defined types, type
// parameters, complex numbers, untyped constants.
func Add(a, b int) int                      { return a + b }
func Sub(a, b float64) float64              { return a - b }
func Scale(d time.Duration) time.Duration   { return d * 2 }
func Div(a, b int) int                      { return a / b }
func Mod(a, b int) int                      { return a % b }
func MulComplex(a, b complex128) complex128 { return a * b }
func Half(x float64) float64                { return x * 0.5 }
func Sum[T int | float64](a, b T) T         { return a + b }
func Shift(n uint) int                      { return 1<<n + 1 }

// Logical operators must keep short-circuit evaluation and nest with
// comparisons.
func And(a bool, b func() bool) bool { return a && b() }
func Or(a bool, b func() bool) bool  { return a || b() }
func Between(a, b, c int) bool       { return a < b && b < c || a == c }

// Statements: negation, ++/-- in every position, returns.
func Sign(x int) int {
	if x > 0 {
		return 1
	}
	return -1
}

func CountTo(n int) int {
	c := 0
	for i := 0; i < n; i++ {
		c++
	}
	return c
}

func Tally(m map[string]int, k string) { m[k]++ }

func Decrement(p *int) { *p-- }

type Counter struct{ n int }

func (c *Counter) Inc() { c.n++ }

func Classify(x int) string {
	switch {
	case x < 0:
		return "negative"
	}
	return "non-negative"
}

func Pair(x int) (int, error) { return x, nil }

// Sites the lowering declines, so their mutants are reported NOT_VIABLE
// under schemata while the rest of the function is still embedded.
func SkipConst() int {
	const c = 2 * 3
	return c
}

func SkipNamedBool(a, b int) Flag { return a < b }

func SkipNamedBoolOperands(a, b Flag) Flag { return a && b }

func SkipRecover(f func()) (caught bool) {
	defer func() { caught = f != nil && recover() != nil }()
	f()
	return false
}

func SkipMapPost(m map[int]int) {
	for ; m[0] < 3; m[0]++ {
	}
}

// Untested has no test, so its mutants live.
func Untested(x int) int { return x + 1 }
