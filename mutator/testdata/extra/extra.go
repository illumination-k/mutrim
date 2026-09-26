package extra

import "strings"

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

// Words counts the fields of s.
func Words(s string) int {
	n := 0
	for range strings.Fields(s) {
		n++
	}
	return n
}
