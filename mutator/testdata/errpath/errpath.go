// Package errpath exercises the errpath operator.
package errpath

import (
	"errors"
	"fmt"
	"strconv"
)

// ErrEmpty is the sentinel Parse wraps.
var ErrEmpty = errors.New("empty")

// Parse parses s as an integer, wrapping its errors.
func Parse(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("parse %q: %w", s, ErrEmpty)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse: %w", err)
	}
	return n, nil
}

// IsEmpty reports whether err wraps ErrEmpty.
func IsEmpty(err error) bool {
	return errors.Is(err, ErrEmpty)
}

// NumError returns the *strconv.NumError err wraps, or nil.
func NumError(err error) *strconv.NumError {
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return ne
	}
	return nil
}

// Must panics on a negative n.
func Must(n int) {
	if n < 0 {
		panic("negative")
	}
}

// Check returns n, panicking on a negative one first.
func Check(n int) int {
	if n < 0 {
		panic("negative")
	}
	return n
}

// Abs needs its panic as the terminating statement: no site.
func Abs(n int) int {
	if n >= 0 {
		return n
	}
	panic("negative")
}

// Safe calls f and turns its panic into an error.
func Safe(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	f()
	return nil
}

// Discard swallows every panic of f; its recover has no site.
func Discard(f func()) {
	defer func() { recover() }()
	f()
}

// Sign needs the panic of its last case.
func Sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		panic("zero")
	}
}
