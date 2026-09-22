// Package assign exercises the assignop and assign operators.
package assign

// Arithmetic, bitwise and shift compound assignments are swapped and, as a
// second mutant, lose their accumulation.
func Accumulate(a, b int) int {
	a += b
	a -= b
	a *= b
	a /= b
	a %= b
	return a
}

func Bits(a, b uint8) uint8 {
	a &= b
	a |= b
	a ^= b
	a &^= b
	return a
}

// A shift's count has its own type, so dropping the accumulation does not
// type-check when it is not assignable to the left operand.
func ShiftBy(a int64, n uint) int64 {
	a <<= n
	a >>= n
	return a
}

// += on strings has no swap that type-checks; dropping it does.
func Join(s, t string) string {
	s += t
	return s
}

// The store of an assignment is dropped, but a short variable declaration,
// a blank target and `= nil` are not sites.
func Store(m map[string]int, k string, v int) int {
	n := v
	m[k] = n
	_ = n
	var p *int
	p = nil
	_ = p
	return m[k]
}

// A compound assignment in a for post statement keeps its swap; the form
// that drops it is an if statement, which a post statement cannot hold.
func Sum(xs []int) int {
	total := 0
	for i := 0; i < len(xs); i += 2 {
		total += xs[i]
	}
	return total
}

// The swap's schemata evaluate the left operand twice, so an index computed
// by a call is declined.
func AtCall(xs []int, at func() int) {
	xs[at()] += 1
}
