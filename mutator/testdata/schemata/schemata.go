// Package schemata exercises every lowering path of the schemata rewrite.
// Its tests kill every embedded mutant except those of Untested, so the
// runner's report golden doubles as a check that each lowering is effective.
package schemata

import (
	"errors"
	"slices"
	"strings"
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

// One result of several is replaced on its own, which is how an untested
// error path shows up; dropping a unary ! is a mutant of its own.
func Halve(x int) (int, error) {
	if x%2 != 0 {
		return 0, ErrTarget
	}
	return x / 2, nil
}

func IsEmpty(s []int) bool { return len(s) == 0 }

func NotEmpty(s []int) bool { return !IsEmpty(s) }

// Sites the lowering declines, so their mutants are reported NOT_VIABLE
// under schemata while the rest of the function is still embedded.
func SkipConst() int {
	const c = 2 * 3
	return c
}

func SkipNamedBool(a, b int) Flag { return a < b }

func SkipNamedBoolOperands(a, b Flag) Flag { return a && b }

func SkipRecover(f func()) (caught bool) {
	defer func() { caught = caught || recover() != nil }()
	f()
	return false
}

func SkipMapPost(m map[int]int, k int) {
	for ; m[k] < 3; m[k]++ {
	}
}

// Bitwise operators, shifts and unary minus.
func Mask(a, b uint8) uint8     { return a & b }
func Either(a, b int) int       { return a | b }
func Shl(a int64, n uint) int64 { return a << n }
func Negate(x int) int          { return -x }

// Call statements the lowering removes; those in a for init or post
// statement are declined, like a condition of a defined bool type.
func Record(log *[]string, s string) { *log = append(*log, s) }

func Twice(log *[]string) {
	Record(log, "a")
	Record(log, "b")
}

func SkipInitCall(log *[]string) int {
	n := 0
	for Record(log, "init"); n < 2; Record(log, "post") {
		n += 1
	}
	return n
}

func SkipNamedBoolCond(f Flag) int {
	if f {
		return 1
	}
	return 0
}

// Numeric literals: a typed basic literal goes through the runtime with
// its type spelled out (go/types gives a literal its converted type even
// in an interface context); a defined type and an operand of a constant
// shift are declined; where Go requires a constant there is no site.
func Offset(x int64) int64                         { return x + 1 }
func Quarter(x float64) float64                    { return x * 0.25 }
func SkipNamedConst(d time.Duration) time.Duration { return d + 1 }
func Boxed() any                                   { return 7 }
func SkipShiftConst(x float64) float64             { return 1<<2 + x }
func NoSiteArrayLen() int                          { var a [2]int; return len(a) }
func NoSiteArrayKey() int                          { return []int{3: 9}[3] }

// break and continue are each other's mutant; a range loop runs no
// iteration, and a for condition is forced to false.
func FirstEven(xs []int) int {
	for _, x := range xs {
		if x%2 != 0 {
			continue
		}
		return x
	}
	return -1
}

func CountUntil(xs []int, stop int) int {
	n := 0
	for _, x := range xs {
		if x == stop {
			break
		}
		n++
	}
	return n
}

// Emptying the body of an if, an else, a case or a select clause. A body
// whose last statement makes the enclosing statement terminating (Classify's
// case, Drain's default) keeps no schemata form.
func Bound(x, lo int) int {
	if x < lo {
		x = lo
	} else {
		x = x + 1
	}
	return x
}

func Describe(n int) string {
	out := "zero"
	switch {
	case n < 0:
		out = "negative"
	case n > 0:
		out = "positive"
	default:
		out += "!"
	}
	return out
}

func Drain(ch <-chan int) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// Library-call swaps: a package function goes through the runtime as a
// function value, a generic one has no function value to select.
func Prefixed(s string) bool { return strings.HasPrefix(s, "go") }

func Smallest(xs []int) int { return slices.Min(xs) }

// Composite literals lose their elements, boolean literals flip, and a
// compound assignment is swapped or loses its accumulation.
type Config struct {
	Names   []string
	Verbose bool
	Retries int
}

func NewConfig(extra []string) Config {
	c := Config{Names: []string{"a"}, Verbose: true, Retries: 2}
	c.Names = append(c.Names, extra...)
	c.Retries *= 3
	return c
}

// Untested has no test, so its mutants live.
func Untested(x int) int { return x + 1 }
