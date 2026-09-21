package ret

import "errors"

type Pair struct{ A, B int }

type ID int

func Basic(x int) int     { return x }
func Str(s string) string { return s }
func Bool(b bool) bool    { return b }
func Ptr(p *int) *int     { return p }
func Slice(s []int) []int { return s }

// Err's mutant leaves "errors" unused, so it does not compile.
func Err() error               { return errors.New("x") }
func Named(x ID) ID            { return x }
func Multi(x int) (int, error) { return x, nil }
func Fn() func()               { return func() {} }

// Skipped: struct result, generic result, already-zero results, multi-value
// call, bare return.
func Struct(p Pair) Pair         { return p }
func Generic[T any](v T) T       { return v }
func AlreadyZero() (int, error)  { return 0, nil }
func Forward(x int) (int, error) { return Multi(x) }
func Bare() (n int)              { return }
func Array(a [2]int) [2]int      { return a }
